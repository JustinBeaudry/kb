package platform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

func TestTelemetryContainsNoFixtureSecrets(t *testing.T) {
	var out bytes.Buffer
	exporter := tracetest.NewInMemoryExporter()
	telemetry, err := NewTelemetry(&out, "api", exporter)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, telemetry.Shutdown(context.Background())) })
	secret := "fixture-source-prose-and-bearer"
	telemetry.Logger().With("authorization", secret).WithGroup("source").Error(secret, "error", errors.New(secret), "payload", map[string]string{"source": secret})
	telemetry.Logger().Info("http_request", "method", "GET", "status", 200, "request_id", "valid-but-user-controlled", "duration_ms", int64(2))
	handler := telemetry.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		telemetry.Logger().InfoContext(r.Context(), "http_request", "source", secret)
		w.WriteHeader(204)
	}))
	req := httptest.NewRequest("GET", "http://private.invalid/"+secret+"?token="+secret, strings.NewReader(secret))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("X-Request-Id", secret)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	require.NotEmpty(t, recorder.Header().Get("X-Request-Id"))
	assert.NotContains(t, out.String(), secret)
	for _, span := range exporter.GetSpans() {
		assert.NotContains(t, span.Name, secret)
		for _, attr := range span.Attributes {
			assert.NotContains(t, fmt.Sprint(attr.Value.AsInterface()), secret)
		}
	}
	assert.Len(t, exporter.GetSpans(), 1)
	assert.Contains(t, out.String(), `"status":204`)
}

func TestTelemetryPropagatesGeneratedRequestIDWithoutMutatingRequest(t *testing.T) {
	telemetry, err := NewTelemetry(io.Discard, "worker", tracetest.NewInMemoryExporter())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, telemetry.Shutdown(context.Background())) })
	var requestID string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID = r.Header.Get("X-Request-Id")
		assert.NotEmpty(t, r.Header.Get("traceparent"))
		assert.Empty(t, r.Header.Get("baggage"))
		w.WriteHeader(200)
	}))
	t.Cleanup(target.Close)
	var original *http.Request
	server := telemetry.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		original, err = http.NewRequestWithContext(r.Context(), "GET", target.URL, nil)
		require.NoError(t, err)
		response, e := (&http.Client{Transport: telemetry.HTTPClientTransport(nil)}).Do(original)
		require.NoError(t, e)
		require.NoError(t, response.Body.Close())
	}))
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest("GET", "http://private.invalid/", nil))
	assert.Equal(t, recorder.Header().Get("X-Request-Id"), requestID)
	assert.Empty(t, original.Header.Get("X-Request-Id"))
}

func TestTelemetryTraceIOReportsSafeFailure(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	telemetry, err := NewTelemetry(io.Discard, "graph", exporter)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, telemetry.Shutdown(context.Background())) })
	failure := errors.New("private source")
	assert.ErrorIs(t, telemetry.TraceIO(t.Context(), "graph_connect", func(context.Context) error { return failure }), failure)
	assert.Error(t, telemetry.TraceIO(t.Context(), "source-private", func(context.Context) error { return nil }))
	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Empty(t, spans[0].Events)
	assert.Equal(t, "unavailable", spans[0].Status.Description)
}

func TestTelemetryRejectsInvalidFactoryConfiguration(t *testing.T) {
	for _, name := range []string{"", "source prose", "private-endpoint"} {
		_, err := NewTelemetry(io.Discard, name)
		require.Error(t, err)
	}
	_, err := NewTelemetry(nil, "api")
	require.Error(t, err)
	var out bytes.Buffer
	telemetry, err := NewTelemetry(&out, "api")
	require.NoError(t, err)
	telemetry.Logger().Info("server_stopped", "reason", "configuration_or_service_unavailable")
	assert.Contains(t, out.String(), "configuration_or_service_unavailable")
	require.NoError(t, telemetry.Shutdown(t.Context()))
}

func TestTelemetryLabelsProcessWithBoundedService(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	telemetry, err := NewTelemetry(io.Discard, "api", exporter)
	require.NoError(t, err)
	require.NoError(t, telemetry.TraceIO(t.Context(), "graph_connect", func(context.Context) error { return nil }))
	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	value, ok := spans[0].Resource.Set().Value("service.name")
	require.True(t, ok)
	assert.Equal(t, "kb-api", value.AsString())
	require.NoError(t, telemetry.Shutdown(t.Context()))
}

func TestTelemetryClientCompletesAtBodyEOFAndKeepsProseOutOfSpan(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	telemetry, err := NewTelemetry(io.Discard, "worker", exporter)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, telemetry.Shutdown(context.Background())) })
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.Header.Get("baggage"))
		assert.Empty(t, r.Header.Get("tracestate"))
		_, err := io.WriteString(w, "fixture private source prose")
		require.NoError(t, err)
	}))
	t.Cleanup(target.Close)
	state, err := oteltrace.ParseTraceState("private=fixture")
	require.NoError(t, err)
	traceID, err := oteltrace.TraceIDFromHex("11111111111111111111111111111111")
	require.NoError(t, err)
	spanID, err := oteltrace.SpanIDFromHex("1111111111111111")
	require.NoError(t, err)
	parent := oteltrace.ContextWithRemoteSpanContext(t.Context(), oteltrace.NewSpanContext(oteltrace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceState: state, TraceFlags: oteltrace.FlagsSampled, Remote: true}))
	request, err := http.NewRequestWithContext(parent, "GET", target.URL, nil)
	require.NoError(t, err)
	request.Header.Set("baggage", "private=fixture")
	request.Header.Set("tracestate", "private=fixture")
	response, err := telemetry.HTTPClientTransport(nil).RoundTrip(request)
	require.NoError(t, err)
	assert.Empty(t, exporter.GetSpans())
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, "fixture private source prose", string(body))
	require.NoError(t, response.Body.Close())
	assert.Len(t, exporter.GetSpans(), 1)
	assert.NotContains(t, fmt.Sprint(exporter.GetSpans()), "fixture private source prose")
	assert.Equal(t, "private=fixture", request.Header.Get("baggage"))
}

func TestTelemetryRecordsTransportFailureWithoutErrorText(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	telemetry, err := NewTelemetry(io.Discard, "worker", exporter)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, telemetry.Shutdown(context.Background())) })
	failure := errors.New("fixture private credential")
	request := httptest.NewRequest("SECRET_METHOD", "http://private.invalid/", nil)
	_, err = telemetry.HTTPClientTransport(telemetryTestTransport(func(*http.Request) (*http.Response, error) { return nil, failure })).RoundTrip(request)
	assert.ErrorIs(t, err, failure)
	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "unavailable", spans[0].Status.Description)
	assert.NotContains(t, fmt.Sprint(spans), failure.Error())
	assert.Contains(t, fmt.Sprint(spans[0].Attributes), "OTHER")
}

type telemetryTestTransport func(*http.Request) (*http.Response, error)

func (f telemetryTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTelemetryResponsePreservesControllerUnwrapAndImplicitStatus(t *testing.T) {
	var out bytes.Buffer
	telemetry, err := NewTelemetry(&out, "api")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, telemetry.Shutdown(context.Background())) })
	recorder := httptest.NewRecorder()
	telemetry.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, http.NewResponseController(w).Flush())
		_, e := w.Write([]byte("ok"))
		require.NoError(t, e)
	})).ServeHTTP(recorder, httptest.NewRequest("GET", "http://private.invalid/", nil))
	assert.Equal(t, "ok", recorder.Body.String())
	assert.Contains(t, out.String(), `"status":200`)
	_, err = NewTelemetry(io.Discard, "api", nil)
	require.Error(t, err)
	telemetry.Logger().With("ready", true).Info("maintenance_closed", "live", true, "journal_capacity_warning", true, "journal_sequence", int64(9000), "journal_capacity", int64(10000))
	assert.Contains(t, out.String(), `"journal_sequence":9000`)
}

func TestTelemetryExporterFailureCannotReachUnredactedSDKHandler(t *testing.T) {
	var out bytes.Buffer
	var unfiltered []error
	previous := otel.GetErrorHandler()
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) { unfiltered = append(unfiltered, err) }))
	t.Cleanup(func() { otel.SetErrorHandler(previous) })
	telemetry, err := NewTelemetry(&out, "api", telemetryFailingExporter{})
	require.NoError(t, err)
	require.NoError(t, telemetry.TraceIO(t.Context(), "graph_connect", func(context.Context) error { return nil }))
	assert.Empty(t, unfiltered)
	assert.Contains(t, out.String(), "telemetry_export_failed")
	assert.NotContains(t, out.String(), "fixture secret exporter failure")
	err = telemetry.Shutdown(t.Context())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "fixture secret exporter failure")
}

type telemetryFailingExporter struct{}

func (telemetryFailingExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return errors.New("fixture secret exporter failure")
}
func (telemetryFailingExporter) Shutdown(context.Context) error {
	return errors.New("fixture secret exporter failure")
}

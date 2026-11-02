package platform

import (
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Telemetry owns one process logger and provider. An exporter is explicit: no
// ambient backend credentials, exporter endpoint, baggage or source capture.
type Telemetry struct {
	logger   *slog.Logger
	provider *sdktrace.TracerProvider
	tracer   trace.Tracer
}

func NewTelemetry(w io.Writer, serviceName string, exporters ...sdktrace.SpanExporter) (*Telemetry, error) {
	if w == nil || !oneOf(serviceName, "api", "worker", "graph", "admin") {
		return nil, domain.ErrInvalidInput
	}
	handler := &safeLogHandler{next: slog.NewJSONHandler(w, nil), service: serviceName}
	logger := slog.New(handler)
	options := []sdktrace.TracerProviderOption{sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "kb-"+serviceName))), sdktrace.WithRawSpanLimits(sdktrace.SpanLimits{AttributeValueLengthLimit: 64, AttributeCountLimit: 8, EventCountLimit: 0, LinkCountLimit: 0, AttributePerEventCountLimit: 0, AttributePerLinkCountLimit: 0})}
	for _, exporter := range exporters {
		if exporter == nil {
			return nil, domain.ErrInvalidInput
		}
		options = append(options, sdktrace.WithSyncer(safeSpanExporter{exporter: exporter, logger: logger}))
	}
	provider := sdktrace.NewTracerProvider(options...)
	return &Telemetry{logger: logger, provider: provider, tracer: provider.Tracer("kb.lifecycle")}, nil
}
func (t *Telemetry) Logger() *slog.Logger               { return t.logger }
func (t *Telemetry) Shutdown(ctx context.Context) error { return t.provider.Shutdown(ctx) }

// TraceIO emits closed lifecycle names only; errors remain values and their
// text is never recorded. Call at graph connect/close/transaction boundaries.
func (t *Telemetry) TraceIO(ctx context.Context, operation string, fn func(context.Context) error) error {
	if fn == nil || !oneOf(operation, "graph_connect", "graph_close", "graph_transaction", "object_read", "object_write", "journal_barrier") {
		return domain.ErrInvalidInput
	}
	ctx, span := t.tracer.Start(ctx, operation)
	defer span.End()
	err := fn(ctx)
	if err != nil {
		span.SetStatus(codes.Error, "unavailable")
	}
	return err
}

func (t *Telemetry) HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Remote tracestate and baggage are deliberately not extracted. A caller's
		// request ID cannot introduce source prose into logs or outbound headers.
		id := rand.Text()
		ctx := domain.WithRequestID(r.Context(), id)
		carrier := propagation.MapCarrier{"traceparent": r.Header.Get("traceparent")}
		ctx = propagation.TraceContext{}.Extract(ctx, carrier)
		ctx, span := t.tracer.Start(ctx, "http_server", trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(attribute.String("http.request.method", safeMethod(r.Method))))
		defer span.End()
		w.Header().Set("X-Request-Id", id)
		recorder := &telemetryResponse{ResponseWriter: w, status: http.StatusOK}
		started := time.Now()
		next.ServeHTTP(recorder, r.WithContext(ctx))
		span.SetAttributes(attribute.Int("http.response.status_code", recorder.status))
		t.logger.InfoContext(ctx, "http_request", "method", safeMethod(r.Method), "status", recorder.status, "duration_ms", time.Since(started).Milliseconds())
	})
}

type telemetryResponse struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *telemetryResponse) WriteHeader(status int) {
	if !w.wrote {
		w.status = status
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *telemetryResponse) Write(p []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}
func (w *telemetryResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }

type telemetryTransport struct {
	telemetry *Telemetry
	base      http.RoundTripper
}

func (t *Telemetry) HTTPClientTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &telemetryTransport{t, base}
}
func (t *telemetryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	ctx, span := t.telemetry.tracer.Start(r.Context(), "http_client", trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attribute.String("http.request.method", safeMethod(r.Method))))
	cloned := r.Clone(ctx)
	cloned.Header.Del("baggage")
	cloned.Header.Del("tracestate")
	if id := domain.RequestID(ctx); id != "" {
		cloned.Header.Set("X-Request-Id", id)
	} else {
		cloned.Header.Set("X-Request-Id", rand.Text())
	}
	propagation.TraceContext{}.Inject(ctx, propagation.HeaderCarrier(cloned.Header))
	cloned.Header.Del("tracestate")
	response, err := t.base.RoundTrip(cloned)
	if err != nil {
		span.SetStatus(codes.Error, "unavailable")
		span.End()
		return response, err
	}
	span.SetAttributes(attribute.Int("http.response.status_code", response.StatusCode))
	if response.Body == nil {
		span.End()
	} else {
		response.Body = &telemetryBody{ReadCloser: response.Body, end: span.End}
	}
	return response, nil
}

type telemetryBody struct {
	io.ReadCloser
	once sync.Once
	end  func(...trace.SpanEndOption)
}

func (b *telemetryBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.once.Do(func() { b.end() })
	}
	return n, err
}
func (b *telemetryBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() { b.end() })
	return err
}

// A closed schema, including With/WithGroup, avoids relying on every caller to
// remember redaction. Unknown attributes and arbitrary messages are discarded.
type safeLogHandler struct {
	next    slog.Handler
	service string
}

func (h *safeLogHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}
func (h *safeLogHandler) Handle(ctx context.Context, r slog.Record) error {
	message := r.Message
	if !oneOf(message, "telemetry_export_failed", "http_request", "job_lease_lost", "job_failed", "worker_stopped", "server_stopped", "capture_reconciliation_incomplete", "supervisor_stopped", "backup_completed", "backup_failed", "restore_failed", "maintenance_closed") {
		message = "event"
	}
	cleaned := slog.NewRecord(r.Time, r.Level, message, r.PC)
	cleaned.AddAttrs(slog.String("service", h.service))
	r.Attrs(func(a slog.Attr) bool {
		if safe, ok := safeLogAttr(a); ok {
			cleaned.AddAttrs(safe)
		}
		return true
	})
	if id := domain.RequestID(ctx); id != "" {
		cleaned.AddAttrs(slog.String("request_id", id))
	}
	return h.next.Handle(ctx, cleaned)
}
func (h *safeLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	safe := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		if a, ok := safeLogAttr(a); ok {
			safe = append(safe, a)
		}
	}
	return &safeLogHandler{next: h.next.WithAttrs(safe), service: h.service}
}
func (h *safeLogHandler) WithGroup(string) slog.Handler { return h }
func safeLogAttr(a slog.Attr) (slog.Attr, bool) {
	// Do not resolve arbitrary LogValuer values, which may execute code or leak
	// typed vendor errors. Groups/Any values never cross this boundary.
	switch a.Key {
	case "status", "duration_ms", "journal_sequence", "journal_capacity", "backup_age_seconds", "active_operations", "unknown_operations":
		if a.Value.Kind() == slog.KindInt64 && a.Value.Int64() >= 0 {
			return a, true
		}
	case "ready", "live", "journal_capacity_warning":
		if a.Value.Kind() == slog.KindBool {
			return a, true
		}
	case "method":
		if a.Value.Kind() == slog.KindString {
			return slog.String("method", safeMethod(a.Value.String())), true
		}
	case "reason":
		if a.Value.Kind() == slog.KindString && oneOf(a.Value.String(), "heartbeat_failed", "handler_failed", "configuration_or_handlers_unavailable", "configuration_or_service_unavailable", "objects_or_policy_unavailable", "maintenance", "unavailable", "journal_unavailable", "journal_capacity", "backup_overdue", "unknown_operation") {
			return a, true
		}
	}
	return slog.Attr{}, false
}
func safeMethod(method string) string {
	if oneOf(method, "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "CONNECT", "TRACE") {
		return method
	}
	return "OTHER"
}
func oneOf(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}

// SDK processor errors otherwise reach its global unstructured error handler.
// Normalize at this boundary and report only safe status through our logger.
type safeSpanExporter struct {
	exporter sdktrace.SpanExporter
	logger   *slog.Logger
}

func (e safeSpanExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	if err := e.exporter.ExportSpans(ctx, spans); err != nil {
		e.logger.ErrorContext(ctx, "telemetry_export_failed", "reason", "unavailable")
	}
	return nil
}
func (e safeSpanExporter) Shutdown(ctx context.Context) error {
	if err := e.exporter.Shutdown(ctx); err != nil {
		e.logger.ErrorContext(ctx, "telemetry_export_failed", "reason", "unavailable")
		return domain.ErrUnavailable
	}
	return nil
}

package platform

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTelemetryRecordsGenerationUsageWithKnownFlags(t *testing.T) {
	for _, tc := range []struct {
		name    string
		usage   domain.Usage
		outcome string
		want    string
		cost    bool
	}{
		{"known usage", domain.Usage{Tokens: 1500, TokensKnown: true, CostMicroUSD: 8100, CostKnown: true}, "ok", "ok", true},
		{"unknown usage is omitted, not zero", domain.Usage{}, "unavailable", "unavailable", false},
		{"unlisted outcome is normalized", domain.Usage{}, "prompt text", "unavailable", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exporter := tracetest.NewInMemoryExporter()
			telemetry, err := NewTelemetry(io.Discard, "worker", exporter)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, telemetry.Shutdown(context.Background())) })

			telemetry.RecordGeneration(t.Context(), domain.GenerationRecord{Route: "primary", Model: "fixture-model", PolicyRevision: "policy-1", Outcome: tc.outcome, Usage: tc.usage, Duration: time.Second})

			spans := exporter.GetSpans()
			require.Len(t, spans, 1)
			assert.Equal(t, "model_generation", spans[0].Name)
			attrs := map[string]any{}
			for _, a := range spans[0].Attributes {
				attrs[string(a.Key)] = a.Value.AsInterface()
			}
			assert.Equal(t, tc.usage.CostKnown, attrs["cost_known"])
			_, hasCost := attrs["cost_micro_usd"]
			assert.Equal(t, tc.cost, hasCost)
			assert.Equal(t, tc.want, attrs["outcome"])
		})
	}
}

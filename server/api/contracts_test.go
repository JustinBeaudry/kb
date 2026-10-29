package api_test

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/handler"
	validator "github.com/oapi-codegen/echo-middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A request must not gain provenance or verification by inserting authoritative
// fields, including inside submitted evidence. This tests the generated contract,
// not the mere presence of schema source text.
func TestContractRejectsSubmittedAuthority(t *testing.T) {
	spec, err := handler.GetSpec()
	require.NoError(t, err)
	require.NoError(t, spec.Validate(t.Context()))
	_ = validator.OapiRequestValidator(spec)
	for _, tc := range []struct {
		name, schema, field string
		value               any
	}{
		{"candidate status", "CandidateInput", "claim_status", "verified"},
		{"candidate origin", "CandidateInput", "origin", map[string]any{"trust": "owner_authenticated"}},
		{"event origin", "EventInput", "origin", map[string]any{"trust": "owner_authenticated"}},
		{"evidence origin", "SubmittedEvidenceRef", "origin", map[string]any{"trust": "owner_authenticated"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := spec.Components.Schemas[tc.schema].Value
			valid := map[string]any{}
			switch tc.schema {
			case "CandidateInput":
				valid = map[string]any{"claim_id": "fixture", "text": "Observation.", "kind": "fact", "scope": map[string]any{"project_id": "demo"}, "evidence": []any{}, "depends_on": []any{}, "applicability": map[string]any{}}
			case "EventInput":
				valid = map[string]any{"id": "event", "idempotency_key": "key", "digest": "sha256:fixture", "scope": map[string]any{"project_id": "demo"}, "kind": "tool_result", "observed_at": "2026-10-29T12:00:00Z", "payload_artifact_id": "artifact", "sequence": float64(1), "completeness": "complete"}
			case "SubmittedEvidenceRef":
				valid = map[string]any{"artifact_id": "artifact", "digest": "sha256:fixture", "locator": "line:1", "source_revision": "revision", "applicability": map[string]any{}}
			}
			require.NoError(t, schema.VisitJSON(valid), "precondition: valid input must be accepted")
			valid[tc.field] = tc.value
			err := schema.VisitJSON(valid)
			assert.Error(t, err)
		})
	}
}

func TestContractGoldenWireVectors(t *testing.T) {
	spec, err := handler.GetSpec()
	require.NoError(t, err)
	require.NoError(t, spec.Validate(t.Context()))
	content, err := os.ReadFile("testdata/wire-v3.json")
	require.NoError(t, err)
	var vectors []struct {
		Name       string         `json:"name"`
		Schema     string         `json:"response_schema"`
		HTTPStatus int            `json:"http_status"`
		Body       map[string]any `json:"body"`
	}
	require.NoError(t, json.Unmarshal(content, &vectors))
	require.Len(t, vectors, 6)
	for _, vector := range vectors {
		t.Run(vector.Name, func(t *testing.T) {
			require.NoError(t, spec.Components.Schemas[vector.Schema].Value.VisitJSON(vector.Body))
			assert.Equal(t, "3", vector.Body["schema_version"])
			switch vector.Name {
			case "candidate", "verified":
				serialized, err := json.Marshal(vector.Body)
				require.NoError(t, err)
				var result handler.RetrievalResult
				require.NoError(t, json.Unmarshal(serialized, &result))
				require.Len(t, result.Chunks, 1)
				require.Len(t, result.Chunks[0].Evidence, 1)
				assert.Equal(t, "artifact:fixture", result.Chunks[0].Evidence[0].ArtifactId)
				assert.Equal(t, "source:fixture-revision", result.Chunks[0].Evidence[0].SourceRevision)
				assert.Equal(t, "line:1", result.Chunks[0].Evidence[0].Locator)
				if vector.Name == "candidate" {
					assert.Equal(t, handler.ClaimStatusCandidate, result.Chunks[0].ClaimStatus)
					assert.Equal(t, handler.OriginTrustSubmitted, result.Chunks[0].OriginTrust)
					assert.Nil(t, result.Chunks[0].VerificationOutcome)
				} else {
					assert.Equal(t, handler.ClaimStatusVerified, result.Chunks[0].ClaimStatus)
					require.NotNil(t, result.Chunks[0].VerificationOutcome)
					assert.Equal(t, handler.VerificationOutcomeSupported, *result.Chunks[0].VerificationOutcome)
				}
			case "no-results":
				assert.Equal(t, true, vector.Body["no_results"])
				assert.Empty(t, vector.Body["chunks"])
			default:
				assert.Equal(t, float64(vector.HTTPStatus), vector.Body["status"])
				assert.NotEmpty(t, vector.Body["code"])
				assert.NotEmpty(t, vector.Body["action"])
				assert.NotEmpty(t, vector.Body["request_id"])
				assert.NotEmpty(t, vector.Body["runbook"])
			}
		})
	}
}

func TestScopedRetrievalWireSamples(t *testing.T) {
	spec, err := handler.GetSpec()
	require.NoError(t, err)
	data, err := os.ReadFile("testdata/retrieval-v3.json")
	require.NoError(t, err)
	var vectors []struct {
		Name      string         `json:"name"`
		Schema    string         `json:"response_schema"`
		Budget    int            `json:"budget_bytes"`
		Body      map[string]any `json:"body"`
		HTTPWire  string         `json:"http_wire"`
		HTTPBytes int            `json:"http_wire_bytes"`
		CLIFrame  string         `json:"cli_frame"`
		CLIBytes  int            `json:"cli_wire_bytes"`
	}
	require.NoError(t, json.Unmarshal(data, &vectors))
	require.Len(t, vectors, 5)
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			require.NoError(t, spec.Components.Schemas[v.Schema].Value.VisitJSON(v.Body))
			assert.Equal(t, len(v.HTTPWire), v.HTTPBytes)
			assert.Equal(t, len(v.CLIFrame), v.CLIBytes)
			assert.Equal(t, fmt.Sprintf("%d\n%s", len(v.HTTPWire)-1, v.HTTPWire[:len(v.HTTPWire)-1]), v.CLIFrame)
			assert.LessOrEqual(t, v.HTTPBytes, v.Budget)
			assert.LessOrEqual(t, v.CLIBytes, v.Budget)
			assert.JSONEq(t, string(mustJSON(t, v.Body)), v.HTTPWire)
			if v.Name == "task-working-candidate" {
				assert.Contains(t, v.HTTPWire, `"claim_status":"candidate"`)
				assert.NotContains(t, v.HTTPWire, "verification_outcome")
			}
		})
	}
}
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, e := json.Marshal(v)
	require.NoError(t, e)
	return data
}

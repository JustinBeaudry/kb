package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/handler"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Match the service's real signed payload, including its original start (b).
// The gateway does not know this key and must never claim to authenticate it.
func testPassageCursor(start, offset int64) string {
	payload, _ := json.Marshal(struct {
		Kind                      string `json:"k"`
		Query                     string `json:"q"`
		Generation, Digest        string
		Auth, Knowledge, Deletion uint64
		Head                      string `json:"h,omitempty"`
		Offset                    int64  `json:"o"`
		Start                     int64  `json:"b"`
	}{"passage", "query", "generation", "digest", 1, 2, 3, "", offset, start})
	mac := hmac.New(sha256.New, []byte("test-only-key-for-signed-payloads"))
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func testPassageBody(start int64, text string, sourceBytes int64, continuation string) string {
	end := start + int64(len(text))
	c := map[string]any{
		"record_id": "a", "revision": "digest", "text": text,
		"claim_status": "candidate", "origin_trust": "submitted",
		"evidence": []any{map[string]any{"artifact_id": "a", "digest": "digest", "locator": "l", "source_revision": "g", "origin": map[string]any{"client_id": "collector", "trust": "submitted"}, "applicability": map[string]any{}}},
		"warnings": []string{"source_evidence_is_not_a_verified_claim", "partial_source_passage"},
		"passage":  map[string]any{"start_byte": start, "end_byte": end, "source_bytes": sourceBytes},
	}
	body := map[string]any{"schema_version": "3", "chunks": []any{c}, "truncated": continuation != "", "visited": 0, "no_results": false}
	if continuation != "" {
		body["continuation"] = continuation
		body["truncation_reason"] = "wire_budget"
	}
	encoded, _ := json.Marshal(body)
	return string(encoded)
}

func TestMemoryAPIFetchContinuousPages(t *testing.T) {
	for _, tc := range []struct {
		name, body, incoming string
		start                int64
		valid                bool
	}{
		{"first page begins at requested byte", testPassageBody(0, "猫", 100, testPassageCursor(0, 3)), "", 0, true},
		{"first page starts at 300", testPassageBody(300, "REMOTE-SECRET", 500, ""), "", 0, false},
		{"first page starts at most three bytes later", testPassageBody(13, "x", 100, ""), "", 10, true},
		{"first page starts four bytes later", testPassageBody(14, "REMOTE-SECRET", 100, ""), "", 10, false},
		{"next page begins at cursor offset", testPassageBody(40, "猫", 100, testPassageCursor(0, 43)), testPassageCursor(0, 40), 0, true},
		{"next page skips a byte", testPassageBody(41, "REMOTE-SECRET", 100, ""), testPassageCursor(0, 40), 0, false},
		{"next cursor skips a byte", testPassageBody(0, "REMOTE-SECRET", 100, testPassageCursor(0, 14)), "", 0, false},
		{"next cursor repeats page start", testPassageBody(0, "REMOTE-SECRET", 100, testPassageCursor(0, 0)), "", 0, false},
		{"next cursor changes original start", testPassageBody(40, "猫", 100, testPassageCursor(1, 43)), testPassageCursor(0, 40), 0, false},
		{"invalid incoming cursor", testPassageBody(0, "REMOTE-SECRET", 100, ""), "not-a-passage.cursor", 0, false},
		{"source exceeds artifact limit", testPassageBody(0, "REMOTE-SECRET", 8<<20+1, ""), "", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &apiFixture{status: http.StatusOK, body: tc.body}
			result, err := f.serve(t).Fetch(t.Context(), domain.FetchRequest{ArtifactID: "a", Locator: "l", Mode: domain.FetchPublication, BudgetBytes: 4096, StartByte: tc.start, Continuation: tc.incoming})
			require.Len(t, f.requests, 1)
			if tc.valid {
				require.NoError(t, err)
				require.Len(t, result.Chunks, 1)
			} else {
				require.ErrorIs(t, err, domain.ErrUnavailable)
				assert.Empty(t, result.Chunks)
				assert.NotContains(t, err.Error(), "REMOTE-SECRET")
			}
		})
	}
}

func TestMemoryAPIFetchDiscontinuityNeverReachesMCP(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"first page skips 300 bytes", testPassageBody(300, "REMOTE-SECRET", 500, "")},
		{"outgoing cursor skips a byte", testPassageBody(0, "REMOTE-SECRET", 100, testPassageCursor(0, 14))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &apiFixture{status: http.StatusOK, body: tc.body}
			remote := service.NewRemoteMemoryService(f.serve(t))
			var output strings.Builder
			request := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_fetch","arguments":{"artifact_id":"a","locator":"l","budget_bytes":4096}}}`
			err := handler.NewMCPServer(remote, remote, remote, remote).ServeStdio(t.Context(), strings.NewReader(request+"\n"), &output)
			require.NoError(t, err)
			require.Len(t, f.requests, 1)
			assert.NotContains(t, output.String(), "REMOTE-SECRET")
			var frame struct {
				Result struct {
					IsError bool `json:"isError"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"result"`
			}
			require.NoError(t, json.Unmarshal([]byte(output.String()), &frame))
			require.True(t, frame.Result.IsError)
			require.Len(t, frame.Result.Content, 1)
			assert.Contains(t, frame.Result.Content[0].Text, `"unavailable"`)
		})
	}
}

package handler

import (
	"context"
	"encoding/json"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

type retrievalHandlerService struct {
	result domain.RetrievalResult
	err    error
	last   domain.RetrievalRequest
}

func (f *retrievalHandlerService) Retrieve(_ context.Context, _ domain.Principal, r domain.RetrievalRequest) (domain.RetrievalResult, error) {
	f.last = r
	return f.result, f.err
}
func (f *retrievalHandlerService) Fetch(context.Context, domain.Principal, string, string, int) (domain.RetrievalResult, error) {
	return f.result, f.err
}

type memoryHandlerService struct{}

func (memoryHandlerService) Submit(_ context.Context, p domain.Principal, c domain.CandidateInput) (domain.ClaimRevision, error) {
	return domain.ClaimRevision{ClaimID: c.ClaimID, Revision: "new", Text: c.Text, Kind: c.Kind, Scope: c.Scope, Status: domain.ClaimCandidate, Evidence: []domain.EvidenceRef{{ArtifactID: "artifact", Digest: "digest", Locator: "file#bytes=0-1", SourceRevision: "generation", Origin: domain.Origin{ClientID: p.ClientID, Trust: domain.TrustSubmitted}}}}, nil
}
func (checkpointHandlerService) Get(context.Context, domain.Principal, string) (domain.Checkpoint, error) {
	return domain.Checkpoint{ID: "cp", Revision: "r", Objective: "resume"}, nil
}
func TestRetrievalHTTPWireAndProblems(t *testing.T) {
	for _, test := range []struct {
		name, path, body string
		err              error
		size, status     int
	}{{"orient", "/v1/retrieval:orient", `{"scope":{"project_id":"demo","task_id":"task"},"mode":"working","budget_bytes":4096}`, nil, 50, 200}, {"search", "/v1/retrieval:search", `{"scope":{"project_id":"demo"}}`, nil, 50, 200}, {"fetch", "/v1/evidence/a?locator=file", ``, nil, 50, 200}, {"tiny", "/v1/retrieval:orient", `{"scope":{"project_id":"demo"},"budget_bytes":1}`, nil, 50, 413}, {"escaping", "/v1/retrieval:orient", `{"scope":{"project_id":"demo"}}`, nil, 1000, 413}, {"stale", "/v1/retrieval:orient", `{"scope":{"project_id":"demo"}}`, domain.ErrStale, 0, 409}, {"unavailable", "/v1/retrieval:search", `{"scope":{"project_id":"demo"}}`, domain.ErrUnavailable, 0, 503}} {
		t.Run(test.name, func(t *testing.T) {
			f := &retrievalHandlerService{err: test.err, result: domain.RetrievalResult{SchemaVersion: "3", Chunks: []domain.EvidenceChunk{{RecordID: "c", Revision: "r", Text: strings.Repeat("<猫>\"", test.size), ClaimStatus: domain.ClaimCandidate, OriginTrust: domain.TrustSubmitted}}}}
			server, e := NewRetrievalServer(captureIdentity{}, nil, checkpointHandlerService{}, memoryHandlerService{}, f, slog.New(slog.NewJSONHandler(io.Discard, nil)))
			require.NoError(t, e)
			method := "POST"
			if test.name == "fetch" {
				method = "GET"
			}
			req := httptest.NewRequest(method, test.path, strings.NewReader(test.body))
			req.Header.Set("Authorization", "Bearer fixture")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			assert.Equal(t, test.status, rec.Code, rec.Body.String())
			if rec.Code == 200 {
				assert.LessOrEqual(t, rec.Body.Len(), 16384)
				assert.Contains(t, rec.Body.String(), `"claim_status":"candidate"`)
				assert.NotContains(t, rec.Body.String(), "verification_outcome")
			}
		})
	}
}
func TestRetrievalServiceAndHTTPAccountingAgree(t *testing.T) {
	v := domain.RetrievalResult{SchemaVersion: "3", Chunks: []domain.EvidenceChunk{{RecordID: "c", Revision: "r", Text: "<猫>\"\\", ClaimStatus: domain.ClaimVerified, OriginTrust: domain.TrustOwnerAuthenticated, Evidence: []domain.EvidenceRef{{ArtifactID: "a", Digest: "digest", Locator: "file#1-2", SourceRevision: "s", Origin: domain.Origin{ClientID: "client", Trust: domain.TrustSubmitted}}}}}, TaskCheckpoint: &domain.TaskState{Label: "task_state", Checkpoint: domain.Checkpoint{ID: "cp", Revision: "r", EvidenceReferencesOmitted: true}}}
	a, e := domain.RetrievalWire(v)
	require.NoError(t, e)
	b, e := json.Marshal(retrievalAPI(v))
	require.NoError(t, e)
	assert.JSONEq(t, string(a), string(b))
	assert.Equal(t, len(a), len(b))
}
func TestRetrievalCandidateRejectsForgedTrust(t *testing.T) {
	server, e := NewRetrievalServer(captureIdentity{}, nil, checkpointHandlerService{}, memoryHandlerService{}, &retrievalHandlerService{}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	require.NoError(t, e)
	body := `{"claim_id":"c","text":"candidate","kind":"finding","scope":{"project_id":"demo","task_id":"task"},"evidence":[],"depends_on":[],"applicability":{}}`
	for _, tc := range []struct {
		body   string
		status int
	}{{body, 201}, {strings.TrimSuffix(body, "}") + `,"claim_status":"verified"}`, 400}, {strings.Replace(body, `"evidence":[]`, `"evidence":[{"artifact_id":"a","digest":"d","locator":"l","source_revision":"s","applicability":{},"origin":{"client_id":"owner","trust":"owner_authenticated"}}]`, 1), 400}} {
		req := httptest.NewRequest("POST", "/v1/candidates", strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer fixture")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		assert.Equal(t, tc.status, rec.Code, rec.Body.String())
	}
}

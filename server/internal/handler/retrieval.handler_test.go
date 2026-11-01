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
	"time"
)

type retrievalHandlerService struct {
	result    domain.RetrievalResult
	err       error
	last      domain.RetrievalRequest
	lastFetch domain.FetchRequest
}

func (f *retrievalHandlerService) Retrieve(_ context.Context, _ domain.Principal, r domain.RetrievalRequest) (domain.RetrievalResult, error) {
	f.last = r
	return f.result, f.err
}
func (f *retrievalHandlerService) FetchEvidence(_ context.Context, _ domain.Principal, r domain.FetchRequest) (domain.RetrievalResult, error) {
	f.lastFetch = r
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
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	v.Chunks[0].Applicability = &domain.Applicability{Environment: "public-fixture", Versions: map[string]string{"runtime": "1", "unknown": ""}, ValidFrom: &now}
	v.Chunks[0].Passage = &domain.Passage{StartByte: 3, EndByte: 9, SourceBytes: 20000}
	a, e := domain.RetrievalWire(v)
	require.NoError(t, e)
	b, e := json.Marshal(retrievalAPI(v))
	require.NoError(t, e)
	assert.JSONEq(t, string(a), string(b))
	assert.Equal(t, len(a), len(b))
	assert.Contains(t, string(b), `"applicability":`)
	assert.Contains(t, string(b), `"passage":{"end_byte":9,"source_bytes":20000,"start_byte":3}`)
	_, e = wireBudget(v, len(b))
	assert.ErrorIs(t, e, domain.ErrBudget)
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

func TestFetchEvidenceHTTPMapsExplicitPassageAndWorkingScope(t *testing.T) {
	end := int64(900)
	for _, tc := range []struct {
		name, query string
		want        domain.FetchRequest
	}{
		{"id-only default", "locator=file", domain.FetchRequest{ArtifactID: "a", Locator: "file", Mode: domain.FetchPublication, BudgetBytes: 16384}},
		{"explicit working task", "locator=file&mode=working&project_id=demo&task_id=task&session_id=s&budget_bytes=2048", domain.FetchRequest{ArtifactID: "a", Locator: "file", Mode: domain.FetchWorking, Scope: domain.Scope{ProjectID: "demo", TaskID: "task", SessionID: "s"}, BudgetBytes: 2048}},
		{"span", "locator=file&start_byte=17&end_byte=900", domain.FetchRequest{ArtifactID: "a", Locator: "file", Mode: domain.FetchPublication, BudgetBytes: 16384, StartByte: 17, EndByte: &end}},
		{"continuation", "locator=file&continuation=abc.def", domain.FetchRequest{ArtifactID: "a", Locator: "file", Mode: domain.FetchPublication, BudgetBytes: 16384, Continuation: "abc.def"}},
		{"scope is passed through for service rejection", "locator=file&project_id=demo", domain.FetchRequest{ArtifactID: "a", Locator: "file", Mode: domain.FetchPublication, Scope: domain.Scope{ProjectID: "demo"}, BudgetBytes: 16384}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &retrievalHandlerService{result: domain.RetrievalResult{SchemaVersion: "3", Chunks: []domain.EvidenceChunk{{RecordID: "a", Revision: "d", Text: "x", ClaimStatus: domain.ClaimCandidate, OriginTrust: domain.TrustSubmitted, Passage: &domain.Passage{StartByte: 17, EndByte: 18, SourceBytes: 40000}}}}}
			server, e := NewRetrievalServer(captureIdentity{}, nil, checkpointHandlerService{}, memoryHandlerService{}, f, slog.New(slog.NewJSONHandler(io.Discard, nil)))
			require.NoError(t, e)
			req := httptest.NewRequest("GET", "/v1/evidence/a?"+tc.query, nil)
			req.Header.Set("Authorization", "Bearer fixture")
			rec := httptest.NewRecorder()

			server.ServeHTTP(rec, req)

			require.Equal(t, 200, rec.Code, rec.Body.String())
			assert.Equal(t, tc.want, f.lastFetch)
			assert.Contains(t, rec.Body.String(), `"passage":{"end_byte":18,"source_bytes":40000,"start_byte":17}`)
		})
	}
}
func TestFetchEvidenceHTTPRejectsUndeclaredModesAndOversizedOffsets(t *testing.T) {
	for _, query := range []string{"locator=file&mode=owner", "locator=file&start_byte=-1", "locator=file&end_byte=8388609", "locator=file&budget_bytes=16385"} {
		t.Run(query, func(t *testing.T) {
			f := &retrievalHandlerService{}
			server, e := NewRetrievalServer(captureIdentity{}, nil, checkpointHandlerService{}, memoryHandlerService{}, f, slog.New(slog.NewJSONHandler(io.Discard, nil)))
			require.NoError(t, e)
			req := httptest.NewRequest("GET", "/v1/evidence/a?"+query, nil)
			req.Header.Set("Authorization", "Bearer fixture")
			rec := httptest.NewRecorder()

			server.ServeHTTP(rec, req)

			assert.Equal(t, 400, rec.Code, rec.Body.String())
			assert.Zero(t, f.lastFetch, "rejected before the service")
		})
	}
}

type statusHandlerService struct {
	got domain.Principal
	err error
}

func (s *statusHandlerService) Status(_ context.Context, p domain.Principal) (domain.Status, error) {
	s.got = p
	return domain.Status{ConnectionState: "available", AuthorizedProjectIDs: []string{"demo"}, AuthorizedCapabilities: []string{"status:read"}, CapabilityGaps: []string{"recovery_unavailable"}}, s.err
}
func TestStatusHTTPUsesAuthenticatedStatusService(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{{"current", nil, 200}, {"revoked", domain.ErrUnauthorized, 401}, {"withdrawn", domain.ErrForbidden, 403}} {
		t.Run(tc.name, func(t *testing.T) {
			s := &statusHandlerService{err: tc.err}
			server, e := NewServer(fixtureIdentity{}, nil, nil, nil, nil, nil, s, slog.New(slog.NewJSONHandler(io.Discard, nil)))
			require.NoError(t, e)
			req := httptest.NewRequest("GET", "/v1/status", nil)
			req.Header.Set("Authorization", "Bearer fixture-secret")
			rec := httptest.NewRecorder()

			server.ServeHTTP(rec, req)

			assert.Equal(t, tc.status, rec.Code, rec.Body.String())
			assert.Equal(t, "client", s.got.ClientID, "service receives the authenticated principal")
			if tc.status == 200 {
				assert.JSONEq(t, `{"schema_version":"3","connection_state":"available","authorized_project_ids":["demo"],"authorized_capabilities":["status:read"],"capability_gaps":["recovery_unavailable"]}`, rec.Body.String())
			}
		})
	}
}

package handler

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type accessRecorderFake struct {
	mu   sync.Mutex
	seen [][]domain.AccessObservation
}

func (f *accessRecorderFake) Record(o []domain.AccessObservation) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, o)
}

func TestAccessAuditRecordsEachAuthenticatedRequestOnce(t *testing.T) {
	rec := &accessRecorderFake{}
	e, err := configuredServer(fixtureIdentity{}, nil, nil, nil, nil, nil, nil, nil, slog.New(slog.NewJSONHandler(io.Discard, nil)), WithAccessAudit(rec))
	require.NoError(t, err)
	for _, tc := range []struct{ method, path, token, body string }{
		{"GET", "/v1/status", "fixture-secret", ""},
		{"POST", "/v1/clients", "fixture-secret", `{"id":"other","role":"agent","project_ids":["demo"],"capabilities":["status:read"]}`},
		{"GET", "/v1/status", "wrong-secret", ""},
		{"GET", "/v1/status", "", ""},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		req.Header.Set("Content-Type", "application/json")
		e.ServeHTTP(httptest.NewRecorder(), req)
	}
	assert.Equal(t, [][]domain.AccessObservation{
		{{ClientID: "client", Capability: "status:read", Allowed: true}},
		{{ClientID: "client", Capability: "clients:manage", Allowed: false}},
		// The fixture identity records nothing for a wrong bearer; the
		// identity service attributes those (see the service tests). A
		// request with no bearer at all is counted as unknown here.
		{{ClientID: domain.AccessOutsideGrant, Capability: domain.AccessAuthUnknown, Allowed: false}},
	}, rec.seen)
}

func TestAccessAuditIsOffWithoutRecorder(t *testing.T) {
	e, err := NewIdentityServer(fixtureIdentity{}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer fixture-secret")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.NotEqual(t, http.StatusUnauthorized, rec.Code)
}

// ownershipRefusal passes the project check, then refuses the request the way
// a task owned by another client is refused.
type ownershipRefusal struct{}

func (ownershipRefusal) Put(ctx context.Context, p domain.Principal, _ string, c domain.Checkpoint) (domain.Checkpoint, error) {
	domain.ObserveAccess(ctx, p, "checkpoint:write", []string{c.Scope.ProjectID}, true)
	return domain.Checkpoint{}, domain.ErrForbidden
}

func TestAccessAuditRecordsOwnershipRefusalAsDenied(t *testing.T) {
	rec := &accessRecorderFake{}
	e, err := configuredServer(fixtureIdentity{}, nil, ownershipRefusal{}, nil, nil, nil, nil, nil, slog.New(slog.NewJSONHandler(io.Discard, nil)), WithAccessAudit(rec))
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPut, "/v1/checkpoints/c", strings.NewReader(`{"scope":{"project_id":"demo","task_id":"task"},"objective":"test","completed":[],"pending":[],"questions":[],"evidence_revision_ids":[]}`))
	req.Header.Set("Authorization", "Bearer fixture-secret")
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	e.ServeHTTP(res, req)
	require.Equal(t, http.StatusForbidden, res.Code)
	require.Len(t, rec.seen, 1)
	require.NotEmpty(t, rec.seen[0])
	for _, o := range rec.seen[0] {
		assert.False(t, o.Allowed, "a refused request must not count as an allowed access: %+v", o)
	}
}

// refusingRetrieval observes an allowed project check, then refuses.
type refusingRetrieval struct{ *mcpBackend }

func (r refusingRetrieval) Retrieve(ctx context.Context, q domain.RetrievalRequest) (domain.RetrievalResult, error) {
	domain.ObserveAccess(ctx, domain.Principal{ClientID: "agent", ProjectIDs: []string{"demo"}}, "retrieval:read", []string{"demo"}, true)
	return domain.RetrievalResult{}, domain.ErrForbidden
}

func TestMCPToolRefusalIsRecordedAsDenied(t *testing.T) {
	b := &mcpBackend{}
	s := newMCPServer(refusingRetrieval{b}, b, b, b)
	c := domain.NewAccessCollector()
	ctx := domain.WithAccessCollector(context.Background(), c)
	frame, ok := s.handleOne(ctx, []byte(call(1, "memory_search", `{"project_id":"demo"}`)))
	require.True(t, ok)
	assert.Contains(t, string(frame), `\"code\":\"forbidden\"`)
	assert.Equal(t, []domain.AccessObservation{{ClientID: "agent", ProjectID: "demo", Capability: "retrieval:read", Allowed: false}}, c.Observations())
}

func TestAccessAuditRecordsAnOAuthTokenUsedOffMCP(t *testing.T) {
	rec := &accessRecorderFake{}
	e, err := configuredServer(resourceBoundIdentity{}, nil, nil, nil, nil, nil, nil, nil, slog.New(slog.NewJSONHandler(io.Discard, nil)), WithAccessAudit(rec))
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodGet, "/v1/exports", nil)
	req.Header.Set("Authorization", "Bearer "+oauthAccessBearer)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, [][]domain.AccessObservation{{{ClientID: "oauth-G1", Capability: domain.AccessAuthResource, Allowed: false}}}, rec.seen, "the grant's misuse is attributed to its client")
}

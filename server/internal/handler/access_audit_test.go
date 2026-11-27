package handler

import (
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
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+tc.token)
		req.Header.Set("Content-Type", "application/json")
		e.ServeHTTP(httptest.NewRecorder(), req)
	}
	assert.Equal(t, [][]domain.AccessObservation{
		{{ClientID: "client", Capability: "status:read", Allowed: true}},
		{{ClientID: "client", Capability: "clients:manage", Allowed: false}},
	}, rec.seen, "an unauthenticated request has no client to attribute, so it records nothing")
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

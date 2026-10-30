package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type policyHandlerService struct{ err error }

func (f policyHandlerService) Get(_ context.Context, _ domain.Principal, project string) (domain.Policy, error) {
	if f.err != nil {
		return domain.Policy{}, f.err
	}
	return domain.Policy{Revision: "p-" + project, ExcludedPaths: []string{"private"}, SpoolLimitBytes: 1024}, nil
}

type deletingCaptureService struct {
	captureHandlerService
	err error
}

func (f deletingCaptureService) Complete(context.Context, domain.Principal, string, string) (domain.Receipt, error) {
	return domain.Receipt{}, f.err
}

func TestGetPolicyHTTP(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{{"read", nil, 200, ""}, {"forbidden", domain.ErrForbidden, 403, "forbidden"}, {"revoked", domain.ErrUnauthorized, 401, "unauthorized"}} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := NewPolicyServer(captureIdentity{}, captureHandlerService{}, checkpointHandlerService{}, nil, nil, policyHandlerService{err: tc.err}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
			require.NoError(t, err)
			req := httptest.NewRequest("GET", "/v1/policies/demo", nil)
			req.Header.Set("Authorization", "Bearer fixture")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			assert.Equal(t, tc.status, rec.Code, rec.Body.String())
			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			if tc.status == 200 {
				policy, ok := body["policy"].(map[string]any)
				require.True(t, ok, rec.Body.String())
				assert.Equal(t, "p-demo", policy["revision"])
				assert.Equal(t, float64(1024), policy["spool_limit_bytes"])
				return
			}
			assert.Equal(t, tc.code, body["code"])
		})
	}
}

func TestPolicyRouteAbsentWithoutPolicyService(t *testing.T) {
	e, err := NewMemoryServer(captureIdentity{}, captureHandlerService{}, checkpointHandlerService{}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	require.NoError(t, err)
	req := httptest.NewRequest("GET", "/v1/policies/demo", nil)
	req.Header.Set("Authorization", "Bearer fixture")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.NotEqual(t, 200, rec.Code)
}

func TestCaptureProblemCodesDistinguishPermanentFailures(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{{domain.ErrDeleted, 410, "deleted"}, {domain.ErrConflict, 409, "conflict"}, {domain.ErrStale, 409, "stale"}, {domain.ErrIncomplete, 503, "incomplete"}} {
		t.Run(tc.code, func(t *testing.T) {
			e, err := NewMemoryServer(captureIdentity{}, deletingCaptureService{err: tc.err}, checkpointHandlerService{}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
			require.NoError(t, err)
			req := httptest.NewRequest("POST", "/v1/artifacts/a:complete", strings.NewReader(`{"digest":"digest"}`))
			req.Header.Set("Authorization", "Bearer fixture")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			assert.Equal(t, tc.status, rec.Code, rec.Body.String())
			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, tc.code, body["code"])
		})
	}
}

package handler

import (
	"context"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

type captureIdentity struct{ fixtureIdentity }

func (captureIdentity) Authenticate(context.Context, string) (domain.Principal, error) {
	return domain.Principal{ClientID: "client", ProjectIDs: []string{"demo"}, Capabilities: []string{"artifacts:write", "events:append", "checkpoint:write"}}, nil
}

type captureHandlerService struct{}

func (captureHandlerService) Stage(_ context.Context, _ domain.Principal, m service.ArtifactInput, r io.Reader) (domain.Artifact, error) {
	b, e := io.ReadAll(r)
	if e != nil || string(b) != "hello" {
		return domain.Artifact{}, domain.ErrInvalidInput
	}
	return domain.Artifact{ID: "a", Scope: m.Scope, Digest: m.Digest, Bytes: m.Bytes, PolicyRevision: m.PolicyRevision, State: domain.ArtifactStaged, SourceLocator: m.SourceLocator}, nil
}
func (captureHandlerService) Complete(context.Context, domain.Principal, string, string) (domain.Receipt, error) {
	return domain.Receipt{ID: "a", State: "ready"}, nil
}
func (captureHandlerService) Append(context.Context, domain.Principal, []domain.Event) ([]domain.Receipt, error) {
	return []domain.Receipt{{ID: "e", State: "submitted"}}, nil
}

type checkpointHandlerService struct{}

func (checkpointHandlerService) Put(_ context.Context, _ domain.Principal, _ string, c domain.Checkpoint) (domain.Checkpoint, error) {
	c.Revision = "r"
	return c, nil
}
func TestCaptureHTTPRoutes(t *testing.T) {
	e, err := NewMemoryServer(captureIdentity{}, captureHandlerService{}, checkpointHandlerService{}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	require.NoError(t, err)
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{{"POST", "/v1/artifacts", `{"scope":{"project_id":"demo"},"digest":"digest","bytes":5,"policy_revision":"p","content_base64":"aGVsbG8=","source_locator":"file#0-5"}`, 201}, {"POST", "/v1/artifacts/a:complete", `{"digest":"digest"}`, 200}, {"POST", "/v1/events:append", `{"events":[{"id":"e","idempotency_key":"k","digest":"d","scope":{"project_id":"demo"},"kind":"message","observed_at":"2026-10-29T00:00:00Z","payload_artifact_id":"a","sequence":0,"completeness":"complete"}]}`, 202}, {"PUT", "/v1/checkpoints/c", `{"scope":{"project_id":"demo","task_id":"task"},"objective":"test","completed":[],"pending":[],"questions":[],"evidence_revision_ids":[]}`, 200}, {"GET", "/v1/checkpoints/c", "", 503}} {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer fixture")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			assert.Equal(t, tc.status, rec.Code, rec.Body.String())
		})
	}
}
func TestCaptureRouteBodyLimits(t *testing.T) {
	e, err := NewMemoryServer(captureIdentity{}, captureHandlerService{}, checkpointHandlerService{}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	require.NoError(t, err)
	for _, tc := range []struct {
		path, body string
		status     int
	}{{"/v1/events:append", strings.Repeat("x", 65537), 413}, {"/v1/artifacts", `{"scope":{"project_id":"demo"},"digest":"d","bytes":65538,"policy_revision":"p","source_locator":"file","content_base64":"` + strings.Repeat("eHh4", 21846) + `"}`, 400}, {"/v1/artifacts", strings.Repeat("x", 11206657), 413}} {
		req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer fixture")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		assert.Equal(t, tc.status, rec.Code, rec.Body.String())
	}
}

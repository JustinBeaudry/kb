package repository_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/handler"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/require"
)

// Exercise the compiled acceptance fixture's first candidate through actual
// HTTP decoding, service orchestration, graph persistence, and evidence I/O.
func TestFirstCandidateHTTPMatchesCompiledFixture(t *testing.T) {
	store, jobs, _ := jobsGraph(t)
	objects := captureS3(t)
	warmObjects(t, objects)
	owners := repository.NewIdentityRepository(store)
	identity := service.NewIdentityService(store, owners, gateway.NostrIdentity{})
	agent, err := identity.Setup(t.Context(), domain.OwnerIdentity{ID: "owner", PublicKey: "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"}, service.EnrollInput{ClientID: "agent", Role: domain.RoleAgent, ProjectIDs: []string{"fixture-project"}, Capabilities: []string{"status:read", "retrieval:read", "evidence:read", "candidate:write", "checkpoint:write"}})
	require.NoError(t, err)
	collector, err := identity.EnrollHost(t.Context(), service.EnrollInput{ClientID: "collector", Role: domain.RoleCollector, ProjectIDs: []string{"fixture-project"}, Capabilities: []string{"artifacts:write"}})
	require.NoError(t, err)
	lifecycle := service.NewLifecycleService(store, repository.NewLifecycleRepository(store), owners, jobs, objects, nil, nil)
	_, err = lifecycle.UpdatePolicyHost(t.Context(), "fixture-project", "", domain.Policy{Revision: "fixture-policy-1", RawHistoryRetention: 90 * 24 * time.Hour, InactiveTaskRetention: 30 * 24 * time.Hour, BackupRetention: 30 * 24 * time.Hour, SpoolLimitBytes: 10485760})
	require.NoError(t, err)
	evidence := repository.NewEvidenceRepository(store)
	auth := repository.NewRetrievalRepository(store)
	checkpoints := repository.NewCheckpointRepository(store)
	capture := service.NewCaptureService(store, evidence, jobs, objects)
	memory := service.NewMemoryService(store, repository.NewMemoryRepository(store), auth, evidence, capture)
	retrieval, err := service.NewRetrievalService(store, auth, checkpoints, evidence, objects, bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	router, err := handler.NewServer(identity, capture, service.NewCheckpointService(store, checkpoints, auth), memory, retrieval, service.NewPolicyService(store, evidence, auth), service.NewStatusService(store, auth), slog.New(slog.NewJSONHandler(io.Discard, nil)))
	require.NoError(t, err)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	request := func(token, method, path string, input any, want int) []byte {
		t.Helper()
		var body io.Reader
		if input != nil {
			raw, e := json.Marshal(input)
			require.NoError(t, e)
			body = bytes.NewReader(raw)
		}
		req, e := http.NewRequestWithContext(t.Context(), method, server.URL+path, body)
		require.NoError(t, e)
		req.Header.Set("Authorization", "Bearer "+token)
		if input != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, e := server.Client().Do(req)
		require.NoError(t, e)
		raw, e := io.ReadAll(resp.Body)
		require.NoError(t, e)
		require.NoError(t, resp.Body.Close())
		var problem struct {
			Code string `json:"code"`
		}
		if resp.StatusCode != want {
			_ = json.Unmarshal(raw, &problem)
		}
		require.Equal(t, want, resp.StatusCode, "method=%s problem_code=%s", method, problem.Code)
		return raw
	}
	text := "Synthetic finding to remove."
	digest := service.TokenDigest(text)
	task, session, path := "task-remove", "session-remove", "fixture.txt"
	scope := handler.Scope{ProjectId: "fixture-project", TaskId: &task, SessionId: &session}
	raw := request(collector.Bearer, http.MethodPost, "/v1/artifacts", handler.ArtifactInput{Bytes: int64(len(text)), ContentBase64: []byte(text), Digest: digest, PolicyRevision: "fixture-policy-1", Scope: scope, SourceLocator: "remove/source", SourcePath: &path}, http.StatusCreated)
	var artifact handler.ArtifactResult
	require.NoError(t, json.Unmarshal(raw, &artifact))
	request(collector.Bearer, http.MethodPost, "/v1/artifacts/"+artifact.Artifact.Id+":complete", handler.CompleteArtifactInput{Digest: digest}, http.StatusOK)
	request(agent.Bearer, http.MethodGet, "/v1/status", nil, http.StatusOK)
	request(agent.Bearer, http.MethodPost, "/v1/candidates", handler.CandidateInput{ClaimId: "candidate-remove", Scope: scope, Text: text, Kind: "finding", DependsOn: []string{}, Evidence: []handler.SubmittedEvidenceRef{{ArtifactId: artifact.Artifact.Id, Digest: digest, Locator: "remove/source", SourceRevision: "source-v1"}}}, http.StatusCreated)
}

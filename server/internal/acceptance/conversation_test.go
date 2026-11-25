package acceptance

// Hermes conversation contract (I4) over the real HTTP API, graph and object
// store with synthetic data. The request bodies are the exact schema-v3 shapes
// the Hermes provider sends (integrations/hermes/src/kb_memory/gateway.py),
// using an agent credential that can only retrieve and write candidates.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/handler"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	conversationS3Image   = "chrislusf/seaweedfs:4.51@sha256:f7a49e3f3972ebed5be2ed4befa4a0c68eb7bdddba6e692d42729ceb545134b8"
	conversationProject   = "conversation-project"
	conversationForbidden = "restricted-project"
	conversationVerified  = "rev-conversation-verified"
	conversationFact      = "The synthetic billing service retries webhooks three times."
)

func conversationContainer(t *testing.T, req testcontainers.ContainerRequest) (string, string) {
	t.Helper()
	c, err := testcontainers.GenericContainer(t.Context(), testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true})
	if c != nil {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			require.NoError(t, c.Terminate(ctx))
		})
	}
	require.NoError(t, err, "real fixture containers are required; this contract never skips")
	host, err := c.Host(t.Context())
	require.NoError(t, err)
	port, err := c.MappedPort(t.Context(), req.ExposedPorts[0])
	require.NoError(t, err)
	return host, port.Port()
}

func conversationGraph(t *testing.T) *graph.Store {
	t.Helper()
	data, err := os.ReadFile("../../../deploy/versions.json")
	require.NoError(t, err)
	var pin struct {
		Graph struct {
			Image string `json:"image"`
		} `json:"graph"`
	}
	require.NoError(t, json.Unmarshal(data, &pin))
	const secret = "conversation-graph-fixture-only"
	host, port := conversationContainer(t, testcontainers.ContainerRequest{Image: pin.Graph.Image, ExposedPorts: []string{"7687/tcp"}, Env: map[string]string{"NEO4J_AUTH": "neo4j/" + secret, "NEO4J_server_memory_heap_initial__size": "256m", "NEO4J_server_memory_heap_max__size": "512m", "NEO4J_server_memory_pagecache_size": "128m"}, WaitingFor: wait.ForLog("Started.").WithStartupTimeout(2 * time.Minute)})
	store, err := graph.Open(t.Context(), graph.Config{URI: "bolt://" + host + ":" + port, Database: "neo4j", User: "neo4j", Secret: secret})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close(context.Background())) })
	return store
}

func conversationObjects(t *testing.T) *gateway.S3 {
	t.Helper()
	host, port := conversationContainer(t, testcontainers.ContainerRequest{Image: conversationS3Image, ExposedPorts: []string{"8333/tcp"}, Cmd: []string{"mini", "-dir=/data", "-ip.bind=0.0.0.0", "-master.telemetry=false"}, Env: map[string]string{"AWS_ACCESS_KEY_ID": "fixture", "AWS_SECRET_ACCESS_KEY": "fixture-secret-only", "S3_BUCKET": "evidence"}, WaitingFor: wait.ForListeningPort("8333/tcp").WithStartupTimeout(90 * time.Second)})
	objects, err := gateway.NewS3(gateway.S3Config{Endpoint: "http://" + host + ":" + port, Region: "local", Bucket: "evidence", AccessKey: "fixture", SecretKey: "fixture-secret-only"})
	require.NoError(t, err)
	// Fixture readiness only: a listening port does not mean a volume exists.
	deadline := time.Now().Add(90 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		key := "conversation-fixture-ready-" + rand.Text()
		err = objects.Put(ctx, key, strings.NewReader("synthetic"), service.TokenDigest("synthetic"))
		if err == nil {
			err = objects.Delete(ctx, key)
		}
		cancel()
		if err == nil {
			return objects
		}
		require.True(t, time.Now().Before(deadline), "object store fixture never became ready: %v", err)
		time.Sleep(500 * time.Millisecond)
	}
}

func conversationMutation(t *testing.T, store *graph.Store, query string, params map[string]any) {
	t.Helper()
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		native, err := store.Unwrap(tx)
		if err != nil {
			return err
		}
		result, err := native.Run(ctx, query, params)
		if err != nil {
			return err
		}
		_, err = result.Consume(ctx)
		return err
	}))
}

// conversationPublish stands in for an owner-adjudicated publication (I1): the
// Hermes credential has no route that could produce it.
func conversationPublish(t *testing.T, store *graph.Store, c domain.ClaimRevision) {
	t.Helper()
	data, err := json.Marshal(c)
	require.NoError(t, err)
	conversationMutation(t, store, `CREATE (c:ClaimRevision {id:$id,claimID:$claim,data:$data,status:'verified',valid:true,projectID:$project,taskID:'',clientID:'fixture',text:$text,kind:$kind}) CREATE (:Publication {id:'pub-'+$id,claimRevision:$id,revision:'fixture-publication',policyRevision:'conversation-policy-1',valid:true,sharingScope:'project',supportRevisions:[],generalizedText:''}) WITH c MATCH (o:OwnerState {id:'owner'}) SET o.revisionEpoch=o.revisionEpoch+1`, map[string]any{"id": c.Revision, "claim": c.ClaimID, "data": string(data), "project": c.Scope.ProjectID, "text": c.Text, "kind": c.Kind})
	for _, ref := range c.Evidence {
		conversationMutation(t, store, `MATCH (c:ClaimRevision {id:$id}),(a:Artifact {id:$artifact}) CREATE (c)-[:EVIDENCE {digest:$digest,locator:$locator}]->(a)`, map[string]any{"id": c.Revision, "artifact": ref.ArtifactID, "digest": ref.Digest, "locator": ref.Locator})
	}
}

type conversationChunk struct {
	RecordID            string   `json:"record_id"`
	Revision            string   `json:"revision"`
	Text                string   `json:"text"`
	ClaimStatus         string   `json:"claim_status"`
	OriginTrust         string   `json:"origin_trust"`
	VerificationOutcome *string  `json:"verification_outcome"`
	Warnings            []string `json:"warnings"`
	Evidence            []struct {
		ArtifactID     string `json:"artifact_id"`
		Digest         string `json:"digest"`
		Locator        string `json:"locator"`
		SourceRevision string `json:"source_revision"`
	} `json:"evidence"`
}

type conversationEnvelope struct {
	SchemaVersion string              `json:"schema_version"`
	Chunks        []conversationChunk `json:"chunks"`
	NoResults     bool                `json:"no_results"`
}

func revisionsOf(e conversationEnvelope) []string {
	out := []string{}
	for _, c := range e.Chunks {
		out = append(out, c.Revision)
	}
	return out
}

func TestHermesConversationContract(t *testing.T) {
	store := conversationGraph(t)
	objects := conversationObjects(t)
	jobs := repository.NewJobsRepository(store)
	owners := repository.NewIdentityRepository(store)
	identity := service.NewIdentityService(store, owners, gateway.NostrIdentity{})
	hermesCapabilities := []string{"status:read", "retrieval:read", "evidence:read", "candidate:write"}
	hermes, err := identity.Setup(t.Context(), domain.OwnerIdentity{ID: "owner", PublicKey: "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"}, service.EnrollInput{ClientID: "hermes", Role: domain.RoleAgent, ProjectIDs: []string{conversationProject}, Capabilities: hermesCapabilities})
	require.NoError(t, err)
	collector, err := identity.EnrollHost(t.Context(), service.EnrollInput{ClientID: "collector", Role: domain.RoleCollector, ProjectIDs: []string{conversationProject, conversationForbidden}, Capabilities: []string{"artifacts:write"}})
	require.NoError(t, err)
	lifecycle := service.NewLifecycleService(store, repository.NewLifecycleRepository(store), owners, jobs, objects, nil, nil)
	for _, project := range []string{conversationProject, conversationForbidden} {
		_, err = lifecycle.UpdatePolicyHost(t.Context(), project, "", domain.Policy{Revision: "conversation-policy-1", RawHistoryRetention: 90 * 24 * time.Hour, InactiveTaskRetention: 30 * 24 * time.Hour, BackupRetention: 30 * 24 * time.Hour, SpoolLimitBytes: 10485760})
		require.NoError(t, err)
	}
	evidence := repository.NewEvidenceRepository(store)
	auth := repository.NewRetrievalRepository(store)
	checkpoints := repository.NewCheckpointRepository(store)
	capture := service.NewCaptureService(store, evidence, jobs, objects)
	memory := service.NewMemoryService(store, repository.NewMemoryRepository(store), auth, evidence, capture)
	retrieval, err := service.NewRetrievalService(store, auth, checkpoints, evidence, objects, bytes.Repeat([]byte{9}, 32))
	require.NoError(t, err)
	router, err := handler.NewServer(identity, capture, service.NewCheckpointService(store, checkpoints, auth), memory, retrieval, service.NewPolicyService(store, evidence, auth), service.NewStatusService(store, auth), nil, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	require.NoError(t, err)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	call := func(t *testing.T, token, method, path string, input any) (int, []byte) {
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
		return resp.StatusCode, raw
	}
	upload := func(project, text, locator string) handler.Artifact {
		t.Helper()
		digest := service.TokenDigest(text)
		status, raw := call(t, collector.Bearer, http.MethodPost, "/v1/artifacts", handler.ArtifactInput{Bytes: int64(len(text)), ContentBase64: []byte(text), Digest: digest, PolicyRevision: "conversation-policy-1", Scope: handler.Scope{ProjectId: project}, SourceLocator: locator})
		require.Equal(t, http.StatusCreated, status)
		var staged handler.ArtifactResult
		require.NoError(t, json.Unmarshal(raw, &staged))
		status, _ = call(t, collector.Bearer, http.MethodPost, "/v1/artifacts/"+staged.Artifact.Id+":complete", handler.CompleteArtifactInput{Digest: digest})
		require.Equal(t, http.StatusOK, status)
		return staged.Artifact
	}
	source := upload(conversationProject, "billing.retries=3 (synthetic configuration)", "billing/config.txt#bytes=0-44")
	ref := domain.EvidenceRef{ArtifactID: source.Id, Digest: source.Digest, Locator: source.SourceLocator, SourceRevision: "source-v1", Origin: domain.Origin{ClientID: "collector", Trust: domain.TrustSubmitted}}
	conversationPublish(t, store, domain.ClaimRevision{ClaimID: "claim-webhook-retries", Revision: conversationVerified, Text: conversationFact, Kind: "finding", Scope: domain.Scope{ProjectID: conversationProject}, Status: domain.ClaimVerified, Evidence: []domain.EvidenceRef{ref}})
	secret := upload(conversationForbidden, "restricted synthetic source", "restricted/source.txt#bytes=0-27")
	secretRef := domain.EvidenceRef{ArtifactID: secret.Id, Digest: secret.Digest, Locator: secret.SourceLocator, SourceRevision: "source-r1", Origin: domain.Origin{ClientID: "collector", Trust: domain.TrustSubmitted}}
	conversationPublish(t, store, domain.ClaimRevision{ClaimID: "claim-restricted", Revision: "rev-restricted", Text: "Restricted synthetic finding.", Kind: "finding", Scope: domain.Scope{ProjectID: conversationForbidden}, Status: domain.ClaimVerified, Evidence: []domain.EvidenceRef{secretRef}})

	session := "session-1"
	retrieve := func(t *testing.T, path, query string, budget int) conversationEnvelope {
		t.Helper()
		status, raw := call(t, hermes.Bearer, http.MethodPost, path, map[string]any{"scope": map[string]string{"project_id": conversationProject, "session_id": session}, "budget_bytes": budget, "mode": map[string]string{"/v1/retrieval:orient": "orientation", "/v1/retrieval:search": "evidence"}[path], "query": query})
		require.Equal(t, http.StatusOK, status, string(raw))
		require.LessOrEqual(t, len(raw), budget)
		var envelope conversationEnvelope
		require.NoError(t, json.Unmarshal(raw, &envelope))
		return envelope
	}
	taskScope := map[string]string{"project_id": conversationProject, "task_id": "hermes-" + session, "session_id": session}

	t.Run("answers preserve evidence references", func(t *testing.T) {
		for _, path := range []string{"/v1/retrieval:orient", "/v1/retrieval:search"} {
			t.Run(path, func(t *testing.T) {
				envelope := retrieve(t, path, "webhooks", 4096)

				require.Len(t, envelope.Chunks, 1)
				chunk := envelope.Chunks[0]
				assert.Equal(t, "3", envelope.SchemaVersion)
				assert.Equal(t, conversationVerified, chunk.Revision)
				assert.Equal(t, "verified", chunk.ClaimStatus)
				require.Len(t, chunk.Evidence, 1)
				assert.Equal(t, source.Id, chunk.Evidence[0].ArtifactID)
				assert.Equal(t, source.Digest, chunk.Evidence[0].Digest)
				assert.Equal(t, "source-v1", chunk.Evidence[0].SourceRevision)
			})
		}
	})

	t.Run("assistant echo is not independent evidence", func(t *testing.T) {
		cases := []struct {
			name     string
			evidence []map[string]any
			want     int
		}{
			{"text-only echo stays a task candidate", []map[string]any{}, http.StatusCreated},
			{"echo cannot re-cite published evidence as new task evidence", []map[string]any{{"artifact_id": source.Id, "digest": source.Digest, "locator": source.SourceLocator, "source_revision": "source-v1", "applicability": map[string]any{}}}, http.StatusForbidden},
		}
		for i, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				status, raw := call(t, hermes.Bearer, http.MethodPost, "/v1/candidates", map[string]any{"claim_id": "hermes-echo-" + string(rune('a'+i)), "text": conversationFact, "kind": "finding", "scope": taskScope, "evidence": tc.evidence, "depends_on": []string{conversationVerified}, "applicability": map[string]any{}})

				require.Equal(t, tc.want, status, string(raw))
				if status == http.StatusCreated {
					var created struct {
						Claim struct {
							ClaimStatus string `json:"claim_status"`
							OriginTrust string `json:"origin_trust"`
						} `json:"claim"`
					}
					require.NoError(t, json.Unmarshal(raw, &created))
					assert.Equal(t, "candidate", created.Claim.ClaimStatus)
					assert.Equal(t, "submitted", created.Claim.OriginTrust)
				}
				assert.Equal(t, []string{conversationVerified}, revisionsOf(retrieve(t, "/v1/retrieval:search", "webhooks", 16384)))
			})
		}
	})

	t.Run("builtin note cannot override graph", func(t *testing.T) {
		status, raw := call(t, hermes.Bearer, http.MethodPost, "/v1/candidates", map[string]any{"claim_id": "hermes-note-correction", "text": "The synthetic billing service retries webhooks five times.", "kind": "correction", "scope": taskScope, "evidence": []any{}, "depends_on": []string{}, "applicability": map[string]any{}})
		require.Equal(t, http.StatusCreated, status, string(raw))

		envelope := retrieve(t, "/v1/retrieval:orient", "webhooks", 4096)

		require.Len(t, envelope.Chunks, 1)
		assert.Equal(t, conversationVerified, envelope.Chunks[0].Revision)
		assert.Equal(t, "verified", envelope.Chunks[0].ClaimStatus)
		assert.Equal(t, conversationFact, envelope.Chunks[0].Text)
	})

	t.Run("forbidden source is not disclosed", func(t *testing.T) {
		cases := []struct{ name, method, path string }{
			{"orient restricted project", http.MethodPost, "/v1/retrieval:orient"},
			{"search restricted project", http.MethodPost, "/v1/retrieval:search"},
			{"fetch restricted evidence", http.MethodGet, "/v1/evidence/" + url.PathEscape(secret.Id) + "?" + url.Values{"locator": {secret.SourceLocator}, "budget_bytes": {"16384"}}.Encode()},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				var input any
				if tc.method == http.MethodPost {
					input = map[string]any{"scope": map[string]string{"project_id": conversationForbidden}, "budget_bytes": 4096, "query": "Restricted"}
				}

				status, raw := call(t, hermes.Bearer, tc.method, tc.path, input)

				assert.Equal(t, http.StatusForbidden, status)
				assert.NotContains(t, string(raw), "Restricted synthetic finding")
				assert.NotContains(t, string(raw), "restricted synthetic source")
			})
		}
	})

	t.Run("background prefetch respects budget", func(t *testing.T) {
		for _, budget := range []int{256, 1024, 4096} {
			t.Run(fmt.Sprintf("budget=%d", budget), func(t *testing.T) {
				status, raw := call(t, hermes.Bearer, http.MethodPost, "/v1/retrieval:orient", map[string]any{"scope": map[string]string{"project_id": conversationProject, "session_id": session}, "budget_bytes": budget, "mode": "orientation", "query": "webhooks"})

				if status == http.StatusOK {
					assert.LessOrEqual(t, len(raw), budget)
					return
				}
				var problem struct {
					Code string `json:"code"`
				}
				require.NoError(t, json.Unmarshal(raw, &problem))
				assert.Equal(t, http.StatusRequestEntityTooLarge, status)
				assert.Equal(t, "budget", problem.Code)
			})
		}
	})

	t.Run("hermes credential has no promotion capability", func(t *testing.T) {
		status, raw := call(t, hermes.Bearer, http.MethodGet, "/v1/status", nil)
		require.Equal(t, http.StatusOK, status)
		var got struct {
			Capabilities []string `json:"authorized_capabilities"`
		}
		require.NoError(t, json.Unmarshal(raw, &got))

		assert.ElementsMatch(t, hermesCapabilities, got.Capabilities)
		_, err := identity.Enroll(t.Context(), domain.Principal{OwnerID: "owner", Role: domain.RoleOwner}, service.EnrollInput{ClientID: "hermes-promoter", Role: domain.RoleAgent, ProjectIDs: []string{conversationProject}, Capabilities: []string{"candidate:write", "verification:write"}})
		assert.ErrorIs(t, err, domain.ErrInvalidInput)
	})
}

package main

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/platform"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
)

func TestWorkerCompositionFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		handlers map[string]platform.RunJob
		want     error
	}{
		{"no feature handlers", nil, domain.ErrUnavailable},
		{"nil implementation", map[string]platform.RunJob{"verify": nil}, domain.ErrInvalidInput},
		{"empty kind", map[string]platform.RunJob{"": func(context.Context, domain.Job, domain.Lease) error { return nil }}, domain.ErrInvalidInput},
		{"missing graph config", map[string]platform.RunJob{"verify": func(context.Context, domain.Job, domain.Lease) error { return nil }}, domain.ErrInvalidInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KB_GRAPH_URI", "")
			t.Setenv("KB_GRAPH_USER", "")
			t.Setenv("KB_GRAPH_SECRET", "")
			err := run(t.Context(), slog.New(slog.NewJSONHandler(io.Discard, nil)), tc.handlers)
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

func TestWorkerCompositionGracefulStop(t *testing.T) {
	maintenanceFixture(t, true)
	versions, err := os.ReadFile("../../../deploy/versions.json")
	require.NoError(t, err)
	var pin struct {
		Graph struct {
			Image string `json:"image"`
		} `json:"graph"`
	}
	require.NoError(t, json.Unmarshal(versions, &pin))
	const secret = "worker-composition-fixture-only"
	container, err := testcontainers.GenericContainer(t.Context(), testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: pin.Graph.Image, ExposedPorts: []string{"7687/tcp"}, Env: map[string]string{"NEO4J_AUTH": "neo4j/" + secret, "NEO4J_server_memory_heap_initial__size": "256m", "NEO4J_server_memory_heap_max__size": "512m", "NEO4J_server_memory_pagecache_size": "128m"}, WaitingFor: wait.ForLog("Started.").WithStartupTimeout(2 * time.Minute)}, Started: true})
	if container != nil {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			require.NoError(t, container.Terminate(ctx))
		})
	}
	require.NoError(t, err)
	host, err := container.Host(t.Context())
	require.NoError(t, err)
	port, err := container.MappedPort(t.Context(), "7687/tcp")
	require.NoError(t, err)
	t.Setenv("KB_GRAPH_URI", "bolt://"+host+":"+port.Port())
	t.Setenv("KB_GRAPH_USER", "neo4j")
	t.Setenv("KB_GRAPH_SECRET", secret)
	t.Setenv("KB_GRAPH_DATABASE", "neo4j")
	t.Setenv("KB_WORKER_ID", "composition-worker")
	t.Setenv("KB_LISTEN", "127.0.0.1:8080")
	t.Setenv("KB_TLS_CERT", "")
	t.Setenv("KB_TLS_KEY", "")

	store, err := graph.Open(t.Context(), graph.Config{URI: "bolt://" + host + ":" + port.Port(), Database: "neo4j", User: "neo4j", Secret: secret})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close(context.Background())) })
	journal := journalFixture(t)
	t.Run("missing external head refuses work", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		t.Cleanup(cancel)
		err := runCapture(ctx, slog.New(slog.NewJSONHandler(io.Discard, nil)))
		require.Error(t, err)
		assert.NotErrorIs(t, err, context.DeadlineExceeded)
	})
	_, err = journal.Initialize(t.Context())
	require.NoError(t, err)
	jobs := service.NewJobsService(store, repository.NewJobsRepository(store))
	require.NoError(t, jobs.Enqueue(t.Context(), domain.Job{ID: "composition-job", Kind: "registered", InputDigest: "composition-digest", PolicyRevision: "p1", DueAt: time.Now().UTC()}))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- run(ctx, slog.New(slog.NewJSONHandler(io.Discard, nil)), map[string]platform.RunJob{"registered": func(ctx context.Context, _ domain.Job, _ domain.Lease) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}})
	}()
	select {
	case <-started:
	case err := <-result:
		t.Fatalf("worker stopped before dispatch: %v", err)
	case <-ctx.Done():
		t.Fatal("registered handler was never dispatched")
	}
	cancel()
	select {
	case err := <-result:
		assert.NoError(t, err)
	case <-time.After(6 * time.Second):
		t.Fatal("composition did not stop gracefully")
	}
	t.Run("capture worker drains durable lifecycle purge", func(t *testing.T) {
		owners := repository.NewIdentityRepository(store)
		identity := service.NewIdentityService(store, owners, gateway.NostrIdentity{})
		credential, err := identity.Setup(t.Context(), domain.OwnerIdentity{ID: "owner", PublicKey: "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"}, service.EnrollInput{ClientID: "collector", Role: domain.RoleCollector, ProjectIDs: []string{"demo"}, Capabilities: []string{"artifacts:write"}})
		require.NoError(t, err)
		objectConfig, err := platform.LoadObjectConfig()
		require.NoError(t, err)
		objects, err := gateway.NewS3(objectConfig)
		require.NoError(t, err)
		jobsRepo := repository.NewJobsRepository(store)
		lifecycle := service.NewLifecycleService(store, repository.NewLifecycleRepository(store), owners, jobsRepo, objects, journal, time.Now)
		_, err = lifecycle.UpdatePolicyHost(t.Context(), "demo", "", domain.Policy{Revision: "p1", SpoolLimitBytes: service.MaxArtifactBytes})
		require.NoError(t, err)
		capture := service.NewCaptureService(store, repository.NewEvidenceRepository(store), jobsRepo, objects)
		text := "synthetic worker purge evidence"
		artifact, err := capture.Stage(t.Context(), credential.Principal, service.ArtifactInput{Scope: domain.Scope{ProjectID: "demo", TaskID: "task", SessionID: "session"}, Digest: service.TokenDigest(text), Bytes: int64(len(text)), PolicyRevision: "p1", SourceLocator: "worker-fixture", SourcePath: "fixture.txt"}, strings.NewReader(text))
		require.NoError(t, err)
		_, err = capture.Complete(t.Context(), credential.Principal, artifact.ID, artifact.Digest)
		require.NoError(t, err)
		selection := domain.DeleteSelection{ProjectID: "demo", IDs: []string{artifact.ID}, IdempotencyKey: "worker-delete"}
		preview, err := lifecycle.PreviewHost(t.Context(), selection)
		require.NoError(t, err)
		selection.ExpectedSelectionDigest = preview.SelectionDigest
		receipt, err := lifecycle.DeleteHost(t.Context(), selection)
		require.NoError(t, err)
		require.Equal(t, "purge_pending", receipt.State)
		workerCtx, stopWorker := context.WithTimeout(t.Context(), 30*time.Second)
		result := make(chan error, 1)
		go func() { result <- runCapture(workerCtx, slog.New(slog.NewJSONHandler(io.Discard, nil))) }()
		t.Cleanup(func() { stopWorker(); require.NoError(t, <-result) })
		require.Eventually(t, func() bool {
			status, err := lifecycle.DeletionStatusHost(t.Context(), receipt.ID)
			return err == nil && status.State == "purge_complete"
		}, 20*time.Second, 100*time.Millisecond)
		body, err := objects.Open(t.Context(), artifact.ObjectKey)
		if body != nil {
			require.NoError(t, body.Close())
		}
		assert.ErrorIs(t, err, domain.ErrIncomplete)
	})
	t.Setenv("KB_WORKER_ID", "")
	t.Setenv("KB_S3_ENDPOINT", "http://127.0.0.1:8333")
	t.Setenv("KB_S3_REGION", "local")
	t.Setenv("KB_S3_BUCKET", "evidence")
	t.Setenv("KB_S3_ACCESS_KEY", "fixture")
	t.Setenv("KB_S3_SECRET_KEY", "fixture-only")
	assert.ErrorIs(t, runCapture(t.Context(), slog.New(slog.NewJSONHandler(io.Discard, nil))), domain.ErrInvalidInput)
}

func TestCaptureWorkerMissingConfiguration(t *testing.T) {
	t.Setenv("KB_GRAPH_URI", "")
	assert.ErrorIs(t, runCapture(t.Context(), slog.New(slog.NewJSONHandler(io.Discard, nil))), domain.ErrInvalidInput)
}

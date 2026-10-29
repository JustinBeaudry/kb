package repository_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const captureS3Image = "chrislusf/seaweedfs:4.51@sha256:f7a49e3f3972ebed5be2ed4befa4a0c68eb7bdddba6e692d42729ceb545134b8"

func captureS3(t *testing.T) *gateway.S3 {
	t.Helper()
	c, err := testcontainers.GenericContainer(t.Context(), testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: captureS3Image, ExposedPorts: []string{"8333/tcp"}, Cmd: []string{"mini", "-dir=/data", "-ip.bind=0.0.0.0", "-master.telemetry=false"}, Env: map[string]string{"AWS_ACCESS_KEY_ID": "fixture", "AWS_SECRET_ACCESS_KEY": "fixture-secret-only", "S3_BUCKET": "evidence"}, WaitingFor: wait.ForListeningPort("8333/tcp").WithStartupTimeout(90 * time.Second)}, Started: true})
	if c != nil {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			require.NoError(t, c.Terminate(ctx))
		})
	}
	require.NoError(t, err, "actual local S3 fixture is required")
	host, err := c.Host(t.Context())
	require.NoError(t, err)
	port, err := c.MappedPort(t.Context(), "8333/tcp")
	require.NoError(t, err)
	s, err := gateway.NewS3(gateway.S3Config{Endpoint: "http://" + host + ":" + port.Port(), Region: "local", Bucket: "evidence", AccessKey: "fixture", SecretKey: "fixture-secret-only"})
	require.NoError(t, err)
	return s
}
func seedCapturePolicy(t *testing.T, store *graph.Store, p domain.Policy) {
	t.Helper()
	data, err := json.Marshal(p)
	require.NoError(t, err)
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		native, err := store.Unwrap(tx)
		if err != nil {
			return err
		}
		r, err := native.Run(ctx, `MERGE (p:CapturePolicy {projectID:'demo'}) SET p.data=$data`, map[string]any{"data": string(data)})
		if err != nil {
			return err
		}
		_, err = r.Consume(ctx)
		return err
	}))
}

type interruptObjects struct {
	service.ObjectStore
	after  bool
	before bool
}

func (o *interruptObjects) Put(ctx context.Context, k string, b io.Reader, d string) error {
	if o.before {
		o.before = false
		return domain.ErrUnavailable
	}
	err := o.ObjectStore.Put(ctx, k, b, d)
	if o.after {
		o.after = false
		return domain.ErrUnavailable
	}
	return err
}

type interruptTx struct {
	service.Transactor
	failIn int
}

func (i *interruptTx) Within(ctx context.Context, fn func(context.Context, service.Tx) error) error {
	i.failIn--
	if i.failIn != 0 {
		return i.Transactor.Within(ctx, fn)
	}
	i.failIn = -1
	return i.Transactor.Within(ctx, func(ctx context.Context, tx service.Tx) error {
		if err := fn(ctx, tx); err != nil {
			return err
		}
		return domain.ErrUnavailable
	})
}
func TestCaptureCrashRecovery(t *testing.T) {
	store, jobsRepo, jobs := jobsGraph(t)
	objects := captureS3(t)
	repo := repository.NewEvidenceRepository(store)
	p := domain.Principal{ClientID: "collector", OwnerID: "owner", Role: domain.RoleCollector, ProjectIDs: []string{"demo"}, Capabilities: []string{"artifacts:write", "events:append", "evidence:read"}}
	policy := domain.Policy{Revision: "p1", SpoolLimitBytes: service.MaxArtifactBytes}
	seedCapturePolicy(t, store, policy)
	for _, phase := range []string{"object-before", "object-after", "graph-commit", "ack"} {
		t.Run(phase, func(t *testing.T) {
			payload := []byte("synthetic " + phase)
			meta := service.ArtifactInput{Scope: domain.Scope{ProjectID: "demo", TaskID: phase}, Digest: service.TokenDigest(string(payload)), Bytes: int64(len(payload)), PolicyRevision: "p1", SourceLocator: "transcript.jsonl#bytes=13-37", SourcePath: "transcript.jsonl"}
			ioBoundary := &interruptObjects{ObjectStore: objects, before: phase == "object-before", after: phase == "object-after"}
			tx := &interruptTx{Transactor: store}
			capture := service.NewCaptureService(tx, repo, jobsRepo, ioBoundary)
			a, err := capture.Stage(t.Context(), p, meta, bytes.NewReader(payload))
			if phase == "object-before" || phase == "object-after" {
				require.ErrorIs(t, err, domain.ErrUnavailable)
				a, err = capture.Stage(t.Context(), p, meta, bytes.NewReader(payload))
			}
			require.NoError(t, err)
			if phase == "graph-commit" { // Object exists, publish graph/outbox rolls back together.
				tx.failIn = 2
				_, err = capture.Complete(t.Context(), p, a.ID, a.Digest)
				require.ErrorIs(t, err, domain.ErrUnavailable)
			}
			_, err = capture.Complete(t.Context(), p, a.ID, a.Digest)
			require.NoError(t, err)
			if phase == "ack" { // The committed result was lost before the caller saw it.
				receipt, retryErr := capture.Complete(t.Context(), p, a.ID, a.Digest)
				require.NoError(t, retryErr)
				assert.True(t, receipt.Duplicate)
			}
			var readyArtifacts []domain.Artifact
			var extractionJobs []domain.Job
			require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
				all, err := repo.Pending(ctx, tx, 100)
				if err != nil {
					return err
				}
				for _, v := range all {
					if v.Scope == meta.Scope && v.State == domain.ArtifactReady {
						readyArtifacts = append(readyArtifacts, v)
					}
				}
				queued, err := jobsRepo.List(ctx, tx, domain.JobQueued, 100)
				for _, j := range queued {
					if j.Kind == "evidence.extract" && j.Scope == meta.Scope {
						extractionJobs = append(extractionJobs, j)
					}
				}
				return err
			}))
			assert.Len(t, readyArtifacts, 1)
			assert.Len(t, extractionJobs, 1)
			assert.Equal(t, meta.SourceLocator, readyArtifacts[0].SourceLocator)
			ref := domain.EvidenceRef{ArtifactID: a.ID, Digest: a.Digest, Locator: meta.SourceLocator, Origin: domain.Origin{Trust: domain.TrustGatewayObserved}}
			bound, err := capture.Reference(t.Context(), p, ref, meta.Scope)
			require.NoError(t, err)
			assert.Equal(t, domain.TrustSubmitted, bound.Origin.Trust)

			event := domain.Event{ID: "event", IdempotencyKey: "event-key", Digest: a.Digest, Scope: meta.Scope, Kind: domain.EventToolResult, ObservedAt: time.Now().UTC(), PayloadArtifactID: a.ID, Completeness: domain.CompletenessComplete}
			receipts, appendErr := capture.Append(t.Context(), p, []domain.Event{event})
			require.NoError(t, appendErr)
			require.Len(t, receipts, 1)
			assert.Equal(t, "submitted", receipts[0].State)
			replay, appendErr := capture.Append(t.Context(), p, []domain.Event{event})
			require.NoError(t, appendErr)
			require.Len(t, replay, 1)
			assert.True(t, replay[0].Duplicate)
			assert.Equal(t, receipts[0].ID, replay[0].ID)
			require.NoError(t, objects.Delete(t.Context(), a.ObjectKey))
			_, err = capture.Reference(t.Context(), p, ref, meta.Scope)
			assert.ErrorIs(t, err, domain.ErrIncomplete)
			require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
				stored, _, err := repo.Artifact(ctx, tx, a.ID)
				assert.False(t, stored.ObjectAvailable)
				assert.Equal(t, domain.ArtifactReady, stored.State)
				return err
			}))
		})
	}
	t.Run("worker recovers staged write without caller completion", func(t *testing.T) {
		meta := service.ArtifactInput{Scope: domain.Scope{ProjectID: "demo", TaskID: "worker"}, Digest: service.TokenDigest("worker bytes"), Bytes: 12, PolicyRevision: "p1", SourceLocator: "session#12-24"}
		capture := service.NewCaptureService(store, repo, jobsRepo, objects)
		a, err := capture.Stage(t.Context(), p, meta, bytes.NewBufferString("worker bytes"))
		require.NoError(t, err)
		for range 5 {
			job, lease, err := jobs.Claim(t.Context(), "capture-worker", []string{"capture.reconcile"}, time.Now().UTC())
			require.NoError(t, err)
			err = capture.Reconcile(t.Context(), job, lease, jobs)
			if errors.Is(err, domain.ErrIncomplete) {
				require.NoError(t, jobs.Fail(t.Context(), lease, time.Now().Add(time.Minute), "handler_failed"))
				continue
			}
			require.NoError(t, err)
			require.NoError(t, jobs.Complete(t.Context(), lease, job.InputDigest))
			if job.Scope == meta.Scope {
				break
			}
		}
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			stored, _, err := repo.Artifact(ctx, tx, a.ID)
			assert.Equal(t, domain.ArtifactReady, stored.State)
			return err
		}))
	})
	t.Run("checkpoint compare-and-swap and client task ownership", func(t *testing.T) {
		s := service.NewCheckpointService(store, repository.NewCheckpointRepository(store))
		p.Capabilities = append(p.Capabilities, "checkpoint:write")
		c := domain.Checkpoint{ID: "checkpoint", Scope: domain.Scope{ProjectID: "demo", TaskID: "checkpoint-task"}, Objective: "synthetic task"}
		created, err := s.Put(t.Context(), p, "", c)
		require.NoError(t, err)
		_, err = s.Put(t.Context(), p, "", c)
		assert.ErrorIs(t, err, domain.ErrConflict)
		other := p
		other.ClientID = "other"
		c.ID = "other-checkpoint"
		_, err = s.Put(t.Context(), other, "", c)
		assert.ErrorIs(t, err, domain.ErrForbidden)
		c.ID = "checkpoint"
		updated, err := s.Put(t.Context(), p, created.Revision, c)
		require.NoError(t, err)
		assert.NotEqual(t, created.Revision, updated.Revision)
	})
}

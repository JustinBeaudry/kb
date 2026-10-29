package repository_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func jobsGraph(t *testing.T) (*graph.Store, *repository.JobsRepository, *service.JobsService) {
	t.Helper()
	store, repo, jobs, _ := jobsGraphConfig(t)
	return store, repo, jobs
}
func jobsGraphConfig(t *testing.T) (*graph.Store, *repository.JobsRepository, *service.JobsService, graph.Config) {
	t.Helper()
	data, err := os.ReadFile("../../../deploy/versions.json")
	require.NoError(t, err)
	var pin struct {
		Graph struct {
			Image string `json:"image"`
		} `json:"graph"`
	}
	require.NoError(t, json.Unmarshal(data, &pin))
	const secret = "jobs-graph-fixture-only"
	c, err := testcontainers.GenericContainer(t.Context(), testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: pin.Graph.Image, ExposedPorts: []string{"7687/tcp"}, Env: map[string]string{"NEO4J_AUTH": "neo4j/" + secret, "NEO4J_server_memory_heap_initial__size": "256m", "NEO4J_server_memory_heap_max__size": "512m", "NEO4J_server_memory_pagecache_size": "128m"}, WaitingFor: wait.ForLog("Started.").WithStartupTimeout(2 * time.Minute)}, Started: true})
	if c != nil {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			require.NoError(t, c.Terminate(ctx))
		})
	}
	require.NoError(t, err, "real graph jobs validation must not skip")
	host, err := c.Host(t.Context())
	require.NoError(t, err)
	port, err := c.MappedPort(t.Context(), "7687/tcp")
	require.NoError(t, err)
	cfg := graph.Config{URI: "bolt://" + host + ":" + port.Port(), Database: "neo4j", User: "neo4j", Secret: secret}
	store, err := graph.Open(t.Context(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close(context.Background())) })
	repo := repository.NewJobsRepository(store)
	return store, repo, service.NewJobsService(store, repo), cfg
}
func queueJob(t *testing.T, s *service.JobsService, id string, due time.Time) domain.Job {
	t.Helper()
	job := domain.Job{ID: id, Kind: "verify", InputDigest: "digest-" + id, PolicyRevision: "policy-1", DueAt: due, Scope: domain.Scope{ProjectID: "demo"}, InputRevisionIDs: []string{"revision-1"}}
	require.NoError(t, s.Enqueue(t.Context(), job))
	return job
}
func TestExpiredWorkerCannotCommit(t *testing.T) {
	t.Run("expired fence cannot publish", func(t *testing.T) {
		store, repo, s := jobsGraph(t)
		now := time.Now().UTC()
		queueJob(t, s, "expired", now.Add(-2*time.Minute))
		_, lease, err := s.Claim(t.Context(), "worker-old", []string{"verify"}, now.Add(-2*time.Minute))
		require.NoError(t, err)
		lateCommitErr := s.Complete(t.Context(), lease, "digest-expired")
		assert.ErrorIs(t, lateCommitErr, domain.ErrStale)
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			effects, err := repo.Effects(ctx, tx, "expired")
			assert.Empty(t, effects)
			return err
		}))
	})
}
func TestDuplicateEnqueueCreatesOneJob(t *testing.T) {
	t.Run("replay deduplicates across ids", func(t *testing.T) {
		store, repo, s := jobsGraph(t)
		job := queueJob(t, s, "first", time.Now().UTC())
		job.ID = "second"
		err := s.Enqueue(t.Context(), job)
		require.NoError(t, err)
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			persistedJobs, err := repo.List(ctx, tx, domain.JobQueued, 50)
			assert.Len(t, persistedJobs, 1)
			return err
		}))
	})
}
func TestCrashAfterCommitReclaimsOnce(t *testing.T) {
	t.Run("committed claim expires and only replacement can finish", func(t *testing.T) {
		store, repo, s := jobsGraph(t)
		now := time.Now().UTC()
		queueJob(t, s, "crash", now.Add(-2*time.Minute))
		_, old, err := s.Claim(t.Context(), "dead-process", []string{"verify"}, now.Add(-2*time.Minute))
		require.NoError(t, err)
		_, replacement, err := s.Claim(t.Context(), "replacement", []string{"verify"}, now)
		require.NoError(t, err)
		require.Greater(t, replacement.Fence, old.Fence)
		require.NoError(t, s.Complete(t.Context(), replacement, "digest-crash"))
		err = s.Complete(t.Context(), replacement, "digest-crash")
		require.NoError(t, err)
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			effects, err := repo.Effects(ctx, tx, "crash")
			assert.Len(t, effects, 1)
			return err
		}))
	})
}

func TestJobsLifecycleRealGraph(t *testing.T) {
	store, repo, s := jobsGraph(t)
	t.Run("replacement fence rejects old worker", func(t *testing.T) {
		now := time.Now().UTC()
		queueJob(t, s, "fenced", now.Add(-2*time.Minute))
		_, old, err := s.Claim(t.Context(), "old", []string{"verify"}, now.Add(-2*time.Minute))
		require.NoError(t, err)
		_, _, err = s.Claim(t.Context(), "new", []string{"verify"}, now)
		require.NoError(t, err)
		err = s.Complete(t.Context(), old, "digest-fenced")
		assert.ErrorIs(t, err, domain.ErrStale)
	})
	t.Run("wrong input digest cannot complete", func(t *testing.T) {
		now := time.Now().UTC()
		queueJob(t, s, "wrong-digest", now)
		_, lease, err := s.Claim(t.Context(), "worker", []string{"verify"}, now)
		require.NoError(t, err)
		err = s.Complete(t.Context(), lease, "different")
		assert.ErrorIs(t, err, domain.ErrStale)
	})
	t.Run("business failure rolls back completion and outbox", func(t *testing.T) {
		now := time.Now().UTC()
		queueJob(t, s, "rollback", now)
		job, lease, err := s.Claim(t.Context(), "worker", []string{"verify"}, now)
		require.NoError(t, err)
		err = s.Commit(t.Context(), lease, job.InputDigest, func(ctx context.Context, tx service.Tx) error {
			require.NoError(t, repo.Enqueue(ctx, tx, domain.Job{ID: "rolled-back-effect", Kind: "effect", InputDigest: "rollback", PolicyRevision: "policy-1", DueAt: now}))
			return domain.ErrConflict
		})
		assert.ErrorIs(t, err, domain.ErrConflict)
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			jobs, err := repo.List(ctx, tx, domain.JobQueued, 100)
			for _, j := range jobs {
				assert.NotEqual(t, "rolled-back-effect", j.ID)
			}
			return err
		}))
	})
	t.Run("five failures become inspectable dead job", func(t *testing.T) {
		base := time.Now().UTC()
		queueJob(t, s, "dead", base)
		// Claims use an explicit clock. Give each retry a later instant without
		// sleeping, while all fence-sensitive writes still validate wall-clock expiry.
		for attempt := 0; attempt < 4; attempt++ {
			_, lease, err := s.Claim(t.Context(), "retry", []string{"verify"}, base.Add(time.Duration(attempt)*10*time.Second))
			require.NoError(t, err)
			require.NoError(t, s.Fail(t.Context(), lease, base, "handler_failed"))
		}
		_, lease, err := s.Claim(t.Context(), "retry", []string{"verify"}, base.Add(40*time.Second))
		require.NoError(t, err)
		err = s.Fail(t.Context(), lease, base, "unsupported_kind")
		require.NoError(t, err)
		dead, err := s.Dead(t.Context(), 100)
		require.NoError(t, err)
		require.Len(t, dead, 1)
		assert.Equal(t, "dead", dead[0].ID)
		assert.Equal(t, 5, dead[0].Attempts)
		assert.Equal(t, "unsupported_kind", dead[0].ReasonCode)
	})
	t.Run("cancel invalidates an active lease", func(t *testing.T) {
		now := time.Now().UTC()
		queueJob(t, s, "cancel", now)
		_, lease, err := s.Claim(t.Context(), "worker", []string{"verify"}, now)
		require.NoError(t, err)
		require.NoError(t, s.Cancel(t.Context(), lease.JobID))
		err = s.Complete(t.Context(), lease, "digest-cancel")
		assert.ErrorIs(t, err, domain.ErrStale)
	})
	t.Run("heartbeat extends live lease", func(t *testing.T) {
		now := time.Now().UTC()
		queueJob(t, s, "heartbeat", now.Add(-30*time.Second))
		_, lease, err := s.Claim(t.Context(), "worker", []string{"verify"}, now.Add(-30*time.Second))
		require.NoError(t, err)
		renewed, err := s.Heartbeat(t.Context(), lease)
		require.NoError(t, err)
		assert.Greater(t, renewed.ExpiresAt, lease.ExpiresAt)
		assert.Equal(t, lease.Fence, renewed.Fence)
	})

	t.Run("heartbeat cannot exceed execution deadline", func(t *testing.T) {
		now := time.Now().UTC()
		queueJob(t, s, "heartbeat-bound", now)
		_, lease, err := s.Claim(t.Context(), "worker", []string{"verify"}, now)
		require.NoError(t, err)
		deadline := now.Add(10 * time.Second)
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			native, err := store.Unwrap(tx)
			if err != nil {
				return err
			}
			result, err := native.Run(ctx, "MATCH (j:Job {id:$id}) SET j.maxLeaseUntil=$deadline", map[string]any{"id": lease.JobID, "deadline": deadline.UnixNano()})
			if err != nil {
				return err
			}
			_, err = result.Consume(ctx)
			return err
		}))
		renewed, err := s.Heartbeat(t.Context(), lease)
		require.NoError(t, err)
		assert.Equal(t, deadline, renewed.ExpiresAt)
	})
	t.Run("heartbeat rejects ended execution deadline", func(t *testing.T) {
		now := time.Now().UTC()
		queueJob(t, s, "heartbeat-ended", now)
		_, lease, err := s.Claim(t.Context(), "worker", []string{"verify"}, now)
		require.NoError(t, err)
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			native, err := store.Unwrap(tx)
			if err != nil {
				return err
			}
			result, err := native.Run(ctx, "MATCH (j:Job {id:$id}) SET j.maxLeaseUntil=$deadline", map[string]any{"id": lease.JobID, "deadline": now.Add(-time.Second).UnixNano()})
			if err != nil {
				return err
			}
			_, err = result.Consume(ctx)
			return err
		}))
		_, err = s.Heartbeat(t.Context(), lease)
		assert.ErrorIs(t, err, domain.ErrStale)
	})
	t.Run("same id with changed input conflicts", func(t *testing.T) {
		job := queueJob(t, s, "id-conflict", time.Now().UTC())
		job.InputDigest = "new-digest"
		err := s.Enqueue(t.Context(), job)
		assert.ErrorIs(t, err, domain.ErrConflict)
	})
	t.Run("retry delay has upper bound", func(t *testing.T) {
		now := time.Now().UTC()
		job := domain.Job{ID: "backoff", Kind: "bounded", InputDigest: "bounded-digest", PolicyRevision: "p1", DueAt: now}
		require.NoError(t, s.Enqueue(t.Context(), job))
		_, lease, err := s.Claim(t.Context(), "worker", []string{"bounded"}, now)
		require.NoError(t, err)
		err = s.Fail(t.Context(), lease, now.Add(24*time.Hour), "handler_failed")
		require.NoError(t, err)
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			jobs, err := repo.List(ctx, tx, domain.JobQueued, 100)
			for _, job := range jobs {
				if job.ID == "backoff" {
					assert.WithinDuration(t, now.Add(5*time.Minute), job.DueAt, 2*time.Second)
					return err
				}
			}
			t.Error("failed job disappeared")
			return err
		}))
	})
	t.Run("policy revision creates independent work", func(t *testing.T) {
		job := queueJob(t, s, "policy", time.Now().UTC())
		job.ID = "policy-new"
		job.PolicyRevision = "policy-2"
		err := s.Enqueue(t.Context(), job)
		require.NoError(t, err)
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			jobs, err := repo.List(ctx, tx, domain.JobQueued, 100)
			var ids []string
			for _, j := range jobs {
				if j.InputDigest == "digest-policy" {
					ids = append(ids, j.ID)
				}
			}
			assert.ElementsMatch(t, []string{"policy", "policy-new"}, ids)
			return err
		}))
	})
}

// TestJobsWorkerProcess is a real OS process worker fixture, not a fake queue.
func TestJobsWorkerProcess(t *testing.T) {
	if os.Getenv("KB_TEST_JOBS_PROCESS") != "1" {
		return
	}
	store, err := graph.Open(t.Context(), graph.Config{URI: os.Getenv("KB_TEST_JOBS_URI"), Database: "neo4j", User: "neo4j", Secret: "jobs-graph-fixture-only"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close(context.Background())) })
	repo := repository.NewJobsRepository(store)
	jobs := service.NewJobsService(store, repo)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	t.Cleanup(cancel)
	worker := platform.Worker{ID: os.Getenv("KB_TEST_JOBS_WORKER"), Kinds: []string{"verify"}, Jobs: jobs, PollInterval: 10 * time.Millisecond, RunJob: func(ctx context.Context, job domain.Job, lease domain.Lease) error {
		// Keep the winning lease active while the other process tries to claim it.
		timer := time.NewTimer(150 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		return jobs.Commit(ctx, lease, job.InputDigest, func(ctx context.Context, tx service.Tx) error {
			return repo.Enqueue(ctx, tx, domain.Job{ID: "logical-effect", Kind: "result", InputDigest: "result-" + job.InputDigest, PolicyRevision: job.PolicyRevision, DueAt: time.Now().UTC()})
		})
	}}
	require.NoError(t, worker.Run(ctx))
}
func TestJobsTwoWorkerProcessesRace(t *testing.T) {
	store, repo, s, cfg := jobsGraphConfig(t)
	queueJob(t, s, "race", time.Now().UTC())
	var commands []*exec.Cmd
	var outputs []*bytes.Buffer
	for _, id := range []string{"process-a", "process-b"} {
		command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestJobsWorkerProcess$", "-test.v")
		command.Env = append(os.Environ(), "KB_TEST_JOBS_PROCESS=1", "KB_TEST_JOBS_URI="+cfg.URI, "KB_TEST_JOBS_WORKER="+id)
		output := new(bytes.Buffer)
		command.Stdout = output
		command.Stderr = output
		outputs = append(outputs, output)
		commands = append(commands, command)
		require.NoError(t, command.Start())
		t.Cleanup(func() {
			if command.ProcessState == nil {
				_ = command.Process.Kill()
				_ = command.Wait()
			}
		})
	}
	// One concurrent execution is the Act; each process owns an independent driver.
	for i, command := range commands {
		require.NoError(t, command.Wait(), outputs[i].String())
	}
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		completed, err := repo.List(ctx, tx, domain.JobSucceeded, 100)
		if err != nil {
			return err
		}
		require.Len(t, completed, 1)
		assert.Equal(t, 1, completed[0].Attempts)
		queued, err := repo.List(ctx, tx, domain.JobQueued, 100)
		if err != nil {
			return err
		}
		require.Len(t, queued, 1)
		assert.Equal(t, "logical-effect", queued[0].ID)
		effects, err := repo.Effects(ctx, tx, "race")
		assert.Len(t, effects, 1)
		return err
	}))
}

func TestJobsExhaustedCrashBecomesDead(t *testing.T) {
	_, _, s := jobsGraph(t)
	now := time.Now().UTC()
	base := now.Add(-10 * time.Minute)
	queueJob(t, s, "crashed-five-times", base)
	for attempt := 0; attempt < 5; attempt++ {
		_, _, err := s.Claim(t.Context(), "crashing-worker", []string{"verify"}, base.Add(time.Duration(attempt)*61*time.Second))
		require.NoError(t, err)
	}
	_, _, err := s.Claim(t.Context(), "replacement", []string{"verify"}, now)
	assert.ErrorIs(t, err, domain.ErrIncomplete)
	dead, err := s.Dead(t.Context(), 100)
	require.NoError(t, err)
	require.Len(t, dead, 1)
	assert.Equal(t, "lease_expired", dead[0].ReasonCode)
	assert.Equal(t, 5, dead[0].Attempts)
}

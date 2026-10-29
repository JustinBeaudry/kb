package platform_test

import (
	"context"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkerUnknownKindFailsVisibly(t *testing.T) {
	run := platform.DispatchJobs(map[string]platform.RunJob{"registered": func(context.Context, domain.Job, domain.Lease) error { return nil }})
	err := run(t.Context(), domain.Job{Kind: "unknown"}, domain.Lease{})
	assert.ErrorIs(t, err, domain.ErrUnavailable)
}
func TestWorkerRequiresHandler(t *testing.T) {
	err := (&platform.Worker{ID: "worker", Kinds: []string{"verify"}}).Run(t.Context())
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
}

type stoppedJobs struct{ completed chan struct{} }

func (*stoppedJobs) Claim(ctx context.Context, _ string, _ []string, _ time.Time) (domain.Job, domain.Lease, error) {
	return domain.Job{ID: "job", Kind: "verify", InputDigest: "digest"}, domain.Lease{JobID: "job", WorkerID: "worker", Fence: 1, ExpiresAt: time.Now().Add(time.Minute)}, nil
}
func (s *stoppedJobs) Complete(context.Context, domain.Lease, string) error {
	s.completed <- struct{}{}
	return nil
}
func (*stoppedJobs) Fail(context.Context, domain.Lease, time.Time, string) error { return nil }
func (*stoppedJobs) Heartbeat(context.Context, domain.Lease) (domain.Lease, error) {
	return domain.Lease{}, domain.ErrStale
}
func TestWorkerShutdownDoesNotCompleteUnfinishedWork(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	started := make(chan struct{})
	jobs := &stoppedJobs{completed: make(chan struct{}, 1)}
	worker := &platform.Worker{ID: "worker", Kinds: []string{"verify"}, Jobs: jobs, RunJob: func(ctx context.Context, _ domain.Job, _ domain.Lease) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}}
	result := make(chan error, 1)
	go func() { result <- worker.Run(ctx) }()
	<-started
	cancel()
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop")
	}
	select {
	case <-jobs.completed:
		t.Fatal("unfinished work marked complete")
	default:
	}
}

type workerScript struct {
	claim     func(context.Context, string, []string, time.Time) (domain.Job, domain.Lease, error)
	complete  func(context.Context, domain.Lease, string) error
	fail      func(context.Context, domain.Lease, time.Time, string) error
	heartbeat func(context.Context, domain.Lease) (domain.Lease, error)
}

func (s workerScript) Claim(ctx context.Context, id string, kinds []string, now time.Time) (domain.Job, domain.Lease, error) {
	return s.claim(ctx, id, kinds, now)
}
func (s workerScript) Complete(ctx context.Context, lease domain.Lease, digest string) error {
	return s.complete(ctx, lease, digest)
}
func (s workerScript) Fail(ctx context.Context, lease domain.Lease, at time.Time, reason string) error {
	return s.fail(ctx, lease, at, reason)
}
func (s workerScript) Heartbeat(ctx context.Context, lease domain.Lease) (domain.Lease, error) {
	return s.heartbeat(ctx, lease)
}
func TestWorkerDispatchLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name        string
		handlerErr  error
		wantReason  string
		completeErr error
	}{
		{name: "successful handler completes digest"},
		{name: "handler failure stores safe reason", handlerErr: domain.ErrConflict, wantReason: "handler_failed"},
		{name: "unknown handler stores explicit reason", handlerErr: platform.ErrUnknownJobKind, wantReason: "unsupported_kind"},
		{name: "completion lease loss is discarded", completeErr: domain.ErrStale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			completed := make(chan string, 1)
			failed := make(chan string, 1)
			jobs := workerScript{
				claim: func(context.Context, string, []string, time.Time) (domain.Job, domain.Lease, error) {
					return domain.Job{ID: "job", Kind: "verify", InputDigest: "expected"}, domain.Lease{JobID: "job", WorkerID: "worker", Fence: 1, ExpiresAt: time.Now().Add(time.Minute)}, nil
				},
				complete: func(_ context.Context, _ domain.Lease, digest string) error {
					completed <- digest
					cancel()
					return tc.completeErr
				},
				fail: func(_ context.Context, _ domain.Lease, _ time.Time, reason string) error {
					failed <- reason
					cancel()
					return nil
				},
			}
			worker := platform.Worker{ID: "worker", Kinds: []string{"verify"}, Jobs: jobs, RunJob: func(context.Context, domain.Job, domain.Lease) error { return tc.handlerErr }}
			err := worker.Run(ctx)
			require.NoError(t, err)
			if tc.wantReason != "" {
				assert.Equal(t, tc.wantReason, <-failed)
				assert.Empty(t, completed)
			} else {
				assert.Equal(t, "expected", <-completed)
				assert.Empty(t, failed)
			}
		})
	}
}
func TestWorkerStopsAtRuntimeBound(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	reasons := make(chan string, 1)
	jobs := workerScript{
		claim: func(context.Context, string, []string, time.Time) (domain.Job, domain.Lease, error) {
			return domain.Job{ID: "job", Kind: "verify"}, domain.Lease{JobID: "job", WorkerID: "worker", Fence: 1, ExpiresAt: time.Now().Add(time.Minute)}, nil
		},
		complete: func(context.Context, domain.Lease, string) error { t.Error("timed out handler completed"); return nil },
		fail: func(_ context.Context, _ domain.Lease, _ time.Time, reason string) error {
			reasons <- reason
			cancel()
			return nil
		},
	}
	worker := platform.Worker{ID: "worker", Kinds: []string{"verify"}, Jobs: jobs, MaxRun: 10 * time.Millisecond, RunJob: func(context.Context, domain.Job, domain.Lease) error { <-release; return nil }}
	err := worker.Run(ctx)
	require.NoError(t, err)
	assert.Equal(t, "deadline_exceeded", <-reasons)
}
func TestWorkerClaimErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		claimErr error
		want     error
	}{
		{"unavailable stops visibly", domain.ErrUnavailable, domain.ErrUnavailable},
		{"empty queue respects cancellation", domain.ErrIncomplete, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			t.Cleanup(cancel)
			jobs := workerScript{claim: func(context.Context, string, []string, time.Time) (domain.Job, domain.Lease, error) {
				return domain.Job{}, domain.Lease{}, tc.claimErr
			}}
			worker := platform.Worker{ID: "worker", Kinds: []string{"verify"}, Jobs: jobs, PollInterval: time.Millisecond, RunJob: func(context.Context, domain.Job, domain.Lease) error { return nil }}
			err := worker.Run(ctx)
			assert.ErrorIs(t, err, tc.want)
		})
	}
}
func TestWorkerHeartbeatLossCancelsHandler(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	t.Cleanup(cancel)
	handlerStopped := make(chan struct{})
	completed := make(chan struct{}, 1)
	jobs := workerScript{
		claim: func(context.Context, string, []string, time.Time) (domain.Job, domain.Lease, error) {
			return domain.Job{ID: "job", Kind: "verify"}, domain.Lease{JobID: "job", WorkerID: "worker", Fence: 1, ExpiresAt: time.Now().Add(time.Minute)}, nil
		},
		complete: func(context.Context, domain.Lease, string) error { completed <- struct{}{}; return nil },
		heartbeat: func(context.Context, domain.Lease) (domain.Lease, error) {
			cancel()
			return domain.Lease{}, domain.ErrStale
		},
	}
	worker := platform.Worker{ID: "worker", Kinds: []string{"verify"}, Jobs: jobs, RunJob: func(ctx context.Context, _ domain.Job, _ domain.Lease) error {
		<-ctx.Done()
		close(handlerStopped)
		return nil
	}}
	err := worker.Run(ctx)
	require.NoError(t, err)
	select {
	case <-handlerStopped:
	case <-time.After(time.Second):
		t.Fatal("lease loss did not cancel handler")
	}
	assert.Empty(t, completed)
}

func TestWorkerHeartbeatErrorStopsClaiming(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	t.Cleanup(cancel)
	claims := 0
	jobs := workerScript{
		claim: func(context.Context, string, []string, time.Time) (domain.Job, domain.Lease, error) {
			claims++
			if claims > 1 {
				return domain.Job{}, domain.Lease{}, domain.ErrConflict
			}
			return domain.Job{ID: "job", Kind: "verify"}, domain.Lease{JobID: "job", WorkerID: "worker", Fence: 1, ExpiresAt: time.Now().Add(time.Minute)}, nil
		},
		heartbeat: func(context.Context, domain.Lease) (domain.Lease, error) {
			return domain.Lease{}, domain.ErrUnavailable
		},
	}
	worker := platform.Worker{ID: "worker", Kinds: []string{"verify"}, Jobs: jobs, RunJob: func(ctx context.Context, _ domain.Job, _ domain.Lease) error { <-ctx.Done(); return ctx.Err() }}
	err := worker.Run(ctx)
	assert.ErrorIs(t, err, domain.ErrUnavailable)
}

func TestWorkerHeartbeatRefreshesCompletionLease(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	t.Cleanup(cancel)
	renewed := domain.Lease{JobID: "job", WorkerID: "worker", Fence: 1, ExpiresAt: time.Now().Add(2 * time.Minute)}
	heartbeat := make(chan struct{})
	completed := make(chan domain.Lease, 1)
	jobs := workerScript{
		claim: func(context.Context, string, []string, time.Time) (domain.Job, domain.Lease, error) {
			return domain.Job{ID: "job", Kind: "verify", InputDigest: "digest"}, domain.Lease{JobID: "job", WorkerID: "worker", Fence: 1, ExpiresAt: time.Now().Add(time.Minute)}, nil
		},
		heartbeat: func(context.Context, domain.Lease) (domain.Lease, error) { close(heartbeat); return renewed, nil },
		complete:  func(_ context.Context, lease domain.Lease, _ string) error { completed <- lease; cancel(); return nil },
	}
	worker := platform.Worker{ID: "worker", Kinds: []string{"verify"}, Jobs: jobs, RunJob: func(ctx context.Context, _ domain.Job, _ domain.Lease) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-heartbeat:
			return nil
		}
	}}
	err := worker.Run(ctx)
	require.NoError(t, err)
	assert.Equal(t, renewed, <-completed)
}

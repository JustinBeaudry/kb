package platform

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/service"
)

// RunJob executes outside the claim transaction. Handlers that publish graph
// state use JobsService.Commit; any future network effect requires natural
// deduplication/idempotency and its own outbox consumer.
type RunJob func(context.Context, domain.Job, domain.Lease) error

var ErrUnknownJobKind = errors.New("unregistered job kind")

func DispatchJobs(handlers map[string]RunJob) RunJob {
	registered := make(map[string]RunJob, len(handlers))
	for kind, handler := range handlers {
		registered[kind] = handler
	}
	return func(ctx context.Context, job domain.Job, lease domain.Lease) error {
		handler, ok := registered[job.Kind]
		if !ok || handler == nil {
			return errors.Join(domain.ErrUnavailable, ErrUnknownJobKind)
		}
		return handler(ctx, job, lease)
	}
}

// WorkerJobs is the consumer port; worker orchestration does not access storage.
type WorkerJobs interface {
	Claim(context.Context, string, []string, time.Time) (domain.Job, domain.Lease, error)
	Complete(context.Context, domain.Lease, string) error
	Fail(context.Context, domain.Lease, time.Time, string) error
	Heartbeat(context.Context, domain.Lease) (domain.Lease, error)
}
type Worker struct {
	ID           string
	Kinds        []string
	Jobs         WorkerJobs
	RunJob       RunJob
	Logger       *slog.Logger
	PollInterval time.Duration
	MaxRun       time.Duration
}

func (w *Worker) Run(ctx context.Context) error {
	if domain.ValidateIDs(w.ID) != nil || len(w.Kinds) == 0 || w.Jobs == nil || w.RunJob == nil {
		return domain.ErrInvalidInput
	}
	for _, kind := range w.Kinds {
		if domain.ValidateIDs(kind) != nil {
			return domain.ErrInvalidInput
		}
	}
	poll := w.PollInterval
	if poll == 0 {
		poll = time.Second
	}
	if poll < time.Millisecond || poll > time.Minute {
		return domain.ErrInvalidInput
	}
	maxRun := w.MaxRun
	if maxRun == 0 {
		maxRun = service.JobMaxRuntime
	}
	if maxRun <= 0 || maxRun > service.JobMaxRuntime {
		return domain.ErrInvalidInput
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		job, lease, err := w.Jobs.Claim(claimCtx, w.ID, w.Kinds, time.Now().UTC())
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if !errors.Is(err, domain.ErrIncomplete) {
				return err
			}
			timer := time.NewTimer(poll)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
			continue
		}
		if err := w.execute(ctx, job, lease, maxRun); err != nil {
			// Repository adapters normalize canceled I/O into unavailable. A
			// shutdown racing final settlement is still a graceful worker stop;
			// an unfinished lease remains recoverable by the next worker.
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}
func (w *Worker) execute(ctx context.Context, job domain.Job, lease domain.Lease, maxRun time.Duration) error {
	runCtx, cancel := context.WithTimeout(ctx, maxRun)
	defer cancel()
	result := make(chan error, 1)
	go func(claimed domain.Lease) { result <- w.RunJob(runCtx, job, claimed) }(lease)
	heartbeat := time.NewTicker(service.JobHeartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-runCtx.Done():
			reason := "deadline_exceeded"
			if ctx.Err() != nil {
				reason = "worker_stopped"
			}
			// A bounded independent context lets shutdown requeue while the parent is
			// cancelled. Failure leaves the unfinished lease to expire naturally.
			cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			_ = w.Jobs.Fail(cleanup, lease, time.Now().UTC().Add(time.Second), reason)
			stop()
			return nil
		case <-heartbeat.C:
			beatCtx, stop := context.WithTimeout(runCtx, 5*time.Second)
			renewed, err := w.Jobs.Heartbeat(beatCtx, lease)
			stop()
			if err != nil {
				cancel()
				if w.Logger != nil {
					w.Logger.Warn("job_lease_lost", "reason", "heartbeat_failed")
				}
				if errors.Is(err, domain.ErrStale) || ctx.Err() != nil {
					return nil
				}
				return err
			}
			lease = renewed
		case err := <-result:
			if runCtx.Err() != nil {
				continue
			}
			operationCtx, stop := context.WithTimeout(runCtx, 5*time.Second)
			if err == nil {
				err = w.Jobs.Complete(operationCtx, lease, job.InputDigest)
			} else {
				reason := "handler_failed"
				if errors.Is(err, ErrUnknownJobKind) {
					reason = "unsupported_kind"
				}
				if w.Logger != nil {
					w.Logger.Error("job_failed", "reason", reason)
				}
				err = w.Jobs.Fail(operationCtx, lease, time.Now().UTC().Add(time.Second), reason)
			}
			stop()
			if errors.Is(err, domain.ErrStale) {
				return nil
			}
			return err
		}
	}
}

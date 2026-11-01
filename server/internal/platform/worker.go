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
type WorkerAdmission interface {
	Admit(context.Context, string, string) (domain.OperationPermit, error)
	Finish(context.Context, domain.OperationPermit) error
}
type Worker struct {
	ID           string
	InstanceID   string
	Admission    WorkerAdmission
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
		var permit domain.OperationPermit
		if w.Admission != nil {
			var err error
			permit, err = w.Admission.Admit(ctx, w.InstanceID, "worker")
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				if errors.Is(err, domain.ErrMaintenance) {
					if !waitWorker(ctx, poll) {
						return nil
					}
					continue
				}
				return err
			}
		}
		operationCtx := domain.WithOperationOutcome(ctx)
		claimCtx, cancel := context.WithTimeout(operationCtx, 5*time.Second)
		job, lease, err := w.Jobs.Claim(claimCtx, w.ID, w.Kinds, time.Now().UTC())
		cancel()
		if err != nil {
			finishErr := w.finishPermit(operationCtx, permit)
			if ctx.Err() != nil {
				return nil
			}
			if finishErr != nil {
				return finishErr
			}
			if !errors.Is(err, domain.ErrIncomplete) {
				return err
			}
			if !waitWorker(ctx, poll) {
				return nil
			}
			continue
		}
		err = w.execute(operationCtx, job, lease, maxRun)
		finishErr := w.finishPermit(operationCtx, permit)
		if ctx.Err() != nil {
			return nil
		}
		if finishErr != nil {
			return finishErr
		}
		if err != nil {
			return err
		}
	}
}
func waitWorker(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func (w *Worker) finishPermit(ctx context.Context, p domain.OperationPermit) error {
	if w.Admission == nil {
		return nil
	}
	return FinishOperation(ctx, w.Admission, p, nil)
}
func (w *Worker) interrupted(ctx context.Context, lease domain.Lease) error {
	reason := "deadline_exceeded"
	if ctx.Err() != nil {
		reason = "worker_stopped"
	}
	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	_ = w.Jobs.Fail(cleanup, lease, time.Now().UTC().Add(time.Second), reason)
	return domain.ErrUnknownOutcome
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
			cancel()
			// Cancellation is a request to stop, not proof an effect ended.
			// Join the handler before returning; retain its durable permit.
			if handlerErr := <-result; handlerErr != nil && w.Logger != nil {
				w.Logger.Warn("job_interrupted", "reason", "handler_failed")
			}
			return w.interrupted(ctx, lease)
		case <-heartbeat.C:
			beatCtx, stop := context.WithTimeout(runCtx, 5*time.Second)
			renewed, err := w.Jobs.Heartbeat(beatCtx, lease)
			stop()
			if err != nil {
				cancel()
				if handlerErr := <-result; handlerErr != nil && w.Logger != nil {
					w.Logger.Warn("job_interrupted", "reason", "handler_failed")
				}
				if w.Logger != nil {
					w.Logger.Warn("job_lease_lost", "reason", "heartbeat_failed")
				}
				if errors.Is(err, domain.ErrStale) || ctx.Err() != nil {
					return domain.ErrUnknownOutcome
				}
				return err
			}
			lease = renewed
		case err := <-result:
			if runCtx.Err() != nil {
				return w.interrupted(ctx, lease)
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

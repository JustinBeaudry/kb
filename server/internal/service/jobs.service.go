package service

import (
	"context"
	"errors"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// Job policy is owned here, separately from persistence and worker timers.
const (
	JobLeaseTTL          = time.Minute
	JobHeartbeatInterval = 20 * time.Second
	// JobMaxRuntime bounds one claimed attempt, independently of its renewable TTL.
	JobMaxRuntime       = 10 * time.Minute
	JobMaxAttempts      = 5
	jobMaxRetryDelay    = 5 * time.Minute
	jobMaintenanceBatch = 100
)

// JobsRepository consumes service-supplied lease timestamps and state. It owns
// atomic persistence conditions, never retry, exhaustion, or lease policy.
type JobsRepository interface {
	Enqueue(context.Context, Tx, domain.Job) error
	Next(context.Context, Tx, []string, time.Time) (domain.Job, domain.Lease, error)
	Claim(context.Context, Tx, domain.Lease, time.Time, time.Time) (domain.Job, domain.Lease, error)
	Exhaust(context.Context, Tx, domain.Lease, time.Time, string) error
	Leased(context.Context, Tx, domain.Lease, time.Time) (domain.Job, time.Time, error)
	CheckLease(context.Context, Tx, domain.Lease, string, time.Time) error
	Finish(context.Context, Tx, domain.Lease, string, time.Time) error
	Heartbeat(context.Context, Tx, domain.Lease, time.Time, time.Time) (domain.Lease, error)
	Fail(context.Context, Tx, domain.Lease, time.Time, string, domain.JobStatus, time.Time) error
	Cancel(context.Context, Tx, string) error
	List(context.Context, Tx, domain.JobStatus, int) ([]domain.Job, error)
}
type JobsService struct {
	tx   Transactor
	repo JobsRepository
}

func NewJobsService(tx Transactor, repo JobsRepository) *JobsService {
	return &JobsService{tx: tx, repo: repo}
}
func (s *JobsService) Enqueue(ctx context.Context, job domain.Job) error {
	if domain.ValidateIDs(job.ID, job.Kind, job.InputDigest, job.PolicyRevision) != nil || job.DueAt.IsZero() || len(job.InputRevisionIDs) > 50 {
		return domain.ErrInvalidInput
	}
	for _, id := range job.InputRevisionIDs {
		if domain.ValidateIDs(id) != nil {
			return domain.ErrInvalidInput
		}
	}
	return s.tx.Within(ctx, func(ctx context.Context, tx Tx) error { return s.repo.Enqueue(ctx, tx, job) })
}
func (s *JobsService) Claim(ctx context.Context, workerID string, kinds []string, now time.Time) (domain.Job, domain.Lease, error) {
	if domain.ValidateIDs(workerID) != nil || len(kinds) == 0 || len(kinds) > 50 || now.IsZero() {
		return domain.Job{}, domain.Lease{}, domain.ErrInvalidInput
	}
	for _, kind := range kinds {
		if domain.ValidateIDs(kind) != nil {
			return domain.Job{}, domain.Lease{}, domain.ErrInvalidInput
		}
	}
	now = now.UTC()
	var job domain.Job
	var lease domain.Lease
	empty := true
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		for range jobMaintenanceBatch {
			candidate, previous, err := s.repo.Next(ctx, tx, kinds, now)
			// No work still commits exhaustion maintenance; ErrIncomplete is returned
			// only outside the transaction so those transitions cannot roll back.
			if errors.Is(err, domain.ErrIncomplete) {
				return nil
			}
			if err != nil {
				return err
			}
			if candidate.Attempts >= JobMaxAttempts {
				reason := "attempts_exhausted"
				if candidate.Status == domain.JobRunning {
					reason = "lease_expired"
				}
				if err := s.repo.Exhaust(ctx, tx, previous, now, reason); err != nil {
					return err
				}
				continue
			}
			next := domain.Lease{JobID: candidate.ID, WorkerID: workerID, Fence: previous.Fence + 1, ExpiresAt: now.Add(JobLeaseTTL)}
			job, lease, err = s.repo.Claim(ctx, tx, next, now, now.Add(JobMaxRuntime))
			empty = false
			return err
		}
		return nil
	})
	if err != nil {
		return domain.Job{}, domain.Lease{}, err
	}
	if empty {
		return domain.Job{}, domain.Lease{}, domain.ErrIncomplete
	}
	return job, lease, nil
}
func validLease(lease domain.Lease) bool {
	return domain.ValidateIDs(lease.JobID, lease.WorkerID) == nil && lease.Fence > 0
}
func (s *JobsService) Complete(ctx context.Context, lease domain.Lease, expectedDigest string) error {
	if !validLease(lease) || domain.ValidateIDs(expectedDigest) != nil {
		return domain.ErrInvalidInput
	}
	return s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		return s.repo.Finish(ctx, tx, lease, expectedDigest, time.Now().UTC())
	})
}

// Commit guards business writes and the completion outbox with the same live
// fence and OwnerState lock. apply must perform repository writes only, never
// network I/O. Already-completed replays cannot invoke the business callback.
func (s *JobsService) Commit(ctx context.Context, lease domain.Lease, expectedDigest string, apply func(context.Context, Tx) error) error {
	if !validLease(lease) || domain.ValidateIDs(expectedDigest) != nil || apply == nil {
		return domain.ErrInvalidInput
	}
	return s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		if err := s.repo.CheckLease(ctx, tx, lease, expectedDigest, time.Now().UTC()); err != nil {
			return err
		}
		if err := apply(ctx, tx); err != nil {
			return err
		}
		return s.repo.Finish(ctx, tx, lease, expectedDigest, time.Now().UTC())
	})
}
func (s *JobsService) Heartbeat(ctx context.Context, lease domain.Lease) (domain.Lease, error) {
	if !validLease(lease) {
		return domain.Lease{}, domain.ErrInvalidInput
	}
	var renewed domain.Lease
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		now := time.Now().UTC()
		_, maximum, err := s.repo.Leased(ctx, tx, lease, now)
		if err != nil {
			return err
		}
		if !maximum.After(now) {
			return domain.ErrStale
		}
		expires := now.Add(JobLeaseTTL)
		if expires.After(maximum) {
			expires = maximum
		}
		renewed, err = s.repo.Heartbeat(ctx, tx, lease, expires, now)
		return err
	})
	return renewed, err
}

// Fail stores closed safe reason codes and chooses retry/exhaustion policy from
// authoritative job state under the same lock as its persistence transition.
func (s *JobsService) Fail(ctx context.Context, lease domain.Lease, retryAt time.Time, reasonCode string) error {
	if !validLease(lease) || retryAt.IsZero() {
		return domain.ErrInvalidInput
	}
	switch reasonCode {
	case "handler_failed", "unsupported_kind", "deadline_exceeded", "worker_stopped":
	default:
		return domain.ErrInvalidInput
	}
	return s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		now := time.Now().UTC()
		job, _, err := s.repo.Leased(ctx, tx, lease, now)
		if err != nil {
			return err
		}
		if job.Attempts < 1 || job.Attempts > JobMaxAttempts {
			return domain.ErrUnavailable
		}
		minimum := now.Add(time.Second * time.Duration(1<<uint(job.Attempts-1)))
		maximum := now.Add(jobMaxRetryDelay)
		if retryAt.Before(minimum) {
			retryAt = minimum
		}
		if retryAt.After(maximum) {
			retryAt = maximum
		}
		status := domain.JobQueued
		if job.Attempts >= JobMaxAttempts {
			status = domain.JobFailed
		}
		return s.repo.Fail(ctx, tx, lease, retryAt.UTC(), reasonCode, status, now)
	})
}
func (s *JobsService) Cancel(ctx context.Context, id string) error {
	if domain.ValidateIDs(id) != nil {
		return domain.ErrInvalidInput
	}
	return s.tx.Within(ctx, func(ctx context.Context, tx Tx) error { return s.repo.Cancel(ctx, tx, id) })
}
func (s *JobsService) Dead(ctx context.Context, limit int) ([]domain.Job, error) {
	if limit < 1 || limit > 100 {
		return nil, domain.ErrInvalidInput
	}
	var jobs []domain.Job
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		jobs, err = s.repo.List(ctx, tx, domain.JobFailed, limit)
		return err
	})
	return jobs, err
}

package service

import (
	"context"
	"errors"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"time"
)

const (
	CaptureAbandonAfter = 24 * time.Hour
	// A 20s Put leaves 40s settlement/crash margin before lease expiry.
	CaptureUploadLease        = time.Minute
	CapturePutTimeout         = 20 * time.Second
	CaptureObjectCheckTimeout = 5 * time.Second
)

func captureObjectContext(ctx context.Context) (context.Context, context.CancelFunc) {
	budget := CaptureObjectCheckTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline) / 2; remaining < budget {
			budget = remaining
		}
	}
	return context.WithTimeout(ctx, budget)
}

// Confirmed absence/corruption invalidates the same generation. A transport
// failure is an unknown observation and must preserve last-confirmed health.
func (s *CaptureService) verifyObject(ctx context.Context, a domain.Artifact) error {
	checkCtx, cancel := captureObjectContext(ctx)
	err := s.checkObject(checkCtx, a)
	cancel()
	if err == nil || !errors.Is(err, domain.ErrIncomplete) {
		return err
	}
	healthErr := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error { return s.repo.Availability(ctx, tx, a.ID, a.ObjectKey, false) })
	if healthErr != nil && !errors.Is(healthErr, domain.ErrStale) {
		return errors.Join(err, healthErr)
	}
	return err
}

// retire claims only expired unready generations. An abandoning generation is
// durable deletion intent; retries cannot reuse its key until Delete settles.
func (s *CaptureService) retire(ctx context.Context, snapshot domain.Artifact) error {
	var retiring domain.Artifact
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		a, client, err := s.repo.Artifact(ctx, tx, snapshot.ID)
		if err != nil {
			return err
		}
		if a.ObjectKey != snapshot.ObjectKey {
			return domain.ErrStale
		}
		if a.State == domain.ArtifactAbandoned {
			return nil
		}
		if a.State != domain.ArtifactAbandoning {
			if a.State != domain.ArtifactStaged || a.ExpiresAt == nil || a.ExpiresAt.After(time.Now().UTC()) || a.UploadLeaseUntil.After(time.Now().UTC()) {
				return domain.ErrIncomplete
			}
			a.State = domain.ArtifactAbandoning
			a.ObjectAvailable = false
			a.UploadLeaseID = ""
			a.UploadLeaseUntil = time.Time{}
			if err = s.repo.SaveArtifact(ctx, tx, a, client); err != nil {
				return err
			}
		}
		retiring = a
		return nil
	})
	if err != nil || retiring.ID == "" {
		return err
	}
	deleteCtx, cancel := captureObjectContext(ctx)
	err = s.objects.Delete(deleteCtx, retiring.ObjectKey)
	cancel()
	// Always advance failed cleanup attempts using the parent budget; the durable
	// abandoning state keeps them retryable without blocking later audit records.
	settleErr := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		a, client, loadErr := s.repo.Artifact(ctx, tx, retiring.ID)
		if loadErr != nil {
			return loadErr
		}
		if a.ObjectKey != retiring.ObjectKey || a.State != domain.ArtifactAbandoning {
			return domain.ErrStale
		}
		if err == nil {
			a.State = domain.ArtifactAbandoned
			if loadErr = s.repo.SaveArtifact(ctx, tx, a, client); loadErr != nil {
				return loadErr
			}
		}
		return s.repo.Checked(ctx, tx, a.ID, a.ObjectKey, time.Now().UTC().UnixNano())
	})
	return errors.Join(err, settleErr)
}

func (s *CaptureService) Reconcile(ctx context.Context, job domain.Job, lease domain.Lease, jobs interface {
	Commit(context.Context, domain.Lease, string, func(context.Context, Tx) error) error
}) error {
	if job.Kind != "capture.reconcile" || len(job.InputRevisionIDs) != 2 {
		return domain.ErrInvalidInput
	}
	var a domain.Artifact
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		a, _, err = s.repo.Artifact(ctx, tx, job.InputRevisionIDs[0])
		if err != nil {
			return err
		}
		if job.InputRevisionIDs[1] != a.Generation || job.PolicyRevision != a.PolicyRevision || job.InputDigest != captureID(a.ID, a.Digest, a.Generation) || job.Scope != a.Scope {
			return domain.ErrConflict
		}
		// Cleanup may retire expired inaccessible bytes; it never publishes under a
		// stale/paused policy. All publication paths still check current policy.
		if a.State == domain.ArtifactDeleted || a.State == domain.ArtifactAbandoned || a.State == domain.ArtifactAbandoning || (a.State == domain.ArtifactStaged && a.ExpiresAt != nil && !a.ExpiresAt.After(time.Now().UTC())) {
			return nil
		}
		return s.policy(ctx, tx, a)
	})
	if err != nil {
		return err
	}
	if a.State == domain.ArtifactDeleted || a.State == domain.ArtifactAbandoned {
		return nil
	}
	if a.State == domain.ArtifactAbandoning || (a.State == domain.ArtifactStaged && a.ExpiresAt != nil && !a.ExpiresAt.After(time.Now().UTC())) {
		return s.retire(ctx, a)
	}
	if a.State == domain.ArtifactStaged && a.UploadLeaseUntil.After(time.Now().UTC()) {
		return domain.ErrIncomplete
	}
	if err = s.verifyObject(ctx, a); err != nil {
		return err
	}
	return jobs.Commit(ctx, lease, job.InputDigest, func(ctx context.Context, tx Tx) error { _, err := s.publish(ctx, tx, a); return err })
}

func (s *CaptureService) Sweep(ctx context.Context, limit int) error {
	if limit < 1 || limit > 100 {
		return domain.ErrInvalidInput
	}
	var artifacts []domain.Artifact
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		artifacts, err = s.repo.Pending(ctx, tx, limit)
		return err
	})
	if err != nil {
		return err
	}
	var failures error
	for _, snapshot := range artifacts {
		if ctx.Err() != nil {
			return errors.Join(failures, ctx.Err())
		}
		a := snapshot
		if a.State == domain.ArtifactAbandoning || (a.State == domain.ArtifactStaged && a.ExpiresAt != nil && !a.ExpiresAt.After(time.Now().UTC())) {
			failures = errors.Join(failures, s.retire(ctx, a))
			continue
		}
		// A crashed Put lease expires on its own. Never audit/publish a live upload.
		if a.State == domain.ArtifactStaged && a.UploadLeaseUntil.After(time.Now().UTC()) {
			err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
				return s.repo.Checked(ctx, tx, a.ID, a.ObjectKey, time.Now().UTC().UnixNano())
			})
			failures = errors.Join(failures, err)
			continue
		}
		checkCtx, cancel := captureObjectContext(ctx)
		checkErr := s.checkObject(checkCtx, a)
		cancel()
		err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
			if checkErr == nil || errors.Is(checkErr, domain.ErrIncomplete) {
				if err := s.repo.Availability(ctx, tx, a.ID, a.ObjectKey, checkErr == nil); err != nil {
					return err
				}
			}
			return s.repo.Checked(ctx, tx, a.ID, a.ObjectKey, time.Now().UTC().UnixNano())
		})
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		if checkErr != nil {
			failures = errors.Join(failures, checkErr)
			continue
		}
		if a.State == domain.ArtifactStaged {
			err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error { _, err := s.publish(ctx, tx, a); return err })
			failures = errors.Join(failures, err)
		}
	}
	return failures
}

package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
)

// The real graph cases cover persistence. These cases catch validation that
// would otherwise let payload-like errors and malformed leases reach storage.
func TestJobsRejectUnsafeInputs(t *testing.T) {
	for _, tc := range []struct {
		name string
		act  func(*service.JobsService) error
	}{
		{"empty enqueue", func(s *service.JobsService) error { return s.Enqueue(context.Background(), domain.Job{}) }},
		{"empty worker", func(s *service.JobsService) error {
			_, _, err := s.Claim(context.Background(), "", []string{"verify"}, time.Now())
			return err
		}},
		{"empty kinds", func(s *service.JobsService) error {
			_, _, err := s.Claim(context.Background(), "worker", nil, time.Now())
			return err
		}},
		{"unsafe failure", func(s *service.JobsService) error {
			return s.Fail(context.Background(), domain.Lease{JobID: "job", WorkerID: "worker", Fence: 1}, time.Now(), "raw secret prompt")
		}},
		{"empty completion digest", func(s *service.JobsService) error { return s.Complete(context.Background(), domain.Lease{}, "") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.act(service.NewJobsService(nil, nil))
			assert.ErrorIs(t, err, domain.ErrInvalidInput)
		})
	}
}

func TestJobsRejectInvalidLeaseOperations(t *testing.T) {
	for _, tc := range []struct {
		name string
		act  func(*service.JobsService) error
	}{
		{"commit cannot have empty lease", func(s *service.JobsService) error {
			return s.Commit(context.Background(), domain.Lease{}, "digest", func(context.Context, service.Tx) error { return nil })
		}},
		{"commit requires callback", func(s *service.JobsService) error {
			return s.Commit(context.Background(), domain.Lease{JobID: "j", WorkerID: "w", Fence: 1}, "digest", nil)
		}},
		{"heartbeat requires lease", func(s *service.JobsService) error {
			_, err := s.Heartbeat(context.Background(), domain.Lease{})
			return err
		}},
		{"cancel requires id", func(s *service.JobsService) error { return s.Cancel(context.Background(), "") }},
		{"dead inspection is bounded", func(s *service.JobsService) error { _, err := s.Dead(context.Background(), 101); return err }},
		{"claim rejects blank kind", func(s *service.JobsService) error {
			_, _, err := s.Claim(context.Background(), "worker", []string{""}, time.Now())
			return err
		}},
		{"enqueue rejects empty revision", func(s *service.JobsService) error {
			return s.Enqueue(context.Background(), domain.Job{ID: "j", Kind: "kind", InputDigest: "digest", PolicyRevision: "p", DueAt: time.Now(), InputRevisionIDs: []string{""}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.act(service.NewJobsService(nil, nil))
			assert.ErrorIs(t, err, domain.ErrInvalidInput)
		})
	}
}

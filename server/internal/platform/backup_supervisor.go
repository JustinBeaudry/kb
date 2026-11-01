package platform

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

type BackupScheduleStore interface {
	LoadSchedule(context.Context) (domain.BackupSchedule, error)
	SaveSchedule(context.Context, domain.BackupSchedule) error
}
type ScheduledBackup interface {
	Run(context.Context, string) (domain.BackupResult, error)
}
type SupervisorMaintenance interface {
	State(context.Context) (domain.MaintenanceState, error)
}
type BackupSupervisor struct {
	mu          sync.Mutex
	store       BackupScheduleStore
	backup      ScheduledBackup
	maintenance SupervisorMaintenance
	now         func() time.Time
}

func NewBackupSupervisor(store BackupScheduleStore, backup ScheduledBackup, maintenance SupervisorMaintenance, now func() time.Time) (*BackupSupervisor, error) {
	if store == nil || backup == nil || maintenance == nil {
		return nil, domain.ErrInvalidInput
	}
	if now == nil {
		now = time.Now
	}
	return &BackupSupervisor{store: store, backup: backup, maintenance: maintenance, now: now}, nil
}

// Tick runs one due backup. A 20h interval leaves completion margin; actual
// backup age is authoritative, and outages can still exceed the 24h RPO target.
func (s *BackupSupervisor) Tick(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	m, err := s.maintenance.State(ctx)
	if err != nil {
		return err
	}
	if m.Phase != "open" || m.Active < 0 || m.Unknown != 0 {
		return domain.ErrMaintenance
	}
	state, err := s.store.LoadSchedule(ctx)
	if err != nil {
		return err
	}
	if m.LastSuccessfulBackup.After(state.LastSnapshot) {
		state.LastSnapshot = m.LastSuccessfulBackup
	}
	due := state.LastSnapshot.Add(20 * time.Hour)
	if state.LastSnapshot.IsZero() {
		due = now
	}
	if state.NextAttempt.After(due) {
		due = state.NextAttempt
	}
	if now.Before(due) {
		return nil
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return domain.ErrUnavailable
	}
	state.AttemptID = "scheduled-" + hex.EncodeToString(random[:])
	state.LastAttempt = now
	state.NextAttempt = now.Add(5 * time.Minute)
	if err = s.store.SaveSchedule(ctx, state); err != nil {
		return err
	}
	result, runErr := s.backup.Run(ctx, state.AttemptID)
	if runErr != nil {
		state.Failures++
		backoff := 5 * time.Minute
		for range min(state.Failures-1, 6) {
			backoff *= 2
		}
		state.NextAttempt = now.Add(backoff)
	} else {
		state.LastSnapshot = result.StartedAt
		state.Failures = 0
		state.NextAttempt = result.StartedAt.Add(20 * time.Hour)
	}
	saveErr := s.store.SaveSchedule(context.WithoutCancel(ctx), state)
	if saveErr != nil {
		return saveErr
	}
	return runErr
}

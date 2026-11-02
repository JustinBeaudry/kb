package platform

import (
	"context"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scheduleFixture struct {
	state       domain.BackupSchedule
	maintenance domain.MaintenanceState
	runs        int
	failure     bool
}

func (f *scheduleFixture) LoadSchedule(context.Context) (domain.BackupSchedule, error) {
	return f.state, nil
}
func (f *scheduleFixture) SaveSchedule(_ context.Context, s domain.BackupSchedule) error {
	f.state = s
	return nil
}
func (f *scheduleFixture) State(context.Context) (domain.MaintenanceState, error) {
	return f.maintenance, nil
}
func (f *scheduleFixture) Run(_ context.Context, id string) (domain.BackupResult, error) {
	f.runs++
	if f.failure {
		return domain.BackupResult{}, domain.ErrUnavailable
	}
	return domain.BackupResult{ID: id, StartedAt: f.state.LastAttempt, CompletedAt: f.state.LastAttempt.Add(time.Minute), State: "complete"}, nil
}
func TestBackupSchedulerCatchesUpAtTwentyHoursAndPersistsRetry(t *testing.T) {
	now := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	f := &scheduleFixture{maintenance: domain.MaintenanceState{Phase: "open", LastSuccessfulBackup: now.Add(-20 * time.Hour)}, failure: true}
	s, e := NewBackupSupervisor(f, f, f, func() time.Time { return now })
	require.NoError(t, e)
	require.Error(t, s.Tick(t.Context()))
	assert.Equal(t, 1, f.runs)
	assert.Equal(t, uint64(1), f.state.Failures)
	assert.True(t, f.state.NextAttempt.After(now))
	assert.NotEmpty(t, f.state.AttemptID)
	require.NoError(t, s.Tick(t.Context()))
	assert.Equal(t, 1, f.runs)
	now = f.state.NextAttempt
	f.failure = false
	require.NoError(t, s.Tick(t.Context()))
	assert.Equal(t, 2, f.runs)
	assert.Equal(t, now, f.state.LastSnapshot)
	assert.Equal(t, now.Add(20*time.Hour), f.state.NextAttempt)
}
func TestBackupSchedulerNeverUsesTimeoutToClearUnknownPermits(t *testing.T) {
	f := &scheduleFixture{maintenance: domain.MaintenanceState{Phase: "failed", Unknown: 1}}
	s, e := NewBackupSupervisor(f, f, f, time.Now)
	require.NoError(t, e)
	require.ErrorIs(t, s.Tick(t.Context()), domain.ErrMaintenance)
	assert.Zero(t, f.runs)
}

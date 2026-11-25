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

func TestBackupSchedulerReportsRecoveryPointGapOnlyForCompletedBackups(t *testing.T) {
	now := time.Date(2026, 11, 24, 20, 0, 0, 0, time.UTC)
	previous := now.Add(-21 * time.Hour)
	f := &scheduleFixture{maintenance: domain.MaintenanceState{Phase: "open", LastSuccessfulBackup: previous}, failure: true}
	s, e := NewBackupSupervisor(f, f, f, func() time.Time { return now })
	require.NoError(t, e)
	var reports []BackupEvidence
	s.OnComplete(func(r BackupEvidence) { reports = append(reports, r) })

	require.Error(t, s.Tick(t.Context()))
	assert.Empty(t, reports, "a failed backup is not evidence of a recovery point")

	now = f.state.NextAttempt
	f.failure = false
	require.NoError(t, s.Tick(t.Context()))
	require.Len(t, reports, 1)
	assert.Equal(t, now.Sub(previous), reports[0].RecoveryPointGap)
	assert.Equal(t, time.Minute, reports[0].Duration)

	require.NoError(t, s.Tick(t.Context()))
	assert.Len(t, reports, 1, "a tick that runs no backup reports nothing")
}

func TestBackupSchedulerFirstBackupHasNoPreviousRecoveryPoint(t *testing.T) {
	now := time.Date(2026, 11, 24, 20, 0, 0, 0, time.UTC)
	f := &scheduleFixture{maintenance: domain.MaintenanceState{Phase: "open"}}
	s, e := NewBackupSupervisor(f, f, f, func() time.Time { return now })
	require.NoError(t, e)
	var reports []BackupEvidence
	s.OnComplete(func(r BackupEvidence) { reports = append(reports, r) })
	require.NoError(t, s.Tick(t.Context()))
	require.Len(t, reports, 1)
	assert.Zero(t, reports[0].RecoveryPointGap)
	assert.True(t, reports[0].First)
}

type failingSaveStore struct {
	*scheduleFixture
	saves int
}

func (f *failingSaveStore) SaveSchedule(ctx context.Context, s domain.BackupSchedule) error {
	f.saves++
	if f.saves > 1 {
		return domain.ErrUnavailable
	}
	return f.scheduleFixture.SaveSchedule(ctx, s)
}

func TestBackupSchedulerSeparatesUnsavedScheduleFromFailedBackup(t *testing.T) {
	now := time.Date(2026, 11, 24, 20, 0, 0, 0, time.UTC)
	f := &scheduleFixture{maintenance: domain.MaintenanceState{Phase: "open", LastSuccessfulBackup: now.Add(-21 * time.Hour)}}
	store := &failingSaveStore{scheduleFixture: f}
	s, e := NewBackupSupervisor(store, f, f, func() time.Time { return now })
	require.NoError(t, e)
	var reports []BackupEvidence
	s.OnComplete(func(r BackupEvidence) { reports = append(reports, r) })
	err := s.Tick(t.Context())
	require.ErrorIs(t, err, ErrScheduleNotSaved, "the backup ran; only the schedule state failed")
	assert.Len(t, reports, 1, "a completed backup is still a recovery point")
}

func TestObserveManualBackupReportsGapFromLastSuccessfulBackup(t *testing.T) {
	now := time.Date(2026, 11, 24, 20, 0, 0, 0, time.UTC)
	f := &scheduleFixture{maintenance: domain.MaintenanceState{Phase: "open", LastSuccessfulBackup: now.Add(-19 * time.Hour)}}
	f.state.LastAttempt = now
	var reports []BackupEvidence
	_, err := ObserveManualBackup(t.Context(), f, func(ctx context.Context) (domain.BackupResult, error) { return f.Run(ctx, "manual-1") }, func(r BackupEvidence) { reports = append(reports, r) })
	require.NoError(t, err)
	require.Len(t, reports, 1)
	assert.Equal(t, 19*time.Hour, reports[0].RecoveryPointGap)

	f.failure = true
	_, err = ObserveManualBackup(t.Context(), f, func(ctx context.Context) (domain.BackupResult, error) { return f.Run(ctx, "manual-2") }, func(r BackupEvidence) { reports = append(reports, r) })
	require.Error(t, err)
	assert.Len(t, reports, 1, "a failed manual backup is not a recovery point")
}

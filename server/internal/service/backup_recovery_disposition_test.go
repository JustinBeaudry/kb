package service

import (
	"context"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failRecoveredCheckpoint struct {
	f      *backupFixture
	failed bool
}

func (s *failRecoveredCheckpoint) LoadOperation(ctx context.Context) (domain.BackupOperation, error) {
	return s.f.LoadOperation(ctx)
}
func (s *failRecoveredCheckpoint) SaveOperation(ctx context.Context, op domain.BackupOperation) error {
	if op.Stage == "recovered" && !s.failed {
		s.failed = true
		return domain.ErrUnavailable
	}
	return s.f.SaveOperation(ctx, op)
}
func failedRecoveryService(t *testing.T, f *backupFixture, store BackupOperationStore, m BackupMaintenance) *BackupService {
	t.Helper()
	s, err := NewBackupService(m, f, f, f, store, BackupConfig{ScratchRoot: t.TempDir(), EvidenceNamespace: "fresh", InstanceID: "fixture"}, time.Now)
	require.NoError(t, err)
	return s
}
func failedBackupOperation() domain.BackupOperation {
	return domain.BackupOperation{Version: 1, ID: "missing-archive", BackupID: "missing-archive", Kind: "backup", Stage: "failed", Fence: domain.MaintenanceFence{OperationID: "op", Generation: 2}, StartedAt: time.Now().UTC()}
}
func TestAbandonedBackupCannotBecomeCompleteAfterLostRecoveredCheckpoint(t *testing.T) {
	f := &backupFixture{state: failedBackupOperation()}
	m := checkpointMaintenance{f, domain.MaintenanceState{Phase: "open", Fence: f.state.Fence}}
	store := &failRecoveredCheckpoint{f: f}
	s := failedRecoveryService(t, f, store, m)
	require.Error(t, s.Recover(t.Context(), "missing-archive"))
	assert.Equal(t, "recovered", f.state.TerminalState)
	_, err := s.Run(t.Context(), "missing-archive")
	require.ErrorIs(t, err, domain.ErrConflict)
	assert.Equal(t, "recovered", f.state.Stage)
	require.NoError(t, s.Recover(t.Context(), "missing-archive"))
	starts := 0
	for _, call := range f.calls {
		if call == "start" {
			starts++
		}
	}
	assert.Equal(t, 1, starts)
	assert.NotContains(t, f.calls, "write")
	assert.NotContains(t, f.calls, "record")
}
func TestStartupPreservesAbandonedBackupDisposition(t *testing.T) {
	f := &backupFixture{state: failedBackupOperation()}
	store := &failRecoveredCheckpoint{f: f}
	first := failedRecoveryService(t, f, store, checkpointMaintenance{f, domain.MaintenanceState{Phase: "open", Fence: f.state.Fence}})
	require.Error(t, first.Recover(t.Context(), "missing-archive"))
	restarted := failedRecoveryService(t, f, store, checkpointMaintenance{f, domain.MaintenanceState{Phase: "open", Fence: domain.MaintenanceFence{OperationID: "startup-after-crash", Generation: 3}}})
	require.NoError(t, restarted.CheckpointStartup(t.Context()))
	assert.Equal(t, "recovered", f.state.Stage)
	require.NoError(t, restarted.Recover(t.Context(), "missing-archive"))
	_, err := restarted.Run(t.Context(), "missing-archive")
	require.ErrorIs(t, err, domain.ErrConflict)
	assert.NotContains(t, f.calls, "write")
	assert.NotContains(t, f.calls, "record")
}

func TestExactRecoverRetryOnlyFinalizesItsRecoveredDisposition(t *testing.T) {
	f := &backupFixture{state: failedBackupOperation()}
	store := &failRecoveredCheckpoint{f: f}
	s := failedRecoveryService(t, f, store, checkpointMaintenance{f, domain.MaintenanceState{Phase: "open", Fence: f.state.Fence}})
	require.Error(t, s.Recover(t.Context(), "missing-archive"))
	assert.Equal(t, "resuming", f.state.Stage)
	assert.Equal(t, "recovered", f.state.TerminalState)
	require.NoError(t, s.Recover(t.Context(), "missing-archive"))
	assert.Equal(t, "recovered", f.state.Stage)
	starts := 0
	for _, call := range f.calls {
		if call == "start" {
			starts++
		}
	}
	assert.Equal(t, 1, starts)
	_, err := s.Run(t.Context(), "missing-archive")
	require.ErrorIs(t, err, domain.ErrConflict)
}

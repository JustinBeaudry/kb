package service

import (
	"context"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failCompletedCheckpoint struct {
	f      *backupFixture
	failed bool
}

func (s *failCompletedCheckpoint) LoadOperation(ctx context.Context) (domain.BackupOperation, error) {
	return s.f.LoadOperation(ctx)
}
func (s *failCompletedCheckpoint) SaveOperation(ctx context.Context, op domain.BackupOperation) error {
	if op.Stage == "complete" && !s.failed {
		s.failed = true
		return domain.ErrUnavailable
	}
	return s.f.SaveOperation(ctx, op)
}

type checkpointMaintenance struct {
	*backupFixture
	maintenance domain.MaintenanceState
}

func (m checkpointMaintenance) State(context.Context) (domain.MaintenanceState, error) {
	return m.maintenance, nil
}
func TestLostFinalCheckpointDoesNotRepeatCompletedBackupEffects(t *testing.T) {
	f := &backupFixture{}
	m := checkpointMaintenance{f, domain.MaintenanceState{Phase: "open", Fence: domain.MaintenanceFence{OperationID: "op", Generation: 2}}}
	store := &failCompletedCheckpoint{f: f}
	s, e := NewBackupService(m, f, f, f, store, BackupConfig{ScratchRoot: t.TempDir(), EvidenceNamespace: "fresh", InstanceID: "fixture"}, time.Now)
	require.NoError(t, e)
	_, e = s.Run(t.Context(), "backup")
	require.Error(t, e)
	result, e := s.Run(t.Context(), "backup")
	require.NoError(t, e)
	assert.Equal(t, "complete", result.State)
	begins := 0
	for _, call := range f.calls {
		if call == "begin" {
			begins++
		}
	}
	assert.Equal(t, 1, begins)
}
func TestStartupReconcilesOnlyPreviouslyResumedOperation(t *testing.T) {
	f := &backupFixture{state: domain.BackupOperation{Version: 1, ID: "backup", BackupID: "backup", Kind: "backup", Stage: "resuming", TerminalState: "complete", Fence: domain.MaintenanceFence{OperationID: "backup", Generation: 3}, StartedAt: time.Now().UTC()}}
	m := checkpointMaintenance{f, domain.MaintenanceState{Phase: "open", Fence: domain.MaintenanceFence{OperationID: "startup-new", Generation: 5}}}
	s, e := NewBackupService(m, f, f, f, f, BackupConfig{ScratchRoot: t.TempDir(), EvidenceNamespace: "fresh", InstanceID: "fixture"}, time.Now)
	require.NoError(t, e)
	require.NoError(t, s.CheckpointStartup(t.Context()))
	assert.Equal(t, "complete", f.state.Stage)
	assert.Equal(t, []string{"persist:complete"}, f.calls)
}

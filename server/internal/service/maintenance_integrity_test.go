package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These fixtures are persisted-state corruption, rather than requests which a
// correctly running service can generate. They must never become admission.
func TestMaintenanceIntegrityRejectsUnusableDurableAuthority(t *testing.T) {
	valid := domain.MaintenanceState{Phase: "recovering", Fence: domain.MaintenanceFence{OperationID: "restore", Generation: 3}}
	permit := domain.OperationPermit{ID: "operation", InstanceID: "worker", Kind: "worker", Generation: 2}
	for _, test := range []struct {
		name    string
		state   domain.MaintenanceState
		permits []domain.OperationPermit
	}{
		{"unknown-phase", domain.MaintenanceState{Phase: "corrupt", Fence: valid.Fence}, nil},
		{"missing-fence-id", domain.MaintenanceState{Phase: "recovering", Fence: domain.MaintenanceFence{Generation: 3}}, nil},
		{"zero-fence-generation", domain.MaintenanceState{Phase: "recovering", Fence: domain.MaintenanceFence{OperationID: "restore"}}, nil},
		{"duplicate-permit-id", valid, []domain.OperationPermit{permit, permit}},
		{"missing-permit-id", valid, []domain.OperationPermit{{InstanceID: "worker", Kind: "worker", Generation: 2}}},
		{"missing-permit-instance", valid, []domain.OperationPermit{{ID: "operation", Kind: "worker", Generation: 2}}},
		{"missing-permit-kind", valid, []domain.OperationPermit{{ID: "operation", InstanceID: "worker", Generation: 2}}},
		{"zero-permit-generation", valid, []domain.OperationPermit{{ID: "operation", InstanceID: "worker", Kind: "worker"}}},
		{"startup-fence-overflow", domain.MaintenanceState{Phase: "open", Fence: domain.MaintenanceFence{OperationID: "previous", Generation: ^uint64(0)}}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &maintenanceStore{state: test.state, permits: test.permits}
			s, err := service.NewMaintenanceService(context.Background(), store)
			require.ErrorIs(t, err, domain.ErrUnavailable)
			assert.Nil(t, s)
		})
	}
}

func TestMaintenanceIntegrityForgedIdentityCannotFinishOrInvalidateAnotherPermit(t *testing.T) {
	ctx := context.Background()
	s, _ := newMaintenance(t)
	openMaintenance(t, s)
	permit, err := s.Admit(ctx, "api", "upload")
	require.NoError(t, err)
	for _, test := range []struct {
		name   string
		mutate func(*domain.OperationPermit)
	}{
		{"generation", func(p *domain.OperationPermit) { p.Generation++ }},
		{"kind", func(p *domain.OperationPermit) { p.Kind = "delete" }},
		{"instance", func(p *domain.OperationPermit) { p.InstanceID = "replacement-instance" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			forged := permit
			test.mutate(&forged)
			require.ErrorIs(t, s.Finish(ctx, forged), domain.ErrStale)
			require.ErrorIs(t, s.MarkUnknown(ctx, forged), domain.ErrStale)
			require.ErrorIs(t, s.ValidatePermit(ctx, forged), domain.ErrMaintenance)
			require.NoError(t, s.ValidatePermit(ctx, permit))
			state, err := s.State(ctx)
			require.NoError(t, err)
			assert.Equal(t, 1, state.Active)
			assert.Zero(t, state.Unknown)
		})
	}
	require.NoError(t, s.Finish(ctx, permit))
	require.ErrorIs(t, s.ValidatePermit(ctx, permit), domain.ErrMaintenance)
}

func TestMaintenanceIntegrityOldFenceCannotAdvanceAReplacementBackup(t *testing.T) {
	ctx := context.Background()
	s, _ := newMaintenance(t)
	openMaintenance(t, s)
	old, err := s.Begin(ctx, "backup-one")
	require.NoError(t, err)
	require.NoError(t, s.SetPhase(ctx, old, "recovering"))
	require.NoError(t, s.MarkRecoveryComplete(ctx, old))
	require.NoError(t, s.Resume(ctx, old))
	current, err := s.Begin(ctx, "backup-two")
	require.NoError(t, err)
	repeat, err := s.Begin(ctx, "backup-two")
	require.NoError(t, err)
	assert.Equal(t, current, repeat)
	_, err = s.Begin(ctx, "different-backup")
	require.ErrorIs(t, err, domain.ErrConflict)
	for _, test := range []struct {
		name    string
		attempt func() error
	}{
		{"drain", func() error { return s.WaitDrained(ctx, old) }},
		{"phase", func() error { return s.SetPhase(ctx, old, "offline") }},
		{"recovery-proof", func() error { return s.MarkRecoveryComplete(ctx, old) }},
		{"resume", func() error { return s.Resume(ctx, old) }},
		{"backup-metadata", func() error { return s.RecordBackup(ctx, old, "stale-backup", time.Now()) }},
	} {
		t.Run(test.name, func(t *testing.T) { require.ErrorIs(t, test.attempt(), domain.ErrStale) })
	}
	state, err := s.State(ctx)
	require.NoError(t, err)
	assert.Equal(t, current, state.Fence)
	assert.Equal(t, "draining", state.Phase)
	assert.Empty(t, state.BackupID)
}

func TestMaintenanceIntegrityResumePersistenceFailureLatchesAuthorityClosed(t *testing.T) {
	ctx := context.Background()
	s, store := newMaintenance(t)
	state, err := s.State(ctx)
	require.NoError(t, err)
	require.NoError(t, s.SetPhase(ctx, state.Fence, "recovering"))
	require.NoError(t, s.MarkRecoveryComplete(ctx, state.Fence))
	store.fail = true
	require.ErrorIs(t, s.Resume(ctx, state.Fence), domain.ErrUnavailable)
	store.fail = false
	require.ErrorIs(t, s.Resume(ctx, state.Fence), domain.ErrMaintenance)
	require.ErrorIs(t, s.SetPhase(ctx, state.Fence, "offline"), domain.ErrMaintenance)
	require.ErrorIs(t, s.MarkRecoveryComplete(ctx, state.Fence), domain.ErrMaintenance)
	require.ErrorIs(t, s.WaitDrained(ctx, state.Fence), domain.ErrMaintenance)
	_, err = s.Begin(ctx, "new-backup")
	require.ErrorIs(t, err, domain.ErrMaintenance)
	restarted, err := service.NewMaintenanceService(ctx, store)
	require.NoError(t, err)
	persisted, err := restarted.State(ctx)
	require.NoError(t, err)
	assert.Equal(t, "recovering", persisted.Phase)
	assert.False(t, persisted.RecoveryComplete)
	require.ErrorIs(t, restarted.Resume(ctx, persisted.Fence), domain.ErrMaintenance)
}

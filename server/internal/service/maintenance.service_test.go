package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type maintenanceStore struct {
	state   domain.MaintenanceState
	permits []domain.OperationPermit
	fail    bool
}

func (s *maintenanceStore) Load(context.Context) (domain.MaintenanceState, []domain.OperationPermit, error) {
	return s.state, append([]domain.OperationPermit(nil), s.permits...), nil
}
func (s *maintenanceStore) Save(_ context.Context, state domain.MaintenanceState, p []domain.OperationPermit) error {
	if s.fail {
		return domain.ErrUnavailable
	}
	s.state = state
	s.permits = append([]domain.OperationPermit(nil), p...)
	return nil
}
func newMaintenance(t *testing.T) (*service.MaintenanceService, *maintenanceStore) {
	t.Helper()
	store := &maintenanceStore{state: domain.MaintenanceState{Phase: "bootstrap", Fence: domain.MaintenanceFence{OperationID: "bootstrap", Generation: 1}}}
	s, err := service.NewMaintenanceService(context.Background(), store)
	require.NoError(t, err)
	return s, store
}
func openMaintenance(t *testing.T, s *service.MaintenanceService) {
	t.Helper()
	ctx := context.Background()
	state, err := s.State(ctx)
	require.NoError(t, err)
	require.NoError(t, s.SetPhase(ctx, state.Fence, "recovering"))
	require.NoError(t, s.MarkRecoveryComplete(ctx, state.Fence))
	require.NoError(t, s.Resume(ctx, state.Fence))
}

func TestMaintenanceDrainHoldsCanceledOperationUntilExactFinish(t *testing.T) {
	ctx := context.Background()
	s, _ := newMaintenance(t)
	openMaintenance(t, s)
	opctx, cancel := context.WithCancel(ctx)
	p, err := s.Admit(opctx, "api-one", "api")
	require.NoError(t, err)
	cancel()
	fence, err := s.Begin(ctx, "backup-one")
	require.NoError(t, err)
	_, err = s.Admit(ctx, "api-two", "api")
	require.ErrorIs(t, err, domain.ErrMaintenance)
	waitctx, cancelWait := context.WithTimeout(ctx, 5*time.Millisecond)
	t.Cleanup(cancelWait)
	require.ErrorIs(t, s.WaitDrained(waitctx, fence), context.DeadlineExceeded)
	wrong := p
	wrong.InstanceID = "other"
	require.ErrorIs(t, s.Finish(ctx, wrong), domain.ErrStale)
	require.NoError(t, s.Finish(ctx, p))
	require.NoError(t, s.Finish(ctx, p))
	require.NoError(t, s.WaitDrained(ctx, fence))
	require.ErrorIs(t, s.Resume(ctx, fence), domain.ErrMaintenance)
	require.NoError(t, s.SetPhase(ctx, fence, "offline"))
	require.ErrorIs(t, s.MarkRecoveryComplete(ctx, fence), domain.ErrMaintenance)
	require.NoError(t, s.SetPhase(ctx, fence, "recovering"))
	require.NoError(t, s.MarkRecoveryComplete(ctx, fence))
	require.NoError(t, s.Resume(ctx, fence))
}
func TestMaintenanceRestartPersistsUnknownAndCannotForgetIt(t *testing.T) {
	ctx := context.Background()
	s, store := newMaintenance(t)
	openMaintenance(t, s)
	p, err := s.Admit(ctx, "old", "worker")
	require.NoError(t, err)
	s, err = service.NewMaintenanceService(ctx, store)
	require.NoError(t, err)
	st, err := s.State(ctx)
	require.NoError(t, err)
	assert.Equal(t, "recovering", st.Phase)
	assert.Equal(t, 1, st.Unknown)
	assert.Equal(t, 0, st.Active)
	require.ErrorIs(t, s.Finish(ctx, p), domain.ErrUnknownOutcome)
	require.ErrorIs(t, s.MarkRecoveryComplete(ctx, st.Fence), domain.ErrMaintenance)
	assert.True(t, store.permits[0].Unknown)
}
func TestMaintenancePersistenceFailureNeverGrantsOrReopens(t *testing.T) {
	ctx := context.Background()
	s, store := newMaintenance(t)
	openMaintenance(t, s)
	store.fail = true
	_, err := s.Admit(ctx, "api", "api")
	require.Error(t, err)
	store.fail = false
	_, err = s.Admit(ctx, "api", "api")
	require.ErrorIs(t, err, domain.ErrMaintenance)
	st, _ := s.State(ctx)
	assert.Equal(t, "failed", st.Phase)
}
func TestMaintenanceRecoveryAuthorityIsLocalAndSerial(t *testing.T) {
	ctx := context.Background()
	s, _ := newMaintenance(t)
	st, _ := s.State(ctx)
	_, err := s.Admit(ctx, "service", "host-admin:setup")
	require.ErrorIs(t, err, domain.ErrForbidden)
	_, err = s.AdmitRecovery(ctx, st.Fence, "host", "arbitrary")
	require.ErrorIs(t, err, domain.ErrForbidden)
	p, err := s.AdmitRecovery(ctx, st.Fence, "host", "host-admin:setup")
	require.NoError(t, err)
	_, err = s.AdmitRecovery(ctx, st.Fence, "host", "host-admin:journal-init")
	require.ErrorIs(t, err, domain.ErrMaintenance)
	require.NoError(t, s.Finish(ctx, p))
	bad := st.Fence
	bad.Generation++
	_, err = s.AdmitRecovery(ctx, bad, "host", "startup-recovery")
	require.ErrorIs(t, err, domain.ErrStale)
}
func TestMaintenanceBeginAdmitRaceHasNoUnregisteredSuccess(t *testing.T) {
	ctx := context.Background()
	for range 40 {
		s, _ := newMaintenance(t)
		openMaintenance(t, s)
		var wg sync.WaitGroup
		wg.Add(2)
		var p domain.OperationPermit
		var admitErr, errorBegin error
		go func() { defer wg.Done(); p, admitErr = s.Admit(ctx, "api", "api") }()
		go func() { defer wg.Done(); _, errorBegin = s.Begin(ctx, "backup") }()
		wg.Wait()
		require.NoError(t, errorBegin)
		st, _ := s.State(ctx)
		assert.Equal(t, "draining", st.Phase)
		if admitErr == nil {
			assert.Equal(t, 1, st.Active)
			require.NoError(t, s.Finish(ctx, p))
		} else {
			assert.True(t, errors.Is(admitErr, domain.ErrMaintenance))
			assert.Zero(t, st.Active)
		}
	}
}
func TestMaintenanceExplicitRestoreCanMoveRecoveringOfflineOnlyWhenDrained(t *testing.T) {
	for _, kind := range []string{"drained", "active", "unknown", "stale"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			s, _ := newMaintenance(t)
			state, err := s.State(ctx)
			require.NoError(t, err)
			require.NoError(t, s.SetPhase(ctx, state.Fence, "recovering"))
			fence := state.Fence
			switch kind {
			case "active", "unknown":
				permit, err := s.AdmitRecovery(ctx, fence, "restore", "restore-recovery")
				require.NoError(t, err)
				if kind == "unknown" {
					require.NoError(t, s.MarkUnknown(ctx, permit))
				}
			case "stale":
				fence.Generation++
			}
			err = s.SetPhase(ctx, fence, "offline")
			switch kind {
			case "drained":
				require.NoError(t, err)
				state, err = s.State(ctx)
				require.NoError(t, err)
				assert.Equal(t, "offline", state.Phase)
				assert.False(t, state.RecoveryComplete)
			case "stale":
				require.ErrorIs(t, err, domain.ErrStale)
			default:
				require.ErrorIs(t, err, domain.ErrMaintenance)
			}
		})
	}
}

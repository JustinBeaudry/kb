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
func TestMaintenanceRestartKeepsPermitUntilItsHolderFinishes(t *testing.T) {
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
	require.ErrorIs(t, s.MarkRecoveryComplete(ctx, st.Fence), domain.ErrMaintenance)
	require.ErrorIs(t, s.ValidatePermit(ctx, p), domain.ErrMaintenance, "a restored permit grants nothing")
	assert.True(t, store.permits[0].Unknown)
	assert.True(t, store.permits[0].Restored)
	wrong := p
	wrong.Generation++
	require.ErrorIs(t, s.Finish(ctx, wrong), domain.ErrStale)
	// Another restart keeps it restored; only the holder's exact Finish ends it.
	s, err = service.NewMaintenanceService(ctx, store)
	require.NoError(t, err)
	require.NoError(t, s.Finish(ctx, p))
	st, err = s.State(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, st.Unknown)
	require.NoError(t, s.MarkRecoveryComplete(ctx, st.Fence))
}
func TestMaintenanceUncertainPermitSurvivesRestartAndCannotBeForgotten(t *testing.T) {
	ctx := context.Background()
	s, store := newMaintenance(t)
	openMaintenance(t, s)
	p, err := s.Admit(ctx, "supervisor", "private-status")
	require.NoError(t, err)
	require.NoError(t, s.MarkUnknown(ctx, p))
	s, err = service.NewMaintenanceService(ctx, store)
	require.NoError(t, err)
	require.ErrorIs(t, s.Finish(ctx, p), domain.ErrUnknownOutcome)
	_, err = s.SettleRestored(ctx, p.ID)
	require.ErrorIs(t, err, domain.ErrUnknownOutcome)
	restored, err := s.Restored(ctx)
	require.NoError(t, err)
	assert.Empty(t, restored)
}
func TestMaintenanceLegacyUnknownRecordsRestoreOnlyServiceRoleKinds(t *testing.T) {
	ctx := context.Background()
	store := &maintenanceStore{
		state: domain.MaintenanceState{Phase: "recovering", Fence: domain.MaintenanceFence{OperationID: "startup-x", Generation: 3}},
		permits: []domain.OperationPermit{
			{ID: "s", InstanceID: "graph", Kind: "private-status", Generation: 2, Unknown: true},
			{ID: "b", InstanceID: "graph", Kind: "backup-recovery", Generation: 2, Unknown: true},
		},
	}
	// Every kind a worker or api process asks for (cmd/server, cmd/worker and
	// the platform handlers) must come back as restored, or a restart while it
	// was held leaves kb closed with no way to settle it.
	holderKinds := []string{"access-audit", "api", "api-startup", "health", "sweep", "worker", "worker-startup"}
	for _, kind := range holderKinds {
		store.permits = append(store.permits, domain.OperationPermit{ID: "h-" + kind, InstanceID: "holder", Kind: kind, Generation: 2, Unknown: true})
	}
	s, err := service.NewMaintenanceService(ctx, store)
	require.NoError(t, err)
	restored, err := s.Restored(ctx)
	require.NoError(t, err)
	ids := []string{}
	for _, p := range restored {
		ids = append(ids, p.ID)
	}
	want := []string{}
	for _, kind := range holderKinds {
		want = append(want, "h-"+kind)
	}
	assert.Equal(t, want, ids)
}
func TestMaintenanceSettleRestoredOnlyWhileClosed(t *testing.T) {
	ctx := context.Background()
	s, store := newMaintenance(t)
	openMaintenance(t, s)
	p, err := s.Admit(ctx, "old-worker", "worker")
	require.NoError(t, err)
	_, err = s.SettleRestored(ctx, p.ID)
	require.ErrorIs(t, err, domain.ErrMaintenance, "a live permit of an open supervisor is never settled")
	s, err = service.NewMaintenanceService(ctx, store)
	require.NoError(t, err)
	_, err = s.SettleRestored(ctx, "missing")
	require.ErrorIs(t, err, domain.ErrIncomplete)
	settled, err := s.SettleRestored(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, p.ID, settled.ID)
	assert.Empty(t, store.permits)
	st, err := s.State(ctx)
	require.NoError(t, err)
	require.NoError(t, s.MarkRecoveryComplete(ctx, st.Fence))
	require.NoError(t, s.Resume(ctx, st.Fence))
}
func TestMaintenanceQuiesceRefusesNewWorkAndWaitsForRunning(t *testing.T) {
	ctx := context.Background()
	s, _ := newMaintenance(t)
	openMaintenance(t, s)
	p, err := s.Admit(ctx, "api-one", "api")
	require.NoError(t, err)
	s.Quiesce()
	_, err = s.Admit(ctx, "api-two", "api")
	require.ErrorIs(t, err, domain.ErrMaintenance)
	short, cancel := context.WithTimeout(ctx, 5*time.Millisecond)
	t.Cleanup(cancel)
	require.ErrorIs(t, s.WaitIdle(short), context.DeadlineExceeded)
	done := make(chan error, 1)
	go func() { done <- s.WaitIdle(ctx) }()
	require.NoError(t, s.Finish(ctx, p))
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("WaitIdle did not return after the last Finish")
	}
}
func TestMaintenanceShutdownDrainWaitsForRestoredButNotUncertainPermits(t *testing.T) {
	ctx := context.Background()
	s, store := newMaintenance(t)
	openMaintenance(t, s)
	p, err := s.Admit(ctx, "api-one", "api")
	require.NoError(t, err)
	uncertain, err := s.Admit(ctx, "graph", "private-status")
	require.NoError(t, err)
	require.NoError(t, s.MarkUnknown(ctx, uncertain))
	s, err = service.NewMaintenanceService(ctx, store)
	require.NoError(t, err)
	s.Quiesce()
	short, cancel := context.WithTimeout(ctx, 5*time.Millisecond)
	t.Cleanup(cancel)
	require.ErrorIs(t, s.WaitIdle(short), context.DeadlineExceeded, "a holder from before the restart may still be running")
	done := make(chan error, 1)
	go func() { done <- s.WaitIdle(ctx) }()
	require.NoError(t, s.Finish(ctx, p))
	select {
	case err := <-done:
		require.NoError(t, err, "the uncertain permit is not waited on")
	case <-time.After(5 * time.Second):
		t.Fatal("WaitIdle did not return after the restored holder finished")
	}
}
func TestMaintenanceFinishKeepsThePermitUntilItsRemovalIsSaved(t *testing.T) {
	ctx := context.Background()
	s, store := newMaintenance(t)
	openMaintenance(t, s)
	p, err := s.Admit(ctx, "api-one", "api")
	require.NoError(t, err)
	store.fail = true
	require.ErrorIs(t, s.Finish(ctx, p), domain.ErrUnavailable)
	require.Len(t, store.permits, 1, "the permit is still on disk")
	store.fail = false
	require.NoError(t, s.Finish(ctx, p), "a retry saves the removal")
	assert.Empty(t, store.permits)
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
	// Reviewing owner judgments is read-only and is what a recovery after a
	// stolen owner key needs, so it is admitted during recovery.
	p, err = s.AdmitRecovery(ctx, st.Fence, "host", "host-admin:judgments")
	require.NoError(t, err)
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
func TestMaintenanceHeldPermitIsSettledOnlyAfterItsHoldAndGrace(t *testing.T) {
	domain.SetHoldOverrun(func(string) {})
	t.Cleanup(func() { domain.SetHoldOverrun(nil) })
	ctx := context.Background()
	s, store := newMaintenance(t)
	openMaintenance(t, s)
	held, release := domain.WithPermitHold(ctx, "worker")
	defer release(nil)
	before := time.Now().UTC()
	p, err := s.Admit(held, "worker-one", "worker")
	require.NoError(t, err)
	require.False(t, p.HoldUntil.IsZero())
	assert.WithinDuration(t, before.Add(15*time.Minute), p.HoldUntil, 5*time.Second)
	unheld, err := s.Admit(ctx, "worker-two", "worker")
	require.NoError(t, err)
	assert.True(t, unheld.HoldUntil.IsZero(), "a holder that sets no deadline declares no hold")

	// The holder died without finishing. Within the hold and its grace the
	// permit stays, since the holder may still be running.
	expired, err := s.ExpireHolds(ctx, p.HoldUntil.Add(4*time.Minute))
	require.NoError(t, err)
	assert.Empty(t, expired)
	expired, err = s.ExpireHolds(ctx, p.HoldUntil.Add(6*time.Minute))
	require.NoError(t, err)
	require.Len(t, expired, 1)
	assert.Equal(t, p.ID, expired[0].ID)
	require.Len(t, store.permits, 1, "the unheld permit is never expired")
	assert.Equal(t, unheld.ID, store.permits[0].ID)
	require.NoError(t, s.Finish(ctx, p), "a late Finish from the holder is harmless")
	expired, err = s.ExpireHolds(ctx, time.Now().UTC().Add(24*time.Hour))
	require.NoError(t, err)
	assert.Empty(t, expired)
}
func TestMaintenanceRestoredHeldPermitExpiresAndLetsStartupRecoveryProceed(t *testing.T) {
	domain.SetHoldOverrun(func(string) {})
	t.Cleanup(func() { domain.SetHoldOverrun(nil) })
	ctx := context.Background()
	s, store := newMaintenance(t)
	openMaintenance(t, s)
	held, release := domain.WithPermitHold(ctx, "api")
	defer release(nil)
	p, err := s.Admit(held, "api-one", "api")
	require.NoError(t, err)
	_, err = s.Admit(ctx, "legacy", "worker")
	require.NoError(t, err)
	legacy := store.permits
	// A graph restart cut both holders off.
	s, err = service.NewMaintenanceService(ctx, store)
	require.NoError(t, err)
	st, err := s.State(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, st.Unknown)
	expired, err := s.ExpireHolds(ctx, p.HoldUntil.Add(6*time.Minute))
	require.NoError(t, err)
	require.Len(t, expired, 1)
	assert.Equal(t, p.ID, expired[0].ID)
	st, err = s.State(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, st.Unknown, "a permit with no declared hold still needs its holder or an operator")
	require.ErrorIs(t, s.MarkRecoveryComplete(ctx, st.Fence), domain.ErrMaintenance)
	for _, l := range legacy {
		if l.ID != p.ID {
			_, err = s.SettleRestored(ctx, l.ID)
			require.NoError(t, err)
		}
	}
	require.NoError(t, s.MarkRecoveryComplete(ctx, st.Fence))
}
func TestMaintenanceHoldsAreOnlyForServiceRoleKindsAndNeverForUncertainPermits(t *testing.T) {
	ctx := context.Background()
	s, store := newMaintenance(t)
	openMaintenance(t, s)
	_, err := s.Admit(domain.WithDeclaredHold(ctx, time.Minute), "admin", "custom")
	require.ErrorIs(t, err, domain.ErrInvalidInput)
	_, err = s.Admit(domain.WithDeclaredHold(ctx, 2*time.Hour), "api-one", "api")
	require.ErrorIs(t, err, domain.ErrInvalidInput)
	_, err = s.Admit(domain.WithDeclaredHold(ctx, -time.Minute), "api-one", "api")
	require.ErrorIs(t, err, domain.ErrInvalidInput)
	p, err := s.Admit(domain.WithDeclaredHold(ctx, time.Minute), "api-one", "api")
	require.NoError(t, err)
	require.NoError(t, s.MarkUnknown(ctx, p))
	expired, err := s.ExpireHolds(ctx, p.HoldUntil.Add(time.Hour))
	require.NoError(t, err)
	assert.Empty(t, expired, "a permit its holder declared uncertain is never settled by time")
	require.Len(t, store.permits, 1)
}
func TestMaintenanceHoldExpiryKeepsPermitsWhenItsSaveFails(t *testing.T) {
	ctx := context.Background()
	s, store := newMaintenance(t)
	openMaintenance(t, s)
	p, err := s.Admit(domain.WithDeclaredHold(ctx, time.Minute), "api-one", "api")
	require.NoError(t, err)
	store.fail = true
	_, err = s.ExpireHolds(ctx, p.HoldUntil.Add(time.Hour))
	require.ErrorIs(t, err, domain.ErrUnavailable)
	require.Len(t, store.permits, 1, "the permit is still on disk")
	expired, err := s.ExpireHolds(ctx, p.HoldUntil.Add(time.Hour))
	require.NoError(t, err)
	assert.Empty(t, expired, "a failed supervisor changes nothing")
}

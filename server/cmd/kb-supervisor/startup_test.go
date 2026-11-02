package main

import (
	"context"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type startupStateStore struct {
	state   domain.MaintenanceState
	permits []domain.OperationPermit
}

func (s *startupStateStore) Load(context.Context) (domain.MaintenanceState, []domain.OperationPermit, error) {
	return s.state, append([]domain.OperationPermit(nil), s.permits...), nil
}
func (s *startupStateStore) Save(_ context.Context, state domain.MaintenanceState, p []domain.OperationPermit) error {
	s.state = state
	s.permits = append([]domain.OperationPermit(nil), p...)
	return nil
}

type startupSnapshotFake struct {
	calls              []string
	failure, uncertain string
}

func (s *startupSnapshotFake) step(ctx context.Context, stage string) error {
	s.calls = append(s.calls, stage)
	if s.uncertain == stage {
		domain.MarkEffectUncertain(ctx)
	}
	if s.failure == stage {
		return domain.ErrUnavailable
	}
	return nil
}
func (s *startupSnapshotFake) InitializeAndRecover(ctx context.Context) error {
	return s.step(ctx, "schema")
}
func (s *startupSnapshotFake) bootstrapPrerequisites(ctx context.Context) error {
	return s.step(ctx, "prerequisites")
}
func (s *startupSnapshotFake) ReconcileJournal(ctx context.Context) (domain.JournalBarrier, error) {
	return domain.JournalBarrier{Namespace: "live"}, s.step(ctx, "journal")
}
func newStartupMaintenance(t *testing.T) *service.MaintenanceService {
	t.Helper()
	m, e := service.NewMaintenanceService(t.Context(), &startupStateStore{state: domain.MaintenanceState{Phase: "bootstrap", Fence: domain.MaintenanceFence{OperationID: "bootstrap", Generation: 1}}})
	require.NoError(t, e)
	return m
}
func TestStartupOpensOnlyAfterSchemaPrerequisitesAndLiveReplay(t *testing.T) {
	m := newStartupMaintenance(t)
	snapshot := &startupSnapshotFake{}
	require.NoError(t, startupRecovery(t.Context(), m, snapshot, "fixture"))
	state, e := m.State(t.Context())
	require.NoError(t, e)
	assert.Equal(t, "open", state.Phase)
	assert.Zero(t, state.Active)
	assert.Zero(t, state.Unknown)
	assert.Equal(t, []string{"schema", "prerequisites", "journal"}, snapshot.calls)
}
func TestMissingBootstrapPrerequisiteReleasesOnlySettledRecovery(t *testing.T) {
	m := newStartupMaintenance(t)
	snapshot := &startupSnapshotFake{failure: "prerequisites"}
	require.Error(t, startupRecovery(t.Context(), m, snapshot, "fixture"))
	state, e := m.State(t.Context())
	require.NoError(t, e)
	assert.Equal(t, "bootstrap", state.Phase)
	assert.Zero(t, state.Active)
	assert.Zero(t, state.Unknown)
	permit, e := m.AdmitRecovery(t.Context(), state.Fence, "owner", "host-admin:setup")
	require.NoError(t, e)
	require.NoError(t, m.Finish(t.Context(), permit))
}
func TestStartupMaskedUnknownEffectCannotOpen(t *testing.T) {
	m := newStartupMaintenance(t)
	snapshot := &startupSnapshotFake{uncertain: "journal"}
	require.ErrorIs(t, startupRecovery(t.Context(), m, snapshot, "fixture"), domain.ErrUnknownOutcome)
	state, e := m.State(t.Context())
	require.NoError(t, e)
	assert.Equal(t, "bootstrap", state.Phase)
	assert.Equal(t, 1, state.Unknown)
	_, e = m.AdmitRecovery(t.Context(), state.Fence, "owner", "host-admin:setup")
	require.Error(t, e)
}
func TestStartupFailedUnknownSchemaRemainsUnreconciled(t *testing.T) {
	m := newStartupMaintenance(t)
	snapshot := &startupSnapshotFake{failure: "schema", uncertain: "schema"}
	require.Error(t, startupRecovery(t.Context(), m, snapshot, "fixture"))
	state, e := m.State(t.Context())
	require.NoError(t, e)
	assert.Equal(t, 1, state.Unknown)
	assert.Equal(t, []string{"schema"}, snapshot.calls)
}

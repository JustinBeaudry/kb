package main

import (
	"context"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type statusAdmissionFake struct{ finished, unknown bool }

func (s *statusAdmissionFake) Admit(context.Context, string, string) (domain.OperationPermit, error) {
	return domain.OperationPermit{ID: "status"}, nil
}
func (s *statusAdmissionFake) Finish(context.Context, domain.OperationPermit) error {
	s.finished = true
	return nil
}
func (s *statusAdmissionFake) MarkUnknown(context.Context, domain.OperationPermit) error {
	s.unknown = true
	return nil
}

type statusJournalFake struct{ uncertain, fail bool }

func (s statusJournalFake) CheckHealth(ctx context.Context) (uint64, uint64, error) {
	if s.uncertain {
		domain.MarkEffectUncertain(ctx)
	}
	if s.fail {
		return 0, 0, domain.ErrUnavailable
	}
	return 9, 10000, nil
}
func TestPrivateStatusCannotReleaseMaskedUnknownQualification(t *testing.T) {
	admission := &statusAdmissionFake{}
	_, _, err := privateJournalStatus(t.Context(), admission, statusJournalFake{uncertain: true}, "fixture")
	require.ErrorIs(t, err, domain.ErrUnknownOutcome)
	assert.False(t, admission.finished)
	assert.True(t, admission.unknown)
}
func TestPrivateStatusReleasesSettledReadFailure(t *testing.T) {
	admission := &statusAdmissionFake{}
	_, _, err := privateJournalStatus(t.Context(), admission, statusJournalFake{fail: true}, "fixture")
	require.ErrorIs(t, err, domain.ErrUnavailable)
	assert.True(t, admission.finished)
	assert.False(t, admission.unknown)
}

// statusRaceFake models a drain beginning after status read the open phase.
type statusRaceFake struct {
	statusAdmissionFake
	admitErr error
	current  domain.MaintenanceState
}

func (s *statusRaceFake) Admit(ctx context.Context, instance, kind string) (domain.OperationPermit, error) {
	if s.admitErr != nil {
		return domain.OperationPermit{}, s.admitErr
	}
	return s.statusAdmissionFake.Admit(ctx, instance, kind)
}
func (s *statusRaceFake) State(context.Context) (domain.MaintenanceState, error) {
	return s.current, nil
}
func TestPrivateStatusSamplesJournalWhileOpen(t *testing.T) {
	maintenance := &statusRaceFake{current: domain.MaintenanceState{Phase: "open"}}
	out := operatorStatus{Maintenance: domain.MaintenanceState{Phase: "open"}}
	require.NoError(t, sampleOpenJournal(t.Context(), maintenance, statusJournalFake{}, "fixture", &out))
	assert.Equal(t, uint64(9), out.JournalSequence)
	assert.Equal(t, uint64(10000), out.JournalCapacity)
	assert.Equal(t, "open", out.Maintenance.Phase)
}
func TestPrivateStatusReportsDrainThatBeganAfterOpenPhaseRead(t *testing.T) {
	maintenance := &statusRaceFake{admitErr: domain.ErrMaintenance, current: domain.MaintenanceState{Phase: "draining", Active: 1}}
	out := operatorStatus{Maintenance: domain.MaintenanceState{Phase: "open"}}
	require.NoError(t, sampleOpenJournal(t.Context(), maintenance, statusJournalFake{}, "fixture", &out))
	assert.Equal(t, "draining", out.Maintenance.Phase)
	assert.Equal(t, 1, out.Maintenance.Active)
	assert.Zero(t, out.JournalSequence, "a closed phase leaves the journal unsampled")
}
func TestPrivateStatusKeepsRefusalWhileStillOpen(t *testing.T) {
	maintenance := &statusRaceFake{admitErr: domain.ErrMaintenance, current: domain.MaintenanceState{Phase: "open"}}
	out := operatorStatus{Maintenance: domain.MaintenanceState{Phase: "open"}}
	require.ErrorIs(t, sampleOpenJournal(t.Context(), maintenance, statusJournalFake{}, "fixture", &out), domain.ErrMaintenance)
}

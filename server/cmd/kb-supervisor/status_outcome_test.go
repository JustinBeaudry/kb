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

package service_test

import (
	"context"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type uncertainHealthJournal struct{}

func (uncertainHealthJournal) CheckHealth(ctx context.Context) (uint64, uint64, error) {
	domain.MarkEffectUncertain(ctx)
	return 0, 10000, nil
}
func TestHealthJournalQualificationUnknownPreventsDrain(t *testing.T) {
	s, _ := newMaintenance(t)
	openMaintenance(t, s)
	h := service.NewHealthService(s, &healthProbe{}, &healthProbe{}, uncertainHealthJournal{}, time.Second, "health")
	assert.False(t, h.Readiness(t.Context()).Ready)
	state, err := s.State(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, state.Active)
	fence, err := s.Begin(t.Context(), "backup")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	require.Error(t, s.WaitDrained(ctx, fence))
}
func TestHealthKnownReadFailureReleasesPermit(t *testing.T) {
	s, _ := newMaintenance(t)
	openMaintenance(t, s)
	h := service.NewHealthService(s, &healthProbe{err: domain.ErrUnavailable}, &healthProbe{}, journalProbe{err: domain.ErrIncomplete}, time.Second, "health")
	assert.False(t, h.Readiness(t.Context()).Ready)
	state, err := s.State(t.Context())
	require.NoError(t, err)
	assert.Zero(t, state.Active)
	fence, err := s.Begin(t.Context(), "backup")
	require.NoError(t, err)
	require.NoError(t, s.WaitDrained(t.Context(), fence))
}

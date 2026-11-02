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

type healthProbe struct {
	err   error
	calls int
}

func (p *healthProbe) Probe(context.Context) error { p.calls++; return p.err }

type journalProbe struct {
	sequence uint64
	err      error
}

func (p journalProbe) CheckHealth(context.Context) (uint64, uint64, error) {
	return p.sequence, 10000, p.err
}
func TestHealthClosedReadinessDoesNotProbeDependencies(t *testing.T) {
	s, _ := newMaintenance(t)
	graph := &healthProbe{}
	object := &healthProbe{}
	h := service.NewHealthService(s, graph, object, journalProbe{}, time.Second, "health")
	report := h.Readiness(context.Background())
	assert.True(t, h.Liveness().Live)
	assert.False(t, report.Ready)
	assert.Equal(t, []string{"maintenance"}, report.Reasons)
	assert.Zero(t, graph.calls)
	assert.Zero(t, object.calls)
}
func TestMissingModelRouteDoesNotFailReadiness(t *testing.T) {
	s, _ := newMaintenance(t)
	openMaintenance(t, s)
	h := service.NewHealthService(s, &healthProbe{}, &healthProbe{}, journalProbe{sequence: 9000}, time.Second, "health")
	report := h.Readiness(context.Background())
	require.True(t, report.Ready)
	assert.True(t, report.JournalCapacityWarning)
	st, _ := s.State(context.Background())
	assert.Zero(t, st.Active)
}
func TestHealthSanitizesProbeFailureAndCapacity(t *testing.T) {
	s, _ := newMaintenance(t)
	openMaintenance(t, s)
	h := service.NewHealthService(s, &healthProbe{err: domain.ErrUnavailable}, &healthProbe{}, journalProbe{sequence: 10000}, time.Second, "health")
	report := h.Readiness(context.Background())
	assert.False(t, report.Ready)
	assert.Equal(t, []string{"graph_unavailable", "journal_capacity"}, report.Reasons)
}

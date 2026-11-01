package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/handler"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type samplerAdmission struct{ states, admits, finishes atomic.Int32 }

func (a *samplerAdmission) State(context.Context) (domain.MaintenanceState, error) {
	a.states.Add(1)
	return domain.MaintenanceState{Phase: "open"}, nil
}
func (a *samplerAdmission) Admit(context.Context, string, string) (domain.OperationPermit, error) {
	return domain.OperationPermit{ID: fmt.Sprint(a.admits.Add(1))}, nil
}
func (a *samplerAdmission) Finish(context.Context, domain.OperationPermit) error {
	a.finishes.Add(1)
	return nil
}

type samplerProbe struct {
	calls    atomic.Int32
	entered  chan context.Context
	release  chan struct{}
	canceled chan struct{}
	cleanup  chan struct{}
	err      error
}

func (p *samplerProbe) Probe(ctx context.Context) error {
	p.calls.Add(1)
	if p.entered != nil {
		p.entered <- ctx
	}
	if p.release != nil {
		select {
		case <-p.release:
		case <-ctx.Done():
			if p.canceled != nil {
				close(p.canceled)
			}
			if p.cleanup != nil {
				<-p.cleanup
			}
			return ctx.Err()
		}
	}
	return p.err
}

type samplerJournal struct {
	calls     atomic.Int32
	uncertain bool
}

func (j *samplerJournal) CheckHealth(ctx context.Context) (uint64, uint64, error) {
	j.calls.Add(1)
	if j.uncertain {
		domain.MarkEffectUncertain(ctx)
	}
	return 1, 10000, nil
}

func TestReadinessPublicFloodDoesNotDriveOrCancelDependencyProbes(t *testing.T) {
	admission := &samplerAdmission{}
	graph := &samplerProbe{entered: make(chan context.Context, 1), release: make(chan struct{})}
	objects, journal := &samplerProbe{}, &samplerJournal{}
	health := service.NewHealthService(admission, graph, objects, journal, 2*time.Second, "api")
	cache := newReadinessSampler(t.Context(), health, time.Hour, time.Hour)
	t.Cleanup(cache.stop)
	probeCtx := <-graph.entered
	public := handler.NewHealthHandler(cache)
	var requests sync.WaitGroup
	for range 128 {
		requests.Go(func() {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			response := httptest.NewRecorder()
			public.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil).WithContext(ctx))
			assert.Equal(t, http.StatusServiceUnavailable, response.Code)
		})
	}
	requests.Wait()
	assert.NoError(t, probeCtx.Err(), "request cancellation must not cancel lifecycle-owned work")
	assert.EqualValues(t, 1, admission.states.Load())
	assert.EqualValues(t, 1, admission.admits.Load())
	assert.EqualValues(t, 1, graph.calls.Load())
	assert.Zero(t, objects.calls.Load())
	assert.Zero(t, journal.calls.Load())
	close(graph.release)
	require.Eventually(t, func() bool { return cache.Readiness(t.Context()).Ready }, time.Second, time.Millisecond)
	assert.EqualValues(t, 1, admission.finishes.Load())
	assert.EqualValues(t, 1, objects.calls.Load())
	assert.EqualValues(t, 1, journal.calls.Load())
}

func TestReadinessCacheExpiresWithoutRequestTriggeredRefresh(t *testing.T) {
	admission, graph, objects, journal := &samplerAdmission{}, &samplerProbe{}, &samplerProbe{}, &samplerJournal{}
	health := service.NewHealthService(admission, graph, objects, journal, time.Second, "api")
	cache := newReadinessSampler(t.Context(), health, time.Hour, 30*time.Millisecond)
	t.Cleanup(cache.stop)
	require.Eventually(t, func() bool { return cache.latest.Load() != nil }, time.Second, time.Millisecond)
	assert.True(t, cache.latest.Load().report.Ready)
	require.Eventually(t, func() bool { return !cache.Readiness(t.Context()).Ready }, time.Second, time.Millisecond)
	public := handler.NewHealthHandler(cache)
	for _, path := range []string{"/readyz", "/livez"} {
		response := httptest.NewRecorder()
		public.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if path == "/readyz" {
			assert.Equal(t, http.StatusServiceUnavailable, response.Code)
		} else {
			assert.Equal(t, http.StatusOK, response.Code)
		}
	}
	assert.EqualValues(t, 1, admission.admits.Load())
	assert.EqualValues(t, 1, graph.calls.Load())
}

func TestReadinessUnreadySnapshotCannotBeMutatedByReader(t *testing.T) {
	admission, journal := &samplerAdmission{}, &samplerJournal{}
	health := service.NewHealthService(admission, &samplerProbe{err: domain.ErrUnavailable}, &samplerProbe{}, journal, time.Second, "api")
	cache := newReadinessSampler(t.Context(), health, time.Hour, time.Hour)
	t.Cleanup(cache.stop)
	require.Eventually(t, func() bool { return admission.finishes.Load() == 1 }, time.Second, time.Millisecond)
	require.Eventually(t, func() bool {
		return len(cache.Readiness(t.Context()).Reasons) == 1 && cache.Readiness(t.Context()).Reasons[0] == "graph_unavailable"
	}, time.Second, time.Millisecond)
	report := cache.Readiness(t.Context())
	assert.False(t, report.Ready)
	report.Reasons[0] = "reader-injected"
	assert.Equal(t, []string{"graph_unavailable"}, cache.Readiness(t.Context()).Reasons)
}

func TestReadinessStopJoinsProbeCleanupBeforeClosingDependencies(t *testing.T) {
	admission, objects, journal := &samplerAdmission{}, &samplerProbe{}, &samplerJournal{}
	graph := &samplerProbe{entered: make(chan context.Context, 1), release: make(chan struct{}), canceled: make(chan struct{}), cleanup: make(chan struct{})}
	health := service.NewHealthService(admission, graph, objects, journal, time.Second, "api")
	var closed atomic.Bool
	runtime := &serverRuntime{readiness: health, close: func() { closed.Store(true) }}
	runtime.startReadiness(t.Context(), time.Hour, time.Hour)
	<-graph.entered
	done := make(chan struct{})
	go func() { runtime.close(); close(done) }()
	<-graph.canceled
	assert.False(t, closed.Load(), "DB close must wait for the active probe's cleanup")
	select {
	case <-done:
		t.Fatal("runtime closed while cleanup was held")
	default:
	}
	close(graph.cleanup)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runtime did not join completed cleanup")
	}
	assert.True(t, closed.Load())
	assert.EqualValues(t, 1, graph.calls.Load())
	response := httptest.NewRecorder()
	runtime.health.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	assert.Equal(t, http.StatusServiceUnavailable, response.Code)
}

func TestReadinessCanceledStartupDoesNotStartDependencyProbe(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	admission, graph, journal := &samplerAdmission{}, &samplerProbe{}, &samplerJournal{}
	cache := newReadinessSampler(ctx, service.NewHealthService(admission, graph, &samplerProbe{}, journal, time.Second, "api"), time.Hour, time.Hour)
	cache.stop()
	assert.False(t, cache.Readiness(t.Context()).Ready)
	assert.Zero(t, admission.states.Load())
	assert.Zero(t, graph.calls.Load())
}

func TestReadinessSamplerNeverFinishesUncertainJournalEffects(t *testing.T) {
	admission, journal := &samplerAdmission{}, &samplerJournal{uncertain: true}
	cache := newReadinessSampler(t.Context(), service.NewHealthService(admission, &samplerProbe{}, &samplerProbe{}, journal, time.Second, "api"), time.Hour, time.Hour)
	t.Cleanup(cache.stop)
	require.Eventually(t, func() bool { return journal.calls.Load() == 1 }, time.Second, time.Millisecond)
	cache.stop()
	assert.Zero(t, admission.finishes.Load())
	assert.False(t, cache.Readiness(t.Context()).Ready)
}

type samplerProbeStep struct{ result chan error }
type samplerTransitionProbe struct {
	steps    chan samplerProbeStep
	inFlight atomic.Int32
	overlap  atomic.Bool
}

func (p *samplerTransitionProbe) Probe(ctx context.Context) error {
	if p.inFlight.Add(1) != 1 {
		p.overlap.Store(true)
	}
	defer p.inFlight.Add(-1)
	step := samplerProbeStep{result: make(chan error, 1)}
	select {
	case p.steps <- step:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-step.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestReadinessSequentialSamplesObserveFailureAndRecovery(t *testing.T) {
	admission, journal := &samplerAdmission{}, &samplerJournal{}
	graph := &samplerTransitionProbe{steps: make(chan samplerProbeStep, 1)}
	health := service.NewHealthService(admission, graph, &samplerProbe{}, journal, 5*time.Second, "api")
	cache := newReadinessSampler(t.Context(), health, 5*time.Millisecond, time.Hour)
	t.Cleanup(cache.stop)
	next := func() samplerProbeStep {
		t.Helper()
		select {
		case step := <-graph.steps:
			return step
		case <-time.After(3 * time.Second):
			t.Fatal("scheduled probe did not start")
			return samplerProbeStep{}
		}
	}
	first := next()
	assert.False(t, cache.Readiness(t.Context()).Ready)
	// Hold the first I/O past several intervals: no parallel probe or catch-up
	// may begin. Successful-state assertions below are barrier controlled.
	select {
	case <-graph.steps:
		t.Fatal("a second scheduled probe overlapped held I/O")
	case <-time.After(20 * time.Millisecond):
	}
	first.result <- nil
	second := next() // The previous sample was published before this starts.
	assert.True(t, cache.Readiness(t.Context()).Ready)
	second.result <- domain.ErrUnavailable
	third := next()
	assert.False(t, cache.Readiness(t.Context()).Ready)
	assert.Equal(t, []string{"graph_unavailable"}, cache.Readiness(t.Context()).Reasons)
	third.result <- nil
	_ = next()
	assert.True(t, cache.Readiness(t.Context()).Ready)
	cache.stop() // Cancels and joins the fourth held probe.
	assert.Zero(t, graph.inFlight.Load())
	assert.False(t, graph.overlap.Load())
	assert.EqualValues(t, 4, admission.admits.Load())
	assert.EqualValues(t, 4, admission.finishes.Load())
}

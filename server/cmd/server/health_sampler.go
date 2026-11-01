package main

import (
	"context"
	"slices"
	"sync/atomic"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/handler"
)

const readinessInterval = 30 * time.Second
const readinessMaxAge = 90 * time.Second

type readinessProbe interface {
	Readiness(context.Context) domain.HealthReport
}
type readinessSample struct {
	report domain.HealthReport
	at     time.Time
}
type readinessSampler struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	latest atomic.Pointer[readinessSample]
	maxAge time.Duration
}

func newReadinessSampler(ctx context.Context, source readinessProbe, interval, maxAge time.Duration) *readinessSampler {
	ctx, cancel := context.WithCancel(ctx)
	s := &readinessSampler{ctx: ctx, cancel: cancel, done: make(chan struct{}), maxAge: maxAge}
	go func() {
		defer close(s.done)
		timer := time.NewTimer(0)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			if ctx.Err() != nil {
				return
			}
			// HealthService owns admission, timeout, effect accounting and joined
			// cleanup. Request contexts never enter this lifecycle-owned call.
			report := source.Readiness(ctx)
			if ctx.Err() != nil {
				return
			}
			s.latest.Store(&readinessSample{report: domain.HealthReport{Live: report.Live, Ready: report.Ready, Reasons: slices.Clone(report.Reasons)}, at: time.Now()})
			// Delay from completion: slow probes never overlap or create catch-up work.
			timer.Reset(interval)
		}
	}()
	return s
}

func (s *readinessSampler) Liveness() domain.HealthReport {
	return domain.HealthReport{Live: true}
}
func (s *readinessSampler) Readiness(context.Context) domain.HealthReport {
	sample := s.latest.Load()
	if s.ctx.Err() != nil || sample == nil || time.Since(sample.at) >= s.maxAge {
		return domain.HealthReport{Live: true, Reasons: []string{"starting"}}
	}
	report := sample.report
	report.Reasons = slices.Clone(report.Reasons)
	return report
}
func (s *readinessSampler) stop() {
	s.cancel()
	<-s.done
}

func (r *serverRuntime) startReadiness(ctx context.Context, interval, maxAge time.Duration) {
	sampler := newReadinessSampler(ctx, r.readiness, interval, maxAge)
	r.health = handler.NewHealthHandler(sampler)
	closeDependencies := r.close
	r.close = func() {
		sampler.stop()
		closeDependencies()
	}
}

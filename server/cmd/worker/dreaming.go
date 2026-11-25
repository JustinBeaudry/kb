package main

import (
	"context"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/platform"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
)

// composeDreaming wires budgeted dreaming. Without an owner-configured key,
// authorized route, pricing and daily cap, dream jobs are still claimed but
// deferred with a visible reason; no spend happens. dream.notify is not
// registered until a Buzz NoticeSender exists, so notices wait in the outbox.
func composeDreaming(store *graph.Store, jobs *service.JobsService, jobsRepo *repository.JobsRepository, recorder gateway.GenerationRecorder) (*service.DreamingService, error) {
	cfg, err := platform.LoadModelConfig()
	if err != nil {
		return nil, err
	}
	var llm service.LLMGateway
	if cfg.APIKey != "" {
		anthropic, err := gateway.NewAnthropic(gateway.AnthropicConfig{Endpoint: cfg.Endpoint, APIKey: cfg.APIKey}, nil, recorder)
		if err != nil {
			return nil, err
		}
		llm = anthropic
	}
	budget := service.NewModelBudgetService(store, repository.NewModelBudgetRepository(store), cfg.DailyCapMicroUSD, time.Now)
	findings := repository.NewFindingsRepository(store)
	return service.NewDreamingService(store, findings, repository.NewMemoryRepository(store), jobs, jobsRepo, budget, llm, service.DreamingConfig{Routes: cfg.Routes, DigestHour: cfg.DigestHourUTC, Now: time.Now}), nil
}

// dreamScheduler runs an incremental scan on every sweep (new events, task
// boundaries and compaction surface as newly ingested claim revisions) and a
// full scan hourly. Unchanged input digests enqueue nothing.
type dreamScheduler struct {
	dreaming *service.DreamingService
	last     time.Time
	lastFull time.Time
}

func (d *dreamScheduler) tick(ctx context.Context, now time.Time) error {
	since := d.last.Add(-time.Minute)
	if d.last.IsZero() || now.Sub(d.lastFull) >= time.Hour {
		since = time.Time{}
	}
	if _, err := d.dreaming.Schedule(ctx, since); err != nil {
		return err
	}
	if since.IsZero() {
		d.lastFull = now
	}
	d.last = now
	return nil
}

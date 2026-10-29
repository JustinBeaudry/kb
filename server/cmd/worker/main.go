package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/platform"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := runCapture(context.Background(), logger); err != nil {
		logger.Error("worker_stopped", "reason", "configuration_or_handlers_unavailable")
		os.Exit(1)
	}
}
func run(parent context.Context, logger *slog.Logger, handlers map[string]platform.RunJob) error {
	if len(handlers) == 0 {
		return domain.ErrUnavailable
	}
	kinds := make([]string, 0, len(handlers))
	for kind, handler := range handlers {
		if handler == nil || domain.ValidateIDs(kind) != nil {
			return domain.ErrInvalidInput
		}
		kinds = append(kinds, kind)
	}
	cfg, err := platform.LoadConfig()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := graph.Open(ctx, cfg.Graph)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = store.Close(cleanup)
	}()
	jobs := service.NewJobsService(store, repository.NewJobsRepository(store))
	worker := platform.Worker{ID: os.Getenv("KB_WORKER_ID"), Kinds: kinds, Jobs: jobs, RunJob: platform.DispatchJobs(handlers), Logger: logger}
	return worker.Run(ctx)
}

// runCapture composes the only implemented durable handler. Intelligence owns
// evidence.extract later; it is deliberately absent from the claimed kinds.
func runCapture(parent context.Context, logger *slog.Logger) error {
	cfg, err := platform.LoadConfig()
	if err != nil {
		return err
	}
	objectConfig, err := platform.LoadObjectConfig()
	if err != nil {
		return err
	}
	objects, err := gateway.NewS3(objectConfig)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := graph.Open(ctx, cfg.Graph)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = store.Close(cleanup)
	}()
	repo := repository.NewEvidenceRepository(store)
	jobsRepo := repository.NewJobsRepository(store)
	jobs := service.NewJobsService(store, jobsRepo)
	capture := service.NewCaptureService(store, repo, jobsRepo, objects)
	handlers := map[string]platform.RunJob{"capture.reconcile": func(ctx context.Context, job domain.Job, lease domain.Lease) error {
		return capture.Reconcile(ctx, job, lease, jobs)
	}}
	worker := platform.Worker{ID: os.Getenv("KB_WORKER_ID"), Kinds: []string{"capture.reconcile"}, Jobs: jobs, RunJob: platform.DispatchJobs(handlers), Logger: logger}
	sweepCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		timer := time.NewTicker(time.Minute)
		defer timer.Stop()
		for {
			select {
			case <-sweepCtx.Done():
				return
			case <-timer.C:
				checkCtx, stop := context.WithTimeout(sweepCtx, 30*time.Second)
				err := capture.Sweep(checkCtx, 100)
				stop()
				if err != nil {
					logger.Warn("capture_reconciliation_incomplete", "reason", "objects_or_policy_unavailable")
				}
			}
		}
	}()
	err = worker.Run(ctx)
	cancel()
	<-stopped
	return err
}

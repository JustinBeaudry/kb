package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	// Feature composition registers actual handlers here as they land. Starting
	// with no implementations is unavailable, never a worker that drops work.
	if err := run(context.Background(), logger, nil); err != nil {
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

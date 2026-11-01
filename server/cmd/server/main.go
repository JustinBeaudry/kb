package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/handler"
	"github.com/JustinBeaudry/kb/server/internal/platform"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(context.Background(), logger); err != nil {
		logger.Error("server_stopped", "reason", "configuration_or_service_unavailable")
		os.Exit(1)
	}
}
func run(parent context.Context, logger *slog.Logger) error {
	cfg, err := platform.LoadConfig()
	if err != nil {
		return err
	}
	key, err := platform.LoadRetrievalKey()
	if err != nil {
		return err
	}
	journalConfig, err := platform.LoadJournalConfig()
	if err != nil {
		return err
	}
	journal, err := gateway.NewS3DeletionJournal(journalConfig)
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
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = store.Close(closeCtx)
	}()
	objectConfig, err := platform.LoadObjectConfig()
	if err != nil {
		return err
	}
	objects, err := gateway.NewS3(objectConfig)
	if err != nil {
		return err
	}
	lifecycle := service.NewLifecycleService(store, repository.NewLifecycleRepository(store), repository.NewIdentityRepository(store), repository.NewJobsRepository(store), objects, journal, time.Now)
	if err = lifecycle.Recover(ctx); err != nil {
		return err
	}
	capture := service.NewCaptureService(store, repository.NewEvidenceRepository(store), repository.NewJobsRepository(store), objects)
	retrievalRepo := repository.NewRetrievalRepository(store)
	checkpointRepo := repository.NewCheckpointRepository(store)
	evidenceRepo := repository.NewEvidenceRepository(store)
	checkpoints := service.NewCheckpointService(store, checkpointRepo, retrievalRepo)
	memory := service.NewMemoryService(store, repository.NewMemoryRepository(store), retrievalRepo, evidenceRepo, capture)
	retrieval, err := service.NewRetrievalService(store, retrievalRepo, checkpointRepo, evidenceRepo, objects, key)
	if err != nil {
		return err
	}
	status := service.NewStatusService(store, retrievalRepo)
	e, err := handler.NewServer(service.NewIdentityService(store, repository.NewIdentityRepository(store), gateway.NostrIdentity{}), capture, checkpoints, memory, retrieval, service.NewPolicyService(store, evidenceRepo, retrievalRepo), status, logger)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: cfg.Address, Handler: e, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: cfg.ReadTimeout, WriteTimeout: cfg.WriteTimeout, IdleTimeout: cfg.IdleTimeout, MaxHeaderBytes: 16 << 10}
	result := make(chan error, 1)
	go func() {
		if cfg.TLSCert != "" {
			result <- server.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
		} else {
			result <- server.ListenAndServe()
		}
	}()
	select {
	case err := <-result:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

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
	capture := service.NewCaptureService(store, repository.NewEvidenceRepository(store), repository.NewJobsRepository(store), objects)
	checkpoints := service.NewCheckpointService(store, repository.NewCheckpointRepository(store))
	e, err := handler.NewMemoryServer(service.NewIdentityService(store, repository.NewIdentityRepository(store), gateway.NostrIdentity{}), capture, checkpoints, logger)
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

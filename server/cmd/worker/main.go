package main

import (
	"context"
	"log/slog"
	"net/http"
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
	telemetry, err := platform.NewTelemetry(os.Stderr, "worker")
	if err != nil {
		os.Exit(1)
	}
	defer func() { _ = telemetry.Shutdown(context.Background()) }()
	http.DefaultTransport = telemetry.HTTPClientTransport(http.DefaultTransport)
	logger := telemetry.Logger()
	if err := runCapture(context.Background(), logger, telemetry); err != nil {
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
	journalConfig, err := platform.LoadJournalConfig()
	if err != nil {
		return err
	}
	journal, err := gateway.NewS3DeletionJournal(journalConfig)
	if err != nil {
		return err
	}
	mc, err := platform.LoadMaintenanceConfig()
	if err != nil {
		return err
	}
	admission, err := gateway.NewMaintenanceHTTPClient(mc.Endpoint, mc.Secret, mc.InstanceID)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	permit, err := platform.WaitForAdmission(ctx, admission, mc.InstanceID, "worker-startup")
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	startupCtx := domain.WithOperationOutcome(ctx)
	startupFinished := false
	defer func() {
		if failure := recover(); failure != nil {
			domain.MarkEffectUncertain(startupCtx)
			panic(failure)
		}
		if !startupFinished {
			_ = platform.FinishOperation(startupCtx, admission, permit, nil)
		}
	}()
	store, err := graph.Connect(startupCtx, cfg.Graph)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(startupCtx), 5*time.Second)
		defer cancel()
		_ = store.Close(cleanup)
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
	if err = lifecycle.Recover(startupCtx); err != nil {
		return err
	}
	if err = platform.FinishOperation(startupCtx, admission, permit, nil); err != nil {
		return err
	}
	startupFinished = true
	jobs := service.NewJobsService(store, repository.NewJobsRepository(store))
	worker := platform.Worker{ID: os.Getenv("KB_WORKER_ID"), InstanceID: mc.InstanceID, Admission: admission, Kinds: kinds, Jobs: jobs, RunJob: platform.DispatchJobs(handlers), Logger: logger}
	return worker.Run(ctx)
}

// runCapture composes durable capture and lifecycle handlers. Intelligence owns
// evidence.extract later; it is deliberately absent from the claimed kinds.
func runCapture(parent context.Context, logger *slog.Logger, telemetry ...*platform.Telemetry) error {
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
	journalConfig, err := platform.LoadJournalConfig()
	if err != nil {
		return err
	}
	journal, err := gateway.NewS3DeletionJournal(journalConfig)
	if err != nil {
		return err
	}
	mc, err := platform.LoadMaintenanceConfig()
	if err != nil {
		return err
	}
	admission, err := gateway.NewMaintenanceHTTPClient(mc.Endpoint, mc.Secret, mc.InstanceID)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	health, err := startWorkerHealth(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = health.Close() }()
	permit, err := platform.WaitForAdmission(ctx, admission, mc.InstanceID, "worker-startup")
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	startupCtx := domain.WithOperationOutcome(ctx)
	startupFinished := false
	defer func() {
		if failure := recover(); failure != nil {
			domain.MarkEffectUncertain(startupCtx)
			panic(failure)
		}
		if !startupFinished {
			_ = platform.FinishOperation(startupCtx, admission, permit, nil)
		}
	}()
	store, err := graph.Connect(startupCtx, cfg.Graph)
	if err != nil {
		return err
	}
	if len(telemetry) > 0 {
		store.SetTrace(telemetry[0].TraceIO)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(startupCtx), 5*time.Second)
		defer cancel()
		_ = store.Close(cleanup)
	}()
	repo := repository.NewEvidenceRepository(store)
	jobsRepo := repository.NewJobsRepository(store)
	jobs := service.NewJobsService(store, jobsRepo)
	lifecycle := service.NewLifecycleService(store, repository.NewLifecycleRepository(store), repository.NewIdentityRepository(store), jobsRepo, objects, journal, time.Now)
	if err = lifecycle.Recover(startupCtx); err != nil {
		return err
	}
	if err = platform.FinishOperation(startupCtx, admission, permit, nil); err != nil {
		return err
	}
	startupFinished = true
	health.Ready(service.NewHealthService(admission, store, objects, journal, 5*time.Second, mc.InstanceID))
	capture := service.NewCaptureService(store, repo, jobsRepo, objects)
	handlers := map[string]platform.RunJob{"capture.reconcile": func(ctx context.Context, job domain.Job, lease domain.Lease) error {
		return capture.Reconcile(ctx, job, lease, jobs)
	}, "lifecycle.purge": func(ctx context.Context, job domain.Job, lease domain.Lease) error {
		return lifecycle.Purge(ctx, job, lease, jobs)
	}, "lifecycle.reconcile": func(ctx context.Context, job domain.Job, lease domain.Lease) error {
		return lifecycle.ReconcilePolicy(ctx, job, lease, jobs)
	}}
	worker := platform.Worker{ID: os.Getenv("KB_WORKER_ID"), InstanceID: mc.InstanceID, Admission: admission, Kinds: []string{"capture.reconcile", "lifecycle.purge", "lifecycle.reconcile"}, Jobs: jobs, RunJob: platform.DispatchJobs(handlers), Logger: logger}
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
				checkCtx, stop := context.WithTimeout(domain.WithOperationOutcome(sweepCtx), 30*time.Second)
				sweepPermit, err := admission.Admit(checkCtx, mc.InstanceID, "sweep")
				if err != nil {
					stop()
					continue
				}
				err = lifecycle.Recover(checkCtx)
				if err == nil {
					_, err = lifecycle.Expire(checkCtx, time.Now().UTC())
				}
				if err == nil {
					err = capture.Sweep(checkCtx, 100)
				}
				err = platform.FinishOperation(checkCtx, admission, sweepPermit, err)
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

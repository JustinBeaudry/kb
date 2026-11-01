package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/handler"
	"github.com/JustinBeaudry/kb/server/internal/platform"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
)

func main() {
	telemetry, err := platform.NewTelemetry(os.Stderr, "api")
	if err != nil {
		os.Exit(1)
	}
	defer func() { _ = telemetry.Shutdown(context.Background()) }()
	http.DefaultTransport = telemetry.HTTPClientTransport(http.DefaultTransport)
	if err = run(context.Background(), telemetry.Logger(), telemetry); err != nil {
		telemetry.Logger().Error("server_stopped", "reason", "configuration_or_service_unavailable")
		os.Exit(1)
	}
}

type serverRuntime struct {
	api, health http.Handler
	readiness   readinessProbe
	close       func()
}

func run(parent context.Context, logger *slog.Logger, telemetry ...*platform.Telemetry) error {
	cfg, err := platform.LoadConfig()
	if err != nil {
		return err
	}
	key, err := platform.LoadRetrievalKey()
	if err != nil {
		return err
	}
	jc, err := platform.LoadJournalConfig()
	if err != nil {
		return err
	}
	journal, err := gateway.NewS3DeletionJournal(jc)
	if err != nil {
		return err
	}
	oc, err := platform.LoadObjectConfig()
	if err != nil {
		return err
	}
	objects, err := gateway.NewS3(oc)
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
	var runtime atomic.Pointer[serverRuntime]
	mux := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/livez" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"live":true}`))
			return
		}
		current := runtime.Load()
		if current == nil {
			platform.WriteUnready(w, "starting")
			return
		}
		if r.URL.Path == "/readyz" {
			current.health.ServeHTTP(w, r)
			return
		}
		current.api.ServeHTTP(w, r)
	}))
	if len(telemetry) > 0 {
		mux = telemetry[0].HTTPMiddleware(mux)
	}
	server := &http.Server{Addr: cfg.Address, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: cfg.ReadTimeout, WriteTimeout: cfg.WriteTimeout, IdleTimeout: cfg.IdleTimeout, MaxHeaderBytes: 16 << 10}
	served := make(chan error, 1)
	go func() {
		if cfg.TLSCert != "" {
			served <- server.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
		} else {
			served <- server.ListenAndServe()
		}
	}()
	initialized := make(chan error, 1)
	go func() {
		permit, err := platform.WaitForAdmission(ctx, admission, mc.InstanceID, "api-startup")
		if err != nil {
			initialized <- err
			return
		}
		operationCtx := domain.WithOperationOutcome(ctx)
		current, err := composeRuntime(operationCtx, cfg, key, objects, journal, admission, mc.InstanceID, logger, telemetry...)
		err = platform.FinishOperation(operationCtx, admission, permit, err)
		if err != nil {
			if current != nil {
				current.close()
			}
			initialized <- err
			return
		}
		// Start only after startup admission finished, using the process lifetime
		// rather than the startup operation's sticky outcome scope.
		current.startReadiness(ctx, readinessInterval, readinessMaxAge)
		runtime.Store(current)
		initialized <- nil
	}()
	startupDone := false
	defer func() {
		stop()
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = server.Shutdown(cleanup)
		if !startupDone {
			if startupErr := <-initialized; startupErr != nil {
				logger.Warn("startup_stopped", "reason", "configuration_or_service_unavailable")
			}
		}
		if current := runtime.Load(); current != nil {
			current.close()
		}
	}()
	for {
		select {
		case err := <-initialized:
			startupDone = true
			initialized = nil
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		case err := <-served:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-ctx.Done():
			return nil
		}
	}
}
func composeRuntime(ctx context.Context, cfg platform.Config, key []byte, objects *gateway.S3, journal *gateway.S3DeletionJournal, admission *gateway.MaintenanceHTTPClient, instance string, logger *slog.Logger, telemetry ...*platform.Telemetry) (*serverRuntime, error) {
	store, err := graph.Connect(ctx, cfg.Graph)
	if err != nil {
		return nil, err
	}
	if len(telemetry) > 0 {
		store.SetTrace(telemetry[0].TraceIO)
	}
	closeStore := func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = store.Close(cleanup)
	}
	complete := false
	defer func() {
		if !complete {
			closeStore()
		}
	}()
	lifecycle := service.NewLifecycleService(store, repository.NewLifecycleRepository(store), repository.NewIdentityRepository(store), repository.NewJobsRepository(store), objects, journal, time.Now)
	if err = lifecycle.Recover(ctx); err != nil {
		return nil, err
	}
	capture := service.NewCaptureService(store, repository.NewEvidenceRepository(store), repository.NewJobsRepository(store), objects)
	retrievalRepo := repository.NewRetrievalRepository(store)
	checkpointRepo := repository.NewCheckpointRepository(store)
	evidenceRepo := repository.NewEvidenceRepository(store)
	checkpoints := service.NewCheckpointService(store, checkpointRepo, retrievalRepo)
	memory := service.NewMemoryService(store, repository.NewMemoryRepository(store), retrievalRepo, evidenceRepo, capture)
	retrieval, err := service.NewRetrievalService(store, retrievalRepo, checkpointRepo, evidenceRepo, objects, key)
	if err != nil {
		return nil, err
	}
	status := service.NewStatusService(store, retrievalRepo)
	e, err := handler.NewServer(service.NewIdentityService(store, repository.NewIdentityRepository(store), gateway.NostrIdentity{}), capture, checkpoints, memory, retrieval, service.NewPolicyService(store, evidenceRepo, retrievalRepo), status, logger)
	if err != nil {
		return nil, err
	}
	complete = true
	return &serverRuntime{api: handler.GuardPublicAPI(platform.AdmittedHandler(admission, instance, e), logger), readiness: service.NewHealthService(admission, store, objects, journal, 5*time.Second, instance), close: closeStore}, nil
}

package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/handler"
	"github.com/JustinBeaudry/kb/server/internal/platform"
	"github.com/JustinBeaudry/kb/server/internal/service"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if run(ctx, os.Args[1:], os.Stdout) != nil {
		_, _ = io.WriteString(os.Stderr, "supervisor_unavailable: inspect protected status and docs/runbooks/recovery.md\n")
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, stdout io.Writer) error {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" || arg == "help" {
			_, err := io.WriteString(stdout, supervisorUsage)
			return err
		}
	}
	action := "serve"
	if len(args) > 0 {
		action = args[0]
		args = args[1:]
	}
	if action == "serve" {
		if len(args) > 0 {
			return domain.ErrInvalidInput
		}
		return serveSupervisor(ctx)
	}
	flags := flag.NewFlagSet("kb-supervisor", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.String("id", "", "operation ID")
	backup := flags.String("backup-id", "", "completed backup ID")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return domain.ErrInvalidInput
	}
	root := os.Getenv("KB_SUPERVISOR_ROOT")
	if root == "" {
		root = "/data/kb"
	}
	work, cancel := context.WithTimeout(ctx, 4*time.Hour)
	defer cancel()
	return callOperator(work, filepath.Join(root, "control", "backup.sock"), operatorRequest{Version: 1, Action: action, ID: *id, BackupID: *backup}, stdout)
}

const supervisorUsage = `Usage: kb-supervisor COMMAND [FLAGS]

  serve                             Run the private graph supervisor (default).
  status                            Inspect private maintenance, journal and disk status.
  backup --id BACKUP_ID              Drain and create an authenticated encrypted backup.
  pause --id RESTORE_ID              Close admission and drain before stopping services.
  restore --backup-id BACKUP_ID --id RESTORE_ID
                                    Restore under the exact paused fence with a fresh
                                    evidence bucket and rotated retrieval HMAC key.
  recover --id BACKUP_ID             Recover a failed backup only when effects are settled.
  settle --id PERMIT_ID              Remove a restored operation permit (listed by status)
                                    after proving its holder's process ended.

Operator commands run as the graph host runtime UID through its protected local
Unix socket. Restore startup uses KB_SUPERVISOR_RESTORE_MODE=1 and never resets
the live independent deletion journal. Unknown operations remain closed.
See docs/runbooks/recovery.md for protected configuration, exact retry and recovery.
`

func supervisorPrivateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return domain.ErrUnavailable
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return domain.ErrForbidden
	}
	info, err := os.Lstat(path)
	if err != nil {
		return domain.ErrUnavailable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || !info.IsDir() || info.Mode().Perm() != 0700 {
		return domain.ErrForbidden
	}
	return nil
}
func freshSupervisorRoot(root string) bool {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.Name() != "control" && entry.Name() != "scratch" {
			return false
		}
		children, err := os.ReadDir(filepath.Join(root, entry.Name()))
		if err != nil || len(children) != 0 {
			return false
		}
	}
	return true
}
func serveSupervisor(parent context.Context) error {
	cfg, err := loadSupervisorConfig()
	if err != nil {
		return err
	}
	telemetry, err := platform.NewTelemetry(os.Stderr, "graph")
	if err != nil {
		return err
	}
	defer func() { _ = telemetry.Shutdown(context.Background()) }()
	logger := telemetry.Logger()
	http.DefaultTransport = telemetry.HTTPClientTransport(http.DefaultTransport)
	if err = supervisorPrivateDir(cfg.Root); err != nil {
		return err
	}
	fresh := freshSupervisorRoot(cfg.Root)
	for _, dir := range []string{"control", "scratch"} {
		if err = supervisorPrivateDir(filepath.Join(cfg.Root, dir)); err != nil {
			return err
		}
	}
	stateStore, err := gateway.NewMaintenanceStateStore(filepath.Join(cfg.Root, "maintenance.json"), fresh)
	if err != nil {
		return err
	}
	defer func() { _ = stateStore.Close() }()
	maintenance, err := service.NewMaintenanceService(parent, stateStore)
	if err != nil {
		return err
	}
	backupState, err := gateway.NewBackupStateStore(filepath.Join(cfg.Root, "backup.json"), fresh)
	if err != nil {
		return err
	}
	defer func() { _ = backupState.Close() }()
	objectsConfig, err := platform.LoadObjectConfig()
	if err != nil {
		return err
	}
	// Bind the namespace to the actual configured bucket identity, rather than
	// trusting an operator-supplied alias to prove a fresh restore destination.
	cfg.EvidenceNamespace = "evidence-" + service.TokenDigest(objectsConfig.Endpoint+"\x00"+objectsConfig.Bucket)
	journalConfig, err := platform.LoadJournalConfig()
	if err != nil {
		return err
	}
	if objectsConfig.Bucket == cfg.BackupStore.Bucket && objectsConfig.Endpoint == cfg.BackupStore.Endpoint || journalConfig.Bucket == cfg.BackupStore.Bucket && journalConfig.Endpoint == cfg.BackupStore.Endpoint {
		return domain.ErrInvalidInput
	}
	for _, backupKey := range cfg.Archive.AuthorityKeys {
		for _, journalKey := range journalConfig.Keys {
			if backupKey == journalKey {
				return domain.ErrInvalidInput
			}
		}
	}
	objects, err := gateway.NewS3(objectsConfig)
	if err != nil {
		return err
	}
	journal, err := gateway.NewS3DeletionJournal(journalConfig)
	if err != nil {
		return err
	}
	archiveStore, err := gateway.NewS3BackupStore(cfg.BackupStore)
	if err != nil {
		return err
	}
	archive, err := gateway.NewBackupArchive(archiveStore, objects, cfg.Archive)
	if err != nil {
		return err
	}
	// The graph and the control servers outlive the shutdown signal until admitted
	// operations drain; cancel ends them.
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	defer cancel()
	process, err := gateway.NewNeo4jProcess(gateway.Neo4jProcessConfig{Root: filepath.Join(cfg.Root, "graph"), Home: "/var/lib/neo4j", Neo4jBinary: "/var/lib/neo4j/bin/neo4j", AdminBinary: "/var/lib/neo4j/bin/neo4j-admin", JavaBinary: "/opt/java/openjdk/bin/java", AuthClassPath: "/opt/kb:/var/lib/neo4j/lib/*", GraphSecret: cfg.Platform.Graph.Secret}, backupState, ctx)
	if err != nil {
		return err
	}
	if err = process.Start(parent); err != nil {
		return err
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 60*time.Second)
		defer stop()
		_ = process.Stop(cleanup)
		_ = process.WaitStopped(cleanup)
	}()
	snapshot, err := newRuntimeSnapshot(cfg, objects, journal, archive)
	if err != nil {
		return err
	}
	snapshot.trace = telemetry.TraceIO
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = snapshot.Close(cleanup)
	}()
	backup, err := service.NewBackupService(maintenance, snapshot, process, archive, backupState, service.BackupConfig{ScratchRoot: cfg.Archive.ScratchRoot, EvidenceNamespace: cfg.EvidenceNamespace, InstanceID: cfg.InstanceID}, time.Now)
	if err != nil {
		return err
	}
	scheduler, err := platform.NewBackupSupervisor(backupState, backup, maintenance, time.Now)
	if err != nil {
		return err
	}
	// Successful scheduled backups leave RPO evidence in provider logs: the gap
	// since the previous snapshot and the run duration, never identifiers.
	logBackupEvidence := func(e platform.BackupEvidence) {
		logger.Info("backup_completed", "backup_age_seconds", int64(e.RecoveryPointGap/time.Second), "duration_ms", e.Duration.Milliseconds())
	}
	scheduler.OnComplete(logBackupEvidence)
	adminExecutor, err := gateway.NewHostAdminProcessExecutor("/usr/local/bin/kb-admin", maintenance)
	if err != nil {
		return err
	}
	admin := service.NewHostAdminService(maintenance, adminExecutor, cfg.InstanceID)
	adminServer, err := gateway.NewUnixHostAdminServer(filepath.Join(cfg.Root, "control", "admin.sock"), os.Geteuid(), admin)
	if err != nil {
		return err
	}
	defer func() { _ = adminServer.Close() }()
	operatorListener, err := gateway.ListenProtectedUnix(filepath.Join(cfg.Root, "control", "backup.sock"), os.Geteuid())
	if err != nil {
		return err
	}
	defer func() { _ = operatorListener.Close() }()
	status := func(ctx context.Context) (operatorStatus, error) {
		ctx = domain.WithOperationOutcome(ctx)
		m, e := maintenance.State(ctx)
		if e != nil {
			return operatorStatus{}, e
		}
		op, e := backupState.LoadOperation(ctx)
		if e != nil {
			return operatorStatus{}, e
		}
		schedule, e := backupState.LoadSchedule(ctx)
		if e != nil {
			return operatorStatus{}, e
		}
		out := operatorStatus{Maintenance: m, Operation: op, Schedule: schedule}
		if out.RestoredPermits, e = maintenance.Restored(ctx); e != nil {
			return operatorStatus{}, e
		}
		out.HostReady = snapshot.HostReady() && (m.Phase == "bootstrap" || m.Phase == "recovering" || m.Phase == "open")
		available, total, spaceErr := gateway.BackupDiskSpace(cfg.Root)
		if spaceErr != nil {
			return out, spaceErr
		}
		out.DiskAvailableBytes = available
		out.DiskTotalBytes = total
		out.DiskUsageWarning = total > 0 && available <= total/4
		if !m.LastSuccessfulBackup.IsZero() {
			out.BackupAgeSeconds = max(0, int64(time.Since(m.LastSuccessfulBackup).Seconds()))
		}
		if m.Phase == "open" {
			if e := sampleOpenJournal(ctx, maintenance, journal, cfg.InstanceID, &out); e != nil {
				return out, e
			}
			out.HostReady = snapshot.HostReady() && out.Maintenance.Phase == "open"
		}
		return out, nil
	}
	health := service.NewHealthService(maintenance, snapshot, recoveryObjectsProbe{objects, archive}, journal, 2*time.Second, cfg.InstanceID)
	healthHandler := handler.NewHealthHandler(health)
	control, err := handler.NewMaintenanceHandler(maintenance, cfg.ControlSecret)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/livez", healthHandler)
	mux.Handle("/readyz", healthHandler)
	mux.Handle("/v1/", control)
	server := &http.Server{Addr: cfg.ControlAddress, Handler: telemetry.HTTPMiddleware(mux), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	results := make(chan error, 3)
	go func() { results <- adminServer.Serve(ctx) }()
	go func() {
		results <- serveOperator(ctx, operatorListener, evidenceRunner{operatorRunner: backup, state: maintenance, observe: logBackupEvidence, settle: maintenance.SettleRestored}, status)
	}()
	go func() { results <- server.ListenAndServe() }()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	closed := closedReporter{log: func(stage, class string, unknown int) {
		logger.Warn("maintenance_closed", "reason", "maintenance", "stage", stage, "error_class", class, "unknown_operations", int64(unknown))
	}}
	for {
		select {
		case <-parent.Done():
			// A deploy stops this process with SIGTERM and a draining window.
			// Refuse new operations and give admitted ones time to finish while
			// the graph is still up, so the restart does not leave them unknown.
			maintenance.Quiesce()
			drain, stopDrain := context.WithTimeout(context.Background(), shutdownDrain)
			if maintenance.WaitIdle(drain) != nil {
				logger.Warn("shutdown_drain_incomplete", "reason", "maintenance")
			}
			stopDrain()
			cancel()
			cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
			_ = server.Shutdown(cleanup)
			stop()
			return nil
		case err := <-results:
			if err != nil {
				return domain.ErrUnavailable
			}
		case <-ticker.C:
			state, e := maintenance.State(ctx)
			if e != nil {
				return e
			}
			operation, operationErr := backupState.LoadOperation(ctx)
			if operationErr != nil {
				return operationErr
			}
			pending := operation.ID == state.Fence.OperationID && operation.Stage != "complete" && operation.Stage != "recovered"
			startupFence := state.Fence.OperationID == "bootstrap" || strings.HasPrefix(state.Fence.OperationID, "startup-")
			if (state.Phase == "bootstrap" || state.Phase == "recovering") && startupFence && !pending && os.Getenv("KB_SUPERVISOR_RESTORE_MODE") != "1" {
				if e = startupRecovery(ctx, maintenance, snapshot, cfg.InstanceID); e != nil {
					closed.report(time.Now(), platform.FailureStage(e), platform.ErrorClass(e), state.Unknown)
				} else if e = backup.CheckpointStartup(ctx); e != nil {
					return e
				} else {
					closed = closedReporter{log: closed.log}
				}
			} else if state.Phase == "open" {
				work, stop := context.WithTimeout(parent, 4*time.Hour)
				e = scheduler.Tick(work)
				stop()
				if errors.Is(e, platform.ErrScheduleNotSaved) {
					logger.Error("backup_schedule_unsaved", "reason", "unavailable")
				} else if e != nil {
					logger.Error("backup_failed", "reason", "unavailable")
				}
			}
		}
	}
}

type recoveryObjectsProbe struct {
	objects *gateway.S3
	archive *gateway.BackupArchiveGateway
}

func (p recoveryObjectsProbe) Probe(ctx context.Context) error {
	if err := p.objects.Probe(ctx); err != nil {
		return err
	}
	return p.archive.CheckRecoveryKeys(ctx)
}

type startupSnapshot interface {
	InitializeAndRecover(context.Context) error
	bootstrapPrerequisites(context.Context) error
	ReconcileJournal(context.Context) (domain.JournalBarrier, error)
}

func startupRecovery(ctx context.Context, m *service.MaintenanceService, snapshot startupSnapshot, instance string) error {
	ctx = domain.WithOperationOutcome(ctx)
	state, err := m.State(ctx)
	if err != nil {
		return platform.AtStage("maintenance", err)
	}
	permit, err := m.AdmitRecovery(ctx, state.Fence, instance, "startup-recovery")
	if err != nil {
		return platform.AtStage("admission", err)
	}
	if err = snapshot.InitializeAndRecover(ctx); err != nil {
		err = platform.AtStage("graph_connect", err)
		if domain.OperationSettled(ctx) {
			_ = m.Finish(context.WithoutCancel(ctx), permit)
		} else {
			_ = m.MarkUnknown(context.WithoutCancel(ctx), permit)
		}
		return err
	}
	if err = snapshot.bootstrapPrerequisites(ctx); err != nil {
		err = platform.AtStage("bootstrap_prerequisites", err)
		if domain.OperationSettled(ctx) {
			_ = m.Finish(context.WithoutCancel(ctx), permit)
		} else {
			_ = m.MarkUnknown(context.WithoutCancel(ctx), permit)
		}
		return err
	}
	if _, err = snapshot.ReconcileJournal(ctx); err != nil {
		err = platform.AtStage("journal", err)
		if domain.OperationSettled(ctx) {
			_ = m.Finish(context.WithoutCancel(ctx), permit)
		} else {
			_ = m.MarkUnknown(context.WithoutCancel(ctx), permit)
		}
		return err
	}
	if !domain.OperationSettled(ctx) {
		_ = m.MarkUnknown(context.WithoutCancel(ctx), permit)
		return platform.AtStage("finish_operation", domain.ErrUnknownOutcome)
	}
	if err = m.Finish(ctx, permit); err != nil {
		return platform.AtStage("finish_operation", err)
	}
	if err = m.SetPhase(ctx, state.Fence, "recovering"); err != nil {
		return platform.AtStage("maintenance", err)
	}
	if err = m.MarkRecoveryComplete(ctx, state.Fence); err != nil {
		return platform.AtStage("maintenance", err)
	}
	return platform.AtStage("maintenance", m.Resume(ctx, state.Fence))
}

// shutdownDrain bounds how long a stopping supervisor waits for admitted
// operations. It stays inside the graph service's 120 second draining window.
const shutdownDrain = 90 * time.Second

// closedReporter logs a failed startup recovery when its stage, class or
// unknown count changes, and at most every five minutes otherwise, so a long
// closure stays visible at warn level without a line every tick.
type closedReporter struct {
	log          func(stage, class string, unknown int)
	stage, class string
	unknown      int
	last         time.Time
}

func (c *closedReporter) report(now time.Time, stage, class string, unknown int) {
	if !c.last.IsZero() && stage == c.stage && class == c.class && unknown == c.unknown && now.Sub(c.last) < 5*time.Minute {
		return
	}
	c.stage, c.class, c.unknown, c.last = stage, class, unknown, now
	c.log(stage, class, unknown)
}

package service

import (
	"context"
	"crypto/rand"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// BackupMaintenance is an in-process supervisor capability. HTTP service-role
// admission must never implement Begin/Resume or recovery completion.
type BackupMaintenance interface {
	Begin(context.Context, string) (domain.MaintenanceFence, error)
	WaitDrained(context.Context, domain.MaintenanceFence) error
	State(context.Context) (domain.MaintenanceState, error)
	SetPhase(context.Context, domain.MaintenanceFence, string) error
	Resume(context.Context, domain.MaintenanceFence) error
	AdmitRecovery(context.Context, domain.MaintenanceFence, string, string) (domain.OperationPermit, error)
	Finish(context.Context, domain.OperationPermit) error
	MarkUnknown(context.Context, domain.OperationPermit) error
	MarkRecoveryComplete(context.Context, domain.MaintenanceFence) error
	RecordBackup(context.Context, domain.MaintenanceFence, string, time.Time) error
}
type BackupSnapshot interface {
	ReconcileJournal(context.Context) (domain.JournalBarrier, error)
	CaptureManifest(context.Context, string) (domain.BackupManifest, error)
	VerifyRestore(context.Context, domain.BackupManifest) error
	InvalidateRestoredClients(context.Context, string) error
	InitializeAndRecover(context.Context) error
	ValidateRestoredSnapshot(context.Context, domain.BackupManifest) error
}
type Neo4jProcess interface {
	Stop(context.Context) error
	WaitStopped(context.Context) error
	Dump(context.Context, string) (string, string, error)
	Load(context.Context, string, string) error
	Start(context.Context) error
}
type BackupArchive interface {
	Write(context.Context, domain.BackupManifest, string, string) error
	Verify(context.Context, string) (domain.BackupManifest, error)
	Restore(context.Context, string, string) (domain.BackupManifest, error)
	Retain(context.Context, time.Time) error
}
type BackupOperationStore interface {
	LoadOperation(context.Context) (domain.BackupOperation, error)
	SaveOperation(context.Context, domain.BackupOperation) error
}
type BackupConfig struct{ ScratchRoot, EvidenceNamespace, InstanceID string }
type BackupService struct {
	mu          sync.Mutex
	maintenance BackupMaintenance
	snapshot    BackupSnapshot
	process     Neo4jProcess
	archive     BackupArchive
	operations  BackupOperationStore
	config      BackupConfig
	now         func() time.Time
}

func NewBackupService(m BackupMaintenance, s BackupSnapshot, p Neo4jProcess, a BackupArchive, o BackupOperationStore, c BackupConfig, now func() time.Time) (*BackupService, error) {
	if m == nil || s == nil || p == nil || a == nil || o == nil || !filepath.IsAbs(c.ScratchRoot) || domain.ValidateIDs(c.EvidenceNamespace, c.InstanceID) != nil {
		return nil, domain.ErrInvalidInput
	}
	if now == nil {
		now = time.Now
	}
	return &BackupService{maintenance: m, snapshot: s, process: p, archive: a, operations: o, config: c, now: now}, nil
}
func (s *BackupService) stage(ctx context.Context, op *domain.BackupOperation, stage string) error {
	op.Stage = stage
	return s.operations.SaveOperation(ctx, *op)
}
func (s *BackupService) begin(ctx context.Context, id, backup, kind string) (domain.BackupOperation, error) {
	if domain.ValidateIDs(id, backup) != nil || !safeBackupID(id) || !safeBackupID(backup) {
		return domain.BackupOperation{}, domain.ErrInvalidInput
	}
	prior, err := s.operations.LoadOperation(ctx)
	if err != nil {
		return domain.BackupOperation{}, err
	}
	pausedRestore := prior.ID == id && prior.Kind == "pause" && prior.Stage == "paused" && kind == "restore"
	if prior.ID == id && !pausedRestore && (prior.Kind != kind || prior.BackupID != backup) {
		return domain.BackupOperation{}, domain.ErrConflict
	}
	if prior.ID != "" && prior.Stage != "complete" && prior.Stage != "recovered" && !pausedRestore && (prior.ID != id || prior.Kind != kind || prior.BackupID != backup) {
		return domain.BackupOperation{}, domain.ErrConflict
	}
	fence, err := s.maintenance.Begin(ctx, id)
	if err != nil {
		return domain.BackupOperation{}, err
	}
	op := domain.BackupOperation{Version: 1, ID: id, BackupID: backup, Kind: kind, Fence: fence, StartedAt: s.now().UTC(), Directory: filepath.Join(s.config.ScratchRoot, kind+"-"+id), TerminalState: "complete"}
	if prior.ID == id && prior.Kind == kind && prior.BackupID == backup {
		op.VerifiedSnapshot = prior.VerifiedSnapshot
		op.StartedAt = prior.StartedAt
	}
	if kind == "restore" {
		if prior.ID == id && prior.Kind == kind && prior.BackupID == backup {
			if prior.RestoreIncarnation == "" {
				return op, domain.ErrUnavailable
			}
			op.RestoreIncarnation = prior.RestoreIncarnation
		} else {
			op.RestoreIncarnation = "restore-" + rand.Text()
		}
		// The incarnation lives only in supervisor state, outside the restored DB.
		// It also binds fresh staging and graph targets, even if an operator ID is
		// reused after a later backup overwrote the last-operation record.
		op.Directory = filepath.Join(s.config.ScratchRoot, op.RestoreIncarnation)
	}
	if err = s.stage(ctx, &op, "draining"); err != nil {
		return op, err
	}
	if err = s.maintenance.WaitDrained(ctx, fence); err != nil {
		return op, err
	}
	return op, nil
}
func safeBackupID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			continue
		}
		return false
	}
	return true
}
func (s *BackupService) fail(ctx context.Context, op *domain.BackupOperation, err error) error {
	// A fresh bounded context persists failure even if the requesting client left.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	op.FailedStage = op.Stage
	_ = s.stage(cleanup, op, "failed")
	_ = s.maintenance.SetPhase(cleanup, op.Fence, "failed")
	return err
}
func (s *BackupService) Run(ctx context.Context, id string) (domain.BackupResult, error) {
	ctx = domain.WithOperationOutcome(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if result, ok, err := s.completedOperation(ctx, id, id, "backup"); err != nil {
		return result, err
	} else if ok {
		return result, nil
	}
	op, err := s.begin(ctx, id, id, "backup")
	if err != nil {
		return domain.BackupResult{}, err
	}
	result := domain.BackupResult{ID: id, StartedAt: op.StartedAt, State: "failed"}
	run := func() error {
		barrier, e := s.snapshot.ReconcileJournal(ctx)
		if e != nil {
			return e
		}
		manifest, e := s.snapshot.CaptureManifest(ctx, id)
		if e != nil {
			return e
		}
		manifest.BackupID = id
		manifest.CreatedAt = op.StartedAt
		manifest.Journal = barrier
		if !domain.OperationSettled(ctx) {
			return domain.ErrUnknownOutcome
		}
		if e = s.stage(ctx, &op, "offline"); e != nil {
			return e
		}
		if e = s.maintenance.SetPhase(ctx, op.Fence, "offline"); e != nil {
			return e
		}
		if e = s.process.Stop(ctx); e != nil {
			return e
		}
		if e = s.process.WaitStopped(ctx); e != nil {
			return e
		}
		if sizer, ok := s.process.(interface {
			DiskBytes(context.Context) (int64, error)
		}); ok {
			manifest.GraphStoreBytes, e = sizer.DiskBytes(ctx)
			if e != nil {
				return e
			}
		}
		if e = s.stage(ctx, &op, "dumping"); e != nil {
			return e
		}
		data, system, e := s.process.Dump(ctx, op.Directory)
		if e != nil {
			return e
		}
		if e = s.stage(ctx, &op, "archiving"); e != nil {
			return e
		}
		if e = s.archive.Write(ctx, manifest, data, system); e != nil {
			return e
		}
		verified, e := s.archive.Verify(ctx, id)
		if e != nil {
			return e
		}
		if cleanup, ok := s.archive.(interface {
			CleanupBackupDirectory(context.Context, string) error
		}); ok {
			if e = cleanup.CleanupBackupDirectory(ctx, op.Directory); e != nil {
				return e
			}
		}
		if e = s.maintenance.RecordBackup(ctx, op.Fence, id, op.StartedAt); e != nil {
			return e
		}
		if e = s.archive.Retain(ctx, s.now().UTC().Add(-30*24*time.Hour)); e != nil {
			return e
		}
		if e = s.recoverOnline(ctx, &op, "backup-recovery", func() error {
			if e := s.snapshot.InitializeAndRecover(ctx); e != nil {
				return e
			}
			_, e := s.snapshot.ReconcileJournal(ctx)
			return e
		}); e != nil {
			return e
		}
		_ = verified
		return nil
	}
	if err = run(); err != nil {
		return result, s.fail(ctx, &op, err)
	}
	op.CompletedAt = s.now().UTC()
	if err = s.stage(ctx, &op, "complete"); err != nil {
		return result, err
	}
	result.CompletedAt = op.CompletedAt
	result.State = "complete"
	return result, nil
}
func (s *BackupService) recoverOnline(ctx context.Context, op *domain.BackupOperation, kind string, recover func() error) error {
	if op.TerminalState != "complete" && op.TerminalState != "recovered" {
		return domain.ErrUnavailable
	}
	if err := s.stage(ctx, op, "starting"); err != nil {
		return err
	}
	if err := s.process.Start(ctx); err != nil {
		return err
	}
	if err := s.maintenance.SetPhase(ctx, op.Fence, "recovering"); err != nil {
		return err
	}
	if err := s.stage(ctx, op, "recovering"); err != nil {
		return err
	}
	permit, err := s.maintenance.AdmitRecovery(ctx, op.Fence, s.config.InstanceID, kind)
	if err != nil {
		return err
	}
	// Failed recovery retains its permit as unresolved. A caller must not infer
	// drain from cancellation, nor clear it merely to make a retry work.
	if err = recover(); err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if domain.OperationSettled(ctx) {
			_ = s.maintenance.Finish(cleanup, permit)
		} else {
			_ = s.maintenance.MarkUnknown(cleanup, permit)
		}
		return err
	}
	if !domain.OperationSettled(ctx) {
		_ = s.maintenance.MarkUnknown(context.WithoutCancel(ctx), permit)
		return domain.ErrUnknownOutcome
	}
	if err = s.maintenance.Finish(ctx, permit); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return domain.ErrUnavailable
	}
	if err = s.maintenance.MarkRecoveryComplete(ctx, op.Fence); err != nil {
		return err
	}
	if err = s.stage(ctx, op, "resuming"); err != nil {
		return err
	}
	return s.maintenance.Resume(ctx, op.Fence)
}
func (s *BackupService) Restore(ctx context.Context, backupID, restoreID string) (domain.BackupResult, error) {
	ctx = domain.WithOperationOutcome(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if result, ok, err := s.completedOperation(ctx, restoreID, backupID, "restore"); err != nil {
		return result, err
	} else if ok {
		return result, nil
	}
	if cleanup, ok := s.archive.(interface{ CleanupRestore() error }); ok {
		defer func() { _ = cleanup.CleanupRestore() }()
	}
	op, err := s.begin(ctx, restoreID, backupID, "restore")
	if err != nil {
		return domain.BackupResult{}, err
	}
	result := domain.BackupResult{ID: restoreID, StartedAt: op.StartedAt, State: "failed"}
	run := func() error {
		if e := s.stage(ctx, &op, "decrypting"); e != nil {
			return e
		}
		manifest, e := s.archive.Restore(ctx, backupID, op.Directory)
		if e != nil {
			return e
		}
		if manifest.EvidenceNamespace == "" || manifest.EvidenceNamespace == s.config.EvidenceNamespace {
			return domain.ErrConflict
		}
		if validator, ok := s.snapshot.(interface {
			ValidateBackup(context.Context, domain.BackupManifest) error
		}); ok {
			if e = validator.ValidateBackup(ctx, manifest); e != nil {
				return e
			}
		}
		if e = s.stage(ctx, &op, "offline"); e != nil {
			return e
		}
		if e = s.maintenance.SetPhase(ctx, op.Fence, "offline"); e != nil {
			return e
		}
		if e = s.process.Stop(ctx); e != nil {
			return e
		}
		if e = s.process.WaitStopped(ctx); e != nil {
			return e
		}
		if e = s.stage(ctx, &op, "loading"); e != nil {
			return e
		}
		if e = s.process.Load(ctx, filepath.Join(op.Directory, "neo4j.dump"), filepath.Join(op.Directory, "system.dump")); e != nil {
			return e
		}
		return s.recoverOnline(ctx, &op, "restore-recovery", func() error {
			// InitializeAndRecover establishes schema/connection only for restore. The
			// live journal is separately reconciled after every old client is revoked.
			if e := s.snapshot.InitializeAndRecover(ctx); e != nil {
				return e
			}
			if !op.VerifiedSnapshot {
				if e := s.snapshot.ValidateRestoredSnapshot(ctx, manifest); e != nil {
					return e
				}
				op.VerifiedSnapshot = true
				if e := s.stage(ctx, &op, "snapshot-verified"); e != nil {
					return e
				}
			}
			if e := s.snapshot.InvalidateRestoredClients(ctx, op.RestoreIncarnation); e != nil {
				return e
			}
			if _, e := s.snapshot.ReconcileJournal(ctx); e != nil {
				return e
			}
			if err := s.snapshot.VerifyRestore(ctx, manifest); err != nil {
				return err
			}
			if cleanup, ok := s.archive.(interface{ CleanupRestore() error }); ok {
				return cleanup.CleanupRestore()
			}
			return nil
		})
	}
	if err = run(); err != nil {
		return result, s.fail(ctx, &op, err)
	}
	op.CompletedAt = s.now().UTC()
	if err = s.stage(ctx, &op, "complete"); err != nil {
		return result, err
	}
	result.CompletedAt = op.CompletedAt
	result.State = "complete"
	return result, nil
}

// Recover abandons only a failed backup attempt, retaining its immutable partial
// archive for inspection. Restore failures require their exact restore workflow;
// neither a timeout nor an operator retry clears unresolved source operations.
func completedBackupResult(op domain.BackupOperation) domain.BackupResult {
	return domain.BackupResult{ID: op.ID, StartedAt: op.StartedAt, CompletedAt: op.CompletedAt, State: "complete"}
}

func (s *BackupService) Pause(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !safeBackupID(id) {
		return domain.ErrInvalidInput
	}
	fence, err := s.maintenance.Begin(ctx, id)
	if err != nil {
		return err
	}
	if err = s.maintenance.WaitDrained(ctx, fence); err != nil {
		return err
	}
	if err = s.maintenance.SetPhase(ctx, fence, "recovering"); err != nil {
		return err
	}
	op := domain.BackupOperation{Version: 1, ID: id, Kind: "pause", Fence: fence, StartedAt: s.now().UTC()}
	return s.stage(ctx, &op, "paused")
}

func (s *BackupService) Recover(ctx context.Context, id string) error {
	ctx = domain.WithOperationOutcome(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	op, err := s.operations.LoadOperation(ctx)
	if err != nil {
		return err
	}
	if op.ID != id || op.Kind != "backup" || op.Stage == "complete" {
		return domain.ErrConflict
	}
	if op.Stage == "recovered" {
		return nil
	}
	if finalized, err := s.finalizeResumedOperation(ctx, &op); err != nil {
		return err
	} else if finalized {
		if op.Stage == "recovered" {
			return nil
		}
		return domain.ErrConflict
	}
	state, err := s.maintenance.State(ctx)
	if err != nil {
		return err
	}
	if state.Fence != op.Fence || state.Unknown != 0 || state.Active != 0 {
		return domain.ErrMaintenance
	}
	if err = s.maintenance.WaitDrained(ctx, op.Fence); err != nil {
		return err
	}
	op.TerminalState = "recovered"
	if err = s.stage(ctx, &op, "recovery-requested"); err != nil {
		return err
	}
	if err = s.recoverOnline(ctx, &op, "backup-recovery", func() error {
		if e := s.snapshot.InitializeAndRecover(ctx); e != nil {
			return e
		}
		_, e := s.snapshot.ReconcileJournal(ctx)
		return e
	}); err != nil {
		return s.fail(ctx, &op, err)
	}
	op.CompletedAt = s.now().UTC()
	return s.stage(ctx, &op, "recovered")
}

func (s *BackupService) completedOperation(ctx context.Context, id, backup, kind string) (domain.BackupResult, bool, error) {
	op, err := s.operations.LoadOperation(ctx)
	if err != nil {
		return domain.BackupResult{}, false, err
	}
	if op.ID != id || op.BackupID != backup || op.Kind != kind {
		return domain.BackupResult{}, false, nil
	}
	if _, err = s.finalizeResumedOperation(ctx, &op); err != nil {
		return domain.BackupResult{}, false, err
	}
	if op.Stage == "recovered" || op.TerminalState == "recovered" {
		return domain.BackupResult{}, false, domain.ErrConflict
	}
	if op.Stage == "complete" {
		if op.TerminalState != "complete" {
			return domain.BackupResult{}, false, domain.ErrUnavailable
		}
		return completedBackupResult(op), true, nil
	}
	return domain.BackupResult{}, false, nil
}

// CheckpointStartup is called only after supervisor startup has independently
// recovered current graph/journal/dependencies and reopened a new startup fence.
// A persisted resuming phase proves the original workflow finished verification;
// a newer startup fence proves its prior Resume became durable before a crash.
func (s *BackupService) CheckpointStartup(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, err := s.operations.LoadOperation(ctx)
	if err != nil {
		return err
	}
	resumed := op.Stage == "resuming" || op.Stage == "failed" && op.FailedStage == "resuming"
	if !resumed {
		return nil
	}
	state, err := s.maintenance.State(ctx)
	if err != nil {
		return err
	}
	if state.Phase != "open" || !strings.HasPrefix(state.Fence.OperationID, "startup-") || state.Fence.Generation <= op.Fence.Generation {
		return domain.ErrMaintenance
	}
	if op.TerminalState != "complete" && op.TerminalState != "recovered" {
		return domain.ErrUnavailable
	}
	op.CompletedAt = s.now().UTC()
	return s.stage(ctx, &op, op.TerminalState)
}

// A durable resume checkpoint carries its intended terminal disposition. An
// abandonment recovery must never be reinterpreted as an existing archive.
func (s *BackupService) finalizeResumedOperation(ctx context.Context, op *domain.BackupOperation) (bool, error) {
	resumed := op.Stage == "resuming" || op.Stage == "failed" && op.FailedStage == "resuming"
	if !resumed {
		return false, nil
	}
	if op.TerminalState != "complete" && op.TerminalState != "recovered" {
		return false, domain.ErrUnavailable
	}
	state, err := s.maintenance.State(ctx)
	if err != nil {
		return false, err
	}
	if state.Phase != "open" || state.Fence != op.Fence {
		return false, nil
	}
	op.CompletedAt = s.now().UTC()
	if err = s.stage(ctx, op, op.TerminalState); err != nil {
		return false, err
	}
	return true, nil
}

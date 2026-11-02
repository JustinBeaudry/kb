package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/platform"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/JustinBeaudry/kb/server/migrations"
)

// Every online epoch obtains a fresh driver after a process stop/load/start.
// No production connection implicitly migrates; caller holds recovery admission.
type runtimeSnapshot struct {
	schemaReady atomic.Bool
	mu          sync.Mutex
	cfg         supervisorConfig
	objects     *gateway.S3
	journal     *gateway.S3DeletionJournal
	archive     *gateway.BackupArchiveGateway
	store       *graph.Store
	snapshot    *service.BackupSnapshotService
	metadata    domain.BackupManifest
	trace       func(context.Context, string, func(context.Context) error) error
}

func newRuntimeSnapshot(c supervisorConfig, o *gateway.S3, j *gateway.S3DeletionJournal, a *gateway.BackupArchiveGateway) (*runtimeSnapshot, error) {
	path := os.Getenv("KB_TOOL_VERSIONS_FILE")
	if path == "" {
		path = "/opt/kb/versions.json"
	}
	tools, e := os.ReadFile(path)
	if e != nil || len(tools) > 1<<20 {
		return nil, domain.ErrInvalidInput
	}
	digest := func(b []byte) string { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }
	schema := migrations.Identity + "\n" + migrations.Clients + "\n" + migrations.Jobs + "\n" + migrations.Evidence + "\n" + migrations.Memory + "\n" + migrations.Verification + "\n" + migrations.Lifecycle
	key, e := platform.LoadRetrievalKey()
	if e != nil {
		return nil, e
	}
	return &runtimeSnapshot{cfg: c, objects: o, journal: j, archive: a, metadata: domain.BackupManifest{SchemaDigest: digest([]byte(schema)), ToolVersionsDigest: digest(tools), Architecture: runtime.GOOS + "/" + runtime.GOARCH, EvidenceNamespace: c.EvidenceNamespace, RetrievalKeyDigest: digest(key)}}, nil
}
func (r *runtimeSnapshot) InitializeAndRecover(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.store != nil {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_ = r.store.Close(closeCtx)
		cancel()
		r.store = nil
		r.snapshot = nil
	}
	connectCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var store *graph.Store
	var err error
	for {
		if r.trace == nil {
			store, err = graph.Connect(connectCtx, r.cfg.Platform.Graph)
		} else {
			err = r.trace(connectCtx, "graph_connect", func(ctx context.Context) error {
				var e error
				store, e = graph.Connect(ctx, r.cfg.Platform.Graph)
				return e
			})
		}
		if err == nil {
			if err = store.Probe(connectCtx); err == nil {
				break
			}
			_ = store.Close(context.WithoutCancel(connectCtx))
			store = nil
		}
		select {
		case <-connectCtx.Done():
			return domain.ErrUnavailable
		case <-time.After(250 * time.Millisecond):
		}
	}
	if r.trace != nil {
		store.SetTrace(r.trace)
	}
	if err = store.InitializeSchema(connectCtx); err != nil {
		_ = store.Close(context.WithoutCancel(ctx))
		return err
	}
	lifecycle := service.NewLifecycleService(store, repository.NewLifecycleRepository(store), repository.NewIdentityRepository(store), repository.NewJobsRepository(store), r.objects, r.journal, time.Now)
	snapshot, err := service.NewBackupSnapshotService(store, repository.NewBackupRepository(store), lifecycle, r.journal, r.objects, r.archive, store.InitializeSchema, r.metadata, time.Now)
	if err != nil {
		_ = store.Close(context.WithoutCancel(ctx))
		return err
	}
	r.store = store
	r.snapshot = snapshot
	r.schemaReady.Store(true)
	return nil
}
func (r *runtimeSnapshot) Close(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.store == nil {
		return nil
	}
	err := r.store.Close(ctx)
	r.store = nil
	r.snapshot = nil
	return err
}
func (r *runtimeSnapshot) ReconcileJournal(ctx context.Context) (domain.JournalBarrier, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.snapshot == nil {
		return domain.JournalBarrier{}, domain.ErrUnavailable
	}
	b, err := r.snapshot.ReconcileJournal(ctx)
	if err != nil {
		return b, err
	}
	if err = r.objects.Probe(ctx); err != nil {
		return b, err
	}
	if err = r.archive.CheckRecoveryKeys(ctx); err != nil {
		return b, err
	}
	return b, nil
}
func (r *runtimeSnapshot) CaptureManifest(ctx context.Context, id string) (domain.BackupManifest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.snapshot == nil {
		return domain.BackupManifest{}, domain.ErrUnavailable
	}
	return r.snapshot.CaptureManifest(ctx, id)
}
func (r *runtimeSnapshot) InvalidateRestoredClients(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.snapshot == nil {
		return domain.ErrUnavailable
	}
	return r.snapshot.InvalidateRestoredClients(ctx, id)
}
func (r *runtimeSnapshot) VerifyRestore(ctx context.Context, m domain.BackupManifest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.snapshot == nil || m.SchemaDigest != r.metadata.SchemaDigest || m.ToolVersionsDigest != r.metadata.ToolVersionsDigest || m.Architecture != r.metadata.Architecture {
		return domain.ErrUnavailable
	}
	if err := r.snapshot.VerifyRestore(ctx, m); err != nil {
		return err
	}
	if err := r.objects.Probe(ctx); err != nil {
		return err
	}
	return r.archive.CheckRecoveryKeys(ctx)
}
func (r *runtimeSnapshot) ValidateRestoredSnapshot(ctx context.Context, m domain.BackupManifest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.snapshot == nil {
		return domain.ErrUnavailable
	}
	return r.snapshot.ValidateRestoredSnapshot(ctx, m)
}
func (r *runtimeSnapshot) ValidateBackup(_ context.Context, m domain.BackupManifest) error {
	if m.SchemaDigest != r.metadata.SchemaDigest || m.ToolVersionsDigest != r.metadata.ToolVersionsDigest || m.Architecture != r.metadata.Architecture || m.RetrievalKeyDigest == "" || m.RetrievalKeyDigest == r.metadata.RetrievalKeyDigest {
		return domain.ErrConflict
	}
	return nil
}

func (r *runtimeSnapshot) HostReady() bool { return r.schemaReady.Load() }

func (r *runtimeSnapshot) Probe(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.store == nil {
		return domain.ErrUnavailable
	}
	return r.store.Probe(ctx)
}
func (r *runtimeSnapshot) bootstrapPrerequisites(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.store == nil {
		return domain.ErrUnavailable
	}
	err := r.store.Within(ctx, func(ctx context.Context, tx domain.Tx) error {
		owner, e := repository.NewIdentityRepository(r.store).Owner(ctx, tx)
		if e != nil {
			return e
		}
		if owner.ID == "" {
			return domain.ErrIncomplete
		}
		return repository.NewBackupRepository(r.store).BootstrapPolicyReady(ctx, tx)
	})
	if err != nil {
		return err
	}
	_, err = r.journal.CurrentHead(ctx)
	return err
}

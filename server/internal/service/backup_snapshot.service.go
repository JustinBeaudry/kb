package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"slices"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

type BackupRepository interface {
	InventoryPage(context.Context, Tx, string, int) ([]domain.BackupObject, error)
	Barrier(context.Context, Tx) (domain.JournalBarrier, error)
	InvalidateRestoredClients(context.Context, Tx, string) error
}
type BackupLifecycle interface{ Recover(context.Context) error }
type BackupJournalHead interface {
	CurrentHead(context.Context) (domain.JournalBarrier, error)
}
type BackupEvidence interface {
	Open(context.Context, string) (io.ReadCloser, error)
	Put(context.Context, string, io.Reader, string) error
	Delete(context.Context, string) error
}
type BackupRestoreSource interface {
	OpenRestoreObject(context.Context, domain.BackupObject) (io.ReadCloser, error)
}
type BackupSnapshotService struct {
	tx         Transactor
	repo       BackupRepository
	lifecycle  BackupLifecycle
	journal    BackupJournalHead
	evidence   BackupEvidence
	restore    BackupRestoreSource
	initialize func(context.Context) error
	metadata   domain.BackupManifest
	now        func() time.Time
}

func NewBackupSnapshotService(tx Transactor, r BackupRepository, l BackupLifecycle, j BackupJournalHead, e BackupEvidence, restore BackupRestoreSource, initialize func(context.Context) error, metadata domain.BackupManifest, now func() time.Time) (*BackupSnapshotService, error) {
	if tx == nil || r == nil || l == nil || j == nil || e == nil || restore == nil || initialize == nil || metadata.SchemaDigest == "" || metadata.ToolVersionsDigest == "" || metadata.Architecture == "" || metadata.EvidenceNamespace == "" {
		return nil, domain.ErrInvalidInput
	}
	if now == nil {
		now = time.Now
	}
	return &BackupSnapshotService{tx, r, l, j, e, restore, initialize, metadata, now}, nil
}

// Initialization is deliberately separate from replay: restore revokes snapshot
// credentials before touching the live journal, while admission stays closed.
func (s *BackupSnapshotService) InitializeAndRecover(ctx context.Context) error {
	return s.initialize(ctx)
}
func (s *BackupSnapshotService) ReconcileJournal(ctx context.Context) (domain.JournalBarrier, error) {
	if err := s.lifecycle.Recover(ctx); err != nil {
		return domain.JournalBarrier{}, err
	}
	head, err := s.journal.CurrentHead(ctx)
	if err != nil {
		return domain.JournalBarrier{}, err
	}
	var b domain.JournalBarrier
	err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error { var e error; b, e = s.repo.Barrier(ctx, tx); return e })
	if err != nil {
		return b, err
	}
	if b != head {
		return b, domain.ErrStale
	}
	return b, nil
}
func (s *BackupSnapshotService) inventory(ctx context.Context) ([]domain.BackupObject, error) {
	result := []domain.BackupObject{}
	after := ""
	for {
		var page []domain.BackupObject
		err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
			var e error
			page, e = s.repo.InventoryPage(ctx, tx, after, 100)
			return e
		})
		if err != nil {
			return nil, err
		}
		if len(page) > 100 || len(result)+len(page) > domain.MaxBackupObjects {
			return nil, domain.ErrBudget
		}
		for _, o := range page {
			if o.ArtifactID <= after {
				return nil, domain.ErrUnavailable
			}
			after = o.ArtifactID
			result = append(result, o)
		}
		if len(page) < 100 {
			return result, nil
		}
	}
}
func (s *BackupSnapshotService) CaptureManifest(ctx context.Context, id string) (domain.BackupManifest, error) {
	if !safeBackupID(id) {
		return domain.BackupManifest{}, domain.ErrInvalidInput
	}
	objects, err := s.inventory(ctx)
	if err != nil {
		return domain.BackupManifest{}, err
	}
	for _, o := range objects {
		if !o.Deleted && o.State == "ready" && !o.Available {
			return domain.BackupManifest{}, domain.ErrIncomplete
		}
		if o.Available && !o.Deleted && (o.ObjectKey == "" || o.Generation == "" || o.Digest == "" || o.Bytes < 0 || o.Bytes > domain.MaxArtifactBytes) {
			return domain.BackupManifest{}, domain.ErrIncomplete
		}
	}
	m := s.metadata
	m.Version = 1
	m.BackupID = id
	m.CreatedAt = s.now().UTC()
	m.Objects = objects
	return m, nil
}
func (s *BackupSnapshotService) InvalidateRestoredClients(ctx context.Context, id string) error {
	if !safeBackupID(id) {
		return domain.ErrInvalidInput
	}
	return s.tx.Within(ctx, func(ctx context.Context, tx Tx) error { return s.repo.InvalidateRestoredClients(ctx, tx, id) })
}
func (s *BackupSnapshotService) VerifyRestore(ctx context.Context, m domain.BackupManifest) error {
	current, err := s.inventory(ctx)
	if err != nil {
		return err
	}
	if len(current) != len(m.Objects) {
		return domain.ErrIncomplete
	}
	for i, old := range m.Objects {
		actual := current[i]
		if actual.ArtifactID != old.ArtifactID {
			return domain.ErrIncomplete
		}
		if actual.Deleted {
			if old.ObjectKey != "" {
				if err = s.evidence.Delete(ctx, old.ObjectKey); err != nil {
					return err
				}
			}
			continue
		}
		if actual.ObjectKey != old.ObjectKey || actual.Generation != old.Generation || actual.Digest != old.Digest || actual.Bytes != old.Bytes || actual.Available != old.Available || actual.State != old.State {
			return domain.ErrStale
		}
		if !actual.Available {
			if actual.State == "ready" {
				return domain.ErrIncomplete
			}
			continue
		}
		body, e := s.restore.OpenRestoreObject(ctx, old)
		if e != nil {
			return e
		}
		data, e := readBackupEvidence(body, old)
		if e != nil {
			return e
		}
		if e = s.evidence.Put(ctx, actual.ObjectKey, bytes.NewReader(data), actual.Digest); e != nil {
			return e
		}
		body, e = s.evidence.Open(ctx, actual.ObjectKey)
		if e != nil {
			return e
		}
		if _, e = readBackupEvidence(body, actual); e != nil {
			return e
		}
	}
	// Fresh authenticated independent head after all object effects, so a
	// namespace/key/head rollback cannot be hidden by a successful graph load.
	_, err = s.ReconcileJournal(ctx)
	return err
}
func (s *BackupSnapshotService) ValidateRestoredSnapshot(ctx context.Context, m domain.BackupManifest) error {
	var b domain.JournalBarrier
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error { var e error; b, e = s.repo.Barrier(ctx, tx); return e })
	if err != nil {
		return err
	}
	if b != m.Journal {
		return domain.ErrStale
	}
	objects, err := s.inventory(ctx)
	if err != nil {
		return err
	}
	if !slices.Equal(objects, m.Objects) {
		return domain.ErrIncomplete
	}
	return nil
}

func readBackupEvidence(body io.ReadCloser, o domain.BackupObject) ([]byte, error) {
	if o.Bytes < 0 || o.Bytes > domain.MaxArtifactBytes {
		_ = body.Close()
		return nil, domain.ErrBudget
	}
	data, err := io.ReadAll(io.LimitReader(body, o.Bytes+1))
	closeErr := body.Close()
	if err != nil || closeErr != nil {
		return nil, domain.ErrUnavailable
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != o.Bytes || hex.EncodeToString(sum[:]) != o.Digest {
		return nil, domain.ErrIncomplete
	}
	return data, nil
}

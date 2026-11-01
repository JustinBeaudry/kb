package service

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

type DeletionJournal interface {
	Append(context.Context, domain.JournalRecord) (domain.JournalBarrier, error)
	ReadSince(context.Context, domain.JournalBarrier) ([]domain.JournalRecord, error)
	CurrentHead(context.Context) (domain.JournalBarrier, error)
	VerifyContinuity(context.Context, domain.JournalBarrier, domain.JournalBarrier) error
}

// LifecycleObjects.Delete confirms removal plus a durable generation-key fence
// against delayed uploads. Unknown outcomes must return an error.
type LifecycleObjects interface {
	Delete(context.Context, string) error
}
type CaptureCommitter interface {
	Commit(context.Context, domain.Lease, string, func(context.Context, Tx) error) error
}
type LifecycleRepository interface {
	CanInitializeJournal(context.Context, Tx) error
	ReindexLegacy(context.Context, Tx, int) (bool, error)
	Select(context.Context, Tx, domain.DeleteSelection) (domain.DeletionManifest, error)
	Operation(context.Context, Tx, string) (domain.DeletionOperation, error)
	Begin(context.Context, Tx, domain.DeletionOperation) error
	Accept(context.Context, Tx, string, domain.JournalBarrier) (domain.DeletionReceipt, error)
	Barrier(context.Context, Tx) (domain.JournalBarrier, error)
	SetBarrier(context.Context, Tx, domain.JournalBarrier) error
	PendingOperations(context.Context, Tx, int) ([]domain.DeletionOperation, error)
	PurgeObjects(context.Context, Tx, string) ([]domain.PurgeObject, error)
	FinishPurge(context.Context, Tx, string, []domain.PurgeObject) error
	Expiry(context.Context, Tx, time.Time, int) ([]domain.DeleteSelection, error)
	ExpiryEligible(context.Context, Tx, domain.DeleteSelection, time.Time) (bool, error)
	Policy(context.Context, Tx, string) (domain.Policy, error)
	UpdatePolicy(context.Context, Tx, string, string, domain.Policy) error
	ReconcilePolicy(context.Context, Tx, string, string, int) (bool, error)
	PolicyExclusions(context.Context, Tx, string, string, int) ([]domain.DeleteSelection, error)
}
type LifecycleService struct {
	tx      Transactor
	repo    LifecycleRepository
	owners  HostOwnerRepository
	jobs    CaptureJobs
	objects LifecycleObjects
	journal DeletionJournal
	now     func() time.Time
}

func NewLifecycleService(tx Transactor, repo LifecycleRepository, owners HostOwnerRepository, jobs CaptureJobs, objects LifecycleObjects, journal DeletionJournal, now func() time.Time) *LifecycleService {
	if now == nil {
		now = time.Now
	}
	return &LifecycleService{tx, repo, owners, jobs, objects, journal, now}
}
func validateSelection(v domain.DeleteSelection) error {
	if domain.ValidateIDs(v.ProjectID) != nil || len(v.IDs)+len(v.SessionIDs)+len(v.TaskIDs) < 1 || len(v.IDs)+len(v.SessionIDs)+len(v.TaskIDs) > domain.MaxDeletionEntries {
		return domain.ErrInvalidInput
	}
	for _, values := range [][]string{v.IDs, v.SessionIDs, v.TaskIDs} {
		for _, id := range values {
			if domain.ValidateIDs(id) != nil {
				return domain.ErrInvalidInput
			}
		}
	}
	return nil
}
func manifestDigest(m domain.DeletionManifest) string {
	b, _ := json.Marshal(m)
	return TokenDigest(string(b))
}
func canonicalManifest(m domain.DeletionManifest) domain.DeletionManifest {
	m.IDs = uniqueSorted(m.IDs)
	m.SourceDigests = uniqueSorted(m.SourceDigests)
	m.Sessions = uniqueSorted(m.Sessions)
	m.Tasks = uniqueSorted(m.Tasks)
	slices.SortFunc(m.Artifacts, func(a, b domain.DeletionArtifact) int { return strings.Compare(a.ID, b.ID) })
	m.Artifacts = slices.Compact(m.Artifacts)
	return m
}
func uniqueSorted(v []string) []string { v = slices.Clone(v); slices.Sort(v); return slices.Compact(v) }

func validLifecycleIdentity(id string) bool {
	return domain.ValidateIDs(id) == nil && utf8.ValidString(id)
}

func validateManifest(m domain.DeletionManifest) error {
	if !validLifecycleIdentity(m.ProjectID) || len(m.IDs)+len(m.Artifacts)+len(m.Sessions)+len(m.Tasks)+len(m.SourceDigests) > domain.MaxDeletionEntries {
		return domain.ErrBudget
	}
	for _, list := range [][]string{m.IDs, m.Sessions, m.Tasks} {
		for _, id := range list {
			if !validLifecycleIdentity(id) {
				return domain.ErrInvalidInput
			}
		}
	}
	for _, d := range m.SourceDigests {
		if !validDigest(d) {
			return domain.ErrInvalidInput
		}
	}
	for _, a := range m.Artifacts {
		if !validLifecycleIdentity(a.ID) || !validLifecycleIdentity(a.Generation) || !validDigest(a.Digest) {
			return domain.ErrInvalidInput
		}
	}
	return nil
}
func (s *LifecycleService) preview(ctx context.Context, tx Tx, v domain.DeleteSelection) (domain.DeletionPreview, error) {
	m, err := s.repo.Select(ctx, tx, v)
	if err != nil {
		return domain.DeletionPreview{}, err
	}
	m = canonicalManifest(m)
	if err = validateManifest(m); err != nil {
		return domain.DeletionPreview{}, err
	}
	return domain.DeletionPreview{Manifest: m, SelectionDigest: manifestDigest(m)}, nil
}
func (s *LifecycleService) PreviewHost(ctx context.Context, v domain.DeleteSelection) (domain.DeletionPreview, error) {
	if err := validateSelection(v); err != nil {
		return domain.DeletionPreview{}, err
	}
	if err := s.prepareHostMetadata(ctx); err != nil {
		return domain.DeletionPreview{}, err
	}
	var result domain.DeletionPreview
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := hostOwner(ctx, tx, s.owners); err != nil {
			return err
		}
		var err error
		result, err = s.preview(ctx, tx, v)
		return err
	})
	return result, err
}
func (s *LifecycleService) DeleteHost(ctx context.Context, v domain.DeleteSelection) (domain.DeletionReceipt, error) {
	if err := validateSelection(v); err != nil {
		return domain.DeletionReceipt{}, err
	}
	if domain.ValidateIDs(v.IdempotencyKey) != nil || v.ExpectedSelectionDigest == "" {
		return domain.DeletionReceipt{}, domain.ErrInvalidInput
	}
	if err := s.prepareHostMetadata(ctx); err != nil {
		return domain.DeletionReceipt{}, err
	}
	return s.delete(ctx, v, true)
}
func (s *LifecycleService) delete(ctx context.Context, v domain.DeleteSelection, host bool) (domain.DeletionReceipt, error) {
	return s.deleteExpectedPolicy(ctx, v, host, "")
}
func (s *LifecycleService) deleteExpectedPolicy(ctx context.Context, v domain.DeleteSelection, host bool, expectedPolicy string) (domain.DeletionReceipt, error) {
	var op domain.DeletionOperation
	request := v
	request.IDs = uniqueSorted(v.IDs)
	request.SessionIDs = uniqueSorted(v.SessionIDs)
	request.TaskIDs = uniqueSorted(v.TaskIDs)
	request.IdempotencyKey = ""
	requestBytes, _ := json.Marshal(request)
	requestDigest := TokenDigest(string(requestBytes))
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		ownerID := "retention"
		if host {
			owner, err := hostOwner(ctx, tx, s.owners)
			if err != nil {
				return err
			}
			ownerID = owner.OwnerID
		}
		id := captureID("deletion", ownerID, v.ProjectID, v.IdempotencyKey)
		existing, err := s.repo.Operation(ctx, tx, id)
		if err == nil {
			if existing.RequestDigest != requestDigest {
				return domain.ErrConflict
			}
			op = existing
			return nil
		}
		if !errors.Is(err, domain.ErrIncomplete) {
			return err
		}
		if v.ExpiryAt != nil {
			eligible, err := s.repo.ExpiryEligible(ctx, tx, v, *v.ExpiryAt)
			if err != nil {
				return err
			}
			if !eligible {
				return domain.ErrStale
			}
		}
		if expectedPolicy != "" {
			p, err := s.repo.Policy(ctx, tx, v.ProjectID)
			if err != nil {
				return err
			}
			if p.Revision != expectedPolicy {
				return domain.ErrStale
			}
		}
		preview, err := s.preview(ctx, tx, v)
		if err != nil {
			return err
		}
		if preview.SelectionDigest != v.ExpectedSelectionDigest {
			return domain.ErrStale
		}
		record := domain.JournalRecord{Version: 1, Action: "delete", OperationID: id, ManifestDigest: preview.SelectionDigest, RequestDigest: requestDigest, Manifest: preview.Manifest, CreatedAt: s.now().UTC()}
		encoded, err := json.Marshal(record)
		if err != nil || len(encoded) > domain.MaxJournalRecordBytes {
			return domain.ErrBudget
		}
		op = domain.DeletionOperation{Record: record, RequestDigest: requestDigest, Receipt: domain.DeletionReceipt{ID: id, State: "pending_journal", ManifestDigest: preview.SelectionDigest, BackupState: "expiry_pending"}}
		return s.repo.Begin(ctx, tx, op)
	})
	if err != nil {
		return domain.DeletionReceipt{}, err
	}
	if op.Receipt.State != "pending_journal" {
		return op.Receipt, nil
	}
	if s.journal == nil {
		return op.Receipt, domain.ErrUnavailable
	}
	barrier, err := s.journal.Append(ctx, op.Record)
	if err != nil {
		return op.Receipt, err
	}
	receipt := op.Receipt
	err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		current, err := s.repo.Operation(ctx, tx, op.Receipt.ID)
		if err != nil {
			return err
		}
		if current.Record.ManifestDigest != op.Record.ManifestDigest {
			return domain.ErrConflict
		}
		receipt, err = s.accept(ctx, tx, current, barrier)
		return err
	})
	return receipt, err
}
func (s *LifecycleService) accept(ctx context.Context, tx Tx, op domain.DeletionOperation, b domain.JournalBarrier) (domain.DeletionReceipt, error) {
	receipt, err := s.repo.Accept(ctx, tx, op.Receipt.ID, b)
	if err != nil {
		return receipt, err
	}
	j := domain.Job{ID: captureID("lifecycle.purge", op.Receipt.ID), Kind: "lifecycle.purge", InputDigest: captureID("lifecycle.purge", op.Receipt.ID, op.Record.ManifestDigest), PolicyRevision: "lifecycle-v1", Scope: domain.Scope{ProjectID: op.Record.Manifest.ProjectID}, InputRevisionIDs: []string{op.Receipt.ID}, DueAt: s.now().UTC()}
	if err = s.jobs.Enqueue(ctx, tx, j); err != nil {
		return domain.DeletionReceipt{}, err
	}
	return receipt, nil
}
func (s *LifecycleService) DeletionStatusHost(ctx context.Context, id string) (domain.DeletionReceipt, error) {
	if domain.ValidateIDs(id) != nil {
		return domain.DeletionReceipt{}, domain.ErrInvalidInput
	}
	var op domain.DeletionOperation
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := hostOwner(ctx, tx, s.owners); err != nil {
			return err
		}
		var err error
		op, err = s.repo.Operation(ctx, tx, id)
		return err
	})
	return op.Receipt, err
}
func (s *LifecycleService) InitializeJournalHost(ctx context.Context) (domain.JournalBarrier, error) {
	if err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		_, err := hostOwner(ctx, tx, s.owners)
		if err != nil {
			return err
		}
		return s.repo.CanInitializeJournal(ctx, tx)
	}); err != nil {
		return domain.JournalBarrier{}, err
	}
	initializer, ok := s.journal.(interface {
		Initialize(context.Context) (domain.JournalBarrier, error)
	})
	if !ok {
		return domain.JournalBarrier{}, domain.ErrUnavailable
	}
	return initializer.Initialize(ctx)
}
func (s *LifecycleService) CheckJournalHost(ctx context.Context) (domain.JournalBarrier, error) {
	if err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error { _, err := hostOwner(ctx, tx, s.owners); return err }); err != nil {
		return domain.JournalBarrier{}, err
	}
	if s.journal == nil {
		return domain.JournalBarrier{}, domain.ErrUnavailable
	}
	return s.journal.CurrentHead(ctx)
}

// Recover authenticates live independent latest head, continuity and full replay
// before readiness. An absent external head is never initialized implicitly.
func (s *LifecycleService) prepareHostMetadata(ctx context.Context) error {
	if err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error { _, err := hostOwner(ctx, tx, s.owners); return err }); err != nil {
		return err
	}
	return s.reindex(ctx)
}

// Reindexing writes identity projections only; immutable bytes and ages remain unchanged.
func (s *LifecycleService) reindex(ctx context.Context) error {
	for {
		more := false
		if err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
			var err error
			more, err = s.repo.ReindexLegacy(ctx, tx, 100)
			return err
		}); err != nil {
			return err
		}
		if !more {
			return nil
		}
	}
}
func (s *LifecycleService) Recover(ctx context.Context) error {
	if s.journal == nil {
		return domain.ErrUnavailable
	}
	if err := s.reindex(ctx); err != nil {
		return err
	}
	// Retry immutable graph intents first. This repairs a crash before append,
	// including scheduled expiry without an operator holding its preview payload.
	for {
		var pending []domain.DeletionOperation
		err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
			var err error
			pending, err = s.repo.PendingOperations(ctx, tx, 100)
			return err
		})
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			break
		}
		for _, op := range pending {
			if op.Record.Version != 1 || op.Record.OperationID != op.Receipt.ID || validateManifest(op.Record.Manifest) != nil || manifestDigest(canonicalManifest(op.Record.Manifest)) != op.Record.ManifestDigest {
				return domain.ErrUnavailable
			}
			b, err := s.journal.Append(ctx, op.Record)
			if err != nil {
				return err
			}
			if err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
				current, err := s.repo.Operation(ctx, tx, op.Receipt.ID)
				if err != nil {
					return err
				}
				if current.Record.ManifestDigest != op.Record.ManifestDigest {
					return domain.ErrConflict
				}
				_, err = s.accept(ctx, tx, current, b)
				return err
			}); err != nil {
				return err
			}
		}
	}
	head, err := s.journal.CurrentHead(ctx)
	if err != nil {
		return err
	}
	var previous domain.JournalBarrier
	if err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		previous, err = s.repo.Barrier(ctx, tx)
		return err
	}); err != nil {
		return err
	}
	if previous.Namespace == "" {
		previous.Namespace = head.Namespace
	}
	if err = s.journal.VerifyContinuity(ctx, previous, head); err != nil {
		return err
	}
	if pager, ok := s.journal.(interface {
		ReadPage(context.Context, domain.JournalBarrier, domain.JournalBarrier, int) ([]domain.JournalRecord, domain.JournalBarrier, error)
	}); ok {
		for previous != head {
			records, next, err := pager.ReadPage(ctx, previous, head, 100)
			if err != nil {
				return err
			}
			if len(records) > 100 || next == previous || next.Namespace != head.Namespace || next.Sequence > head.Sequence {
				return domain.ErrUnavailable
			}
			if err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
				if err := s.replayRecords(ctx, tx, records, next); err != nil {
					return err
				}
				return s.repo.SetBarrier(ctx, tx, next)
			}); err != nil {
				return err
			}
			previous = next
		}
	} else {
		records, err := s.journal.ReadSince(ctx, previous)
		if err != nil {
			return err
		}
		if len(records) > domain.MaxDeletionEntries {
			return domain.ErrBudget
		}
		if err = s.journal.VerifyContinuity(ctx, previous, head); err != nil {
			return err
		}
		if err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error { return s.replayRecords(ctx, tx, records, head) }); err != nil {
			return err
		}
	}
	return s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		pending, err := s.repo.PendingOperations(ctx, tx, 1)
		if err != nil {
			return err
		}
		if len(pending) != 0 {
			return domain.ErrUnavailable
		}
		return s.repo.SetBarrier(ctx, tx, head)
	})
}
func (s *LifecycleService) replayRecords(ctx context.Context, tx Tx, records []domain.JournalRecord, barrier domain.JournalBarrier) error {
	for _, record := range records {
		if record.Version != 1 || record.Action != "delete" || domain.ValidateIDs(record.OperationID) != nil || validateManifest(record.Manifest) != nil || manifestDigest(canonicalManifest(record.Manifest)) != record.ManifestDigest {
			return domain.ErrUnavailable
		}
		op, err := s.repo.Operation(ctx, tx, record.OperationID)
		if errors.Is(err, domain.ErrIncomplete) {
			op = domain.DeletionOperation{Record: record, RequestDigest: record.RequestDigest, Receipt: domain.DeletionReceipt{ID: record.OperationID, State: "pending_journal", ManifestDigest: record.ManifestDigest, BackupState: "expiry_pending"}}
			if err = s.repo.Begin(ctx, tx, op); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if op.Record.ManifestDigest != record.ManifestDigest {
			return domain.ErrConflict
		}
		if _, err = s.accept(ctx, tx, op, barrier); err != nil {
			return err
		}
	}
	return nil
}

func (s *LifecycleService) Purge(ctx context.Context, job domain.Job, lease domain.Lease, jobs CaptureCommitter) error {
	if job.Kind != "lifecycle.purge" || len(job.InputRevisionIDs) != 1 {
		return domain.ErrInvalidInput
	}
	id := job.InputRevisionIDs[0]
	var objects []domain.PurgeObject
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		op, err := s.repo.Operation(ctx, tx, id)
		if err != nil {
			return err
		}
		if captureID("lifecycle.purge", op.Receipt.ID, op.Record.ManifestDigest) != job.InputDigest || op.Receipt.State == "pending_journal" {
			return domain.ErrStale
		}
		objects, err = s.repo.PurgeObjects(ctx, tx, id)
		return err
	})
	if err != nil {
		return err
	}
	for _, object := range objects {
		if object.UploadLeaseUntil.After(s.now().UTC()) {
			return domain.ErrIncomplete
		}
		if err = s.objects.Delete(ctx, object.Key); err != nil {
			return err
		}
	}
	return jobs.Commit(ctx, lease, job.InputDigest, func(ctx context.Context, tx Tx) error { return s.repo.FinishPurge(ctx, tx, id, objects) })
}
func (s *LifecycleService) Expire(ctx context.Context, now time.Time) (domain.ExpiryReport, error) {
	if now.IsZero() {
		return domain.ExpiryReport{}, domain.ErrInvalidInput
	}
	var selections []domain.DeleteSelection
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		selections, err = s.repo.Expiry(ctx, tx, now.UTC(), 100)
		return err
	})
	if err != nil {
		return domain.ExpiryReport{}, err
	}
	report := domain.ExpiryReport{}
	for _, v := range selections {
		at := now.UTC()
		v.ExpiryAt = &at
		var preview domain.DeletionPreview
		err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
			var err error
			preview, err = s.preview(ctx, tx, v)
			return err
		})
		if err != nil {
			return report, err
		}
		v.ExpectedSelectionDigest = preview.SelectionDigest
		v.IdempotencyKey = captureID("expiry", preview.SelectionDigest)
		receipt, err := s.delete(ctx, v, false)
		if errors.Is(err, domain.ErrStale) {
			continue
		}
		report.Operations = append(report.Operations, receipt)
		if err != nil {
			return report, err
		}
	}
	return report, nil
}
func (s *LifecycleService) PolicyHost(ctx context.Context, project string) (domain.Policy, error) {
	if domain.ValidateIDs(project) != nil {
		return domain.Policy{}, domain.ErrInvalidInput
	}
	var p domain.Policy
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := hostOwner(ctx, tx, s.owners); err != nil {
			return err
		}
		var err error
		p, err = s.repo.Policy(ctx, tx, project)
		return err
	})
	return p, err
}
func validPolicy(p domain.Policy) bool {
	if domain.ValidateIDs(p.Revision) != nil || p.SpoolLimitBytes < 0 || p.SpoolLimitBytes > 1<<40 || len(p.ExcludedPaths) > 100 || len(p.ExcludedFields) > 100 || len(p.AllowedModelRoutes) > 100 {
		return false
	}
	for _, retention := range []time.Duration{p.RawHistoryRetention, p.InactiveTaskRetention, p.BackupRetention} {
		if retention < 0 || retention > 3650*24*time.Hour {
			return false
		}
	}
	for _, source := range p.ExcludedPaths {
		if source == "" || validateSource(domain.Policy{}, strings.TrimSuffix(source, "/")) != nil {
			return false
		}
	}
	for _, field := range append(slices.Clone(p.ExcludedFields), p.AllowedModelRoutes...) {
		if domain.ValidateIDs(field) != nil {
			return false
		}
	}
	return true
}
func (s *LifecycleService) UpdatePolicyHost(ctx context.Context, project, expected string, p domain.Policy) (domain.Policy, error) {
	if domain.ValidateIDs(project) != nil || (expected != "" && domain.ValidateIDs(expected) != nil) || !validPolicy(p) || expected == p.Revision {
		return domain.Policy{}, domain.ErrInvalidInput
	}
	if p.RawHistoryRetention == 0 {
		p.RawHistoryRetention = 90 * 24 * time.Hour
	}
	if p.InactiveTaskRetention == 0 {
		p.InactiveTaskRetention = 30 * 24 * time.Hour
	}
	if p.BackupRetention == 0 {
		p.BackupRetention = 30 * 24 * time.Hour
	}
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := hostOwner(ctx, tx, s.owners); err != nil {
			return err
		}
		if err := s.repo.UpdatePolicy(ctx, tx, project, expected, p); err != nil {
			return err
		}
		return s.jobs.Enqueue(ctx, tx, domain.Job{ID: captureID("lifecycle.reconcile", project, p.Revision), Kind: "lifecycle.reconcile", InputDigest: captureID(project, expected, p.Revision), PolicyRevision: p.Revision, Scope: domain.Scope{ProjectID: project}, InputRevisionIDs: []string{expected, p.Revision}, DueAt: s.now().UTC()})
	})
	if err != nil {
		return domain.Policy{}, err
	}
	return p, nil
}
func (s *LifecycleService) ReconcilePolicy(ctx context.Context, job domain.Job, lease domain.Lease, jobs CaptureCommitter) error {
	if job.Kind != "lifecycle.reconcile" || (len(job.InputRevisionIDs) != 2 && len(job.InputRevisionIDs) != 3) || job.InputRevisionIDs[1] != job.PolicyRevision {
		return domain.ErrInvalidInput
	}
	expected := captureID(job.Scope.ProjectID, job.InputRevisionIDs[0], job.PolicyRevision)
	if len(job.InputRevisionIDs) == 3 {
		expected = captureID(job.Scope.ProjectID, job.InputRevisionIDs[0], job.PolicyRevision, job.InputRevisionIDs[2])
	}
	if job.InputDigest != expected {
		return domain.ErrInvalidInput
	}
	if err := s.Recover(ctx); err != nil {
		return err
	}
	var excluded []domain.DeleteSelection
	if err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		excluded, err = s.repo.PolicyExclusions(ctx, tx, job.Scope.ProjectID, job.PolicyRevision, 100)
		return err
	}); err != nil {
		return err
	}
	for _, v := range excluded {
		var pre domain.DeletionPreview
		if err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error { var err error; pre, err = s.preview(ctx, tx, v); return err }); err != nil {
			return err
		}
		v.ExpectedSelectionDigest = pre.SelectionDigest
		v.IdempotencyKey = captureID("policy-exclusion", job.PolicyRevision, pre.SelectionDigest)
		if _, err := s.deleteExpectedPolicy(ctx, v, false, job.PolicyRevision); err != nil {
			return err
		}
	}
	return jobs.Commit(ctx, lease, job.InputDigest, func(ctx context.Context, tx Tx) error {
		more, err := s.repo.ReconcilePolicy(ctx, tx, job.Scope.ProjectID, job.PolicyRevision, 100)
		if err != nil {
			return err
		}
		if more || len(excluded) == 100 {
			next := job
			next.ID = captureID("lifecycle.reconcile", job.ID, "continuation")
			next.InputRevisionIDs = []string{job.InputRevisionIDs[0], job.PolicyRevision, next.ID}
			next.InputDigest = captureID(job.Scope.ProjectID, job.InputRevisionIDs[0], job.PolicyRevision, next.ID)
			next.DueAt = s.now().UTC()
			return s.jobs.Enqueue(ctx, tx, next)
		}
		return nil
	})
}

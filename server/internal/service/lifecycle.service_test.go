package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeletionNotAcknowledgedBeforeJournalBarrier(t *testing.T) {
	ctx := context.Background()
	repo := newLifecycleFake()
	journal := &journalFake{fail: true}
	s := NewLifecycleService(&identityFixture{}, repo, repo, repo, repo, journal, time.Now)
	selection := domain.DeleteSelection{ProjectID: "p", IDs: []string{"a"}, IdempotencyKey: "key"}
	preview, err := s.PreviewHost(ctx, selection)
	require.NoError(t, err)
	selection.ExpectedSelectionDigest = preview.SelectionDigest
	receipt, err := s.DeleteHost(ctx, selection)
	require.ErrorIs(t, err, domain.ErrUnavailable)
	assert.Equal(t, "pending_journal", receipt.State)
	assert.True(t, repo.excluded)
	assert.Empty(t, repo.enqueued)
	journal.fail = false
	receipt, err = s.DeleteHost(ctx, selection)
	require.NoError(t, err)
	assert.Equal(t, "purge_pending", receipt.State)
	assert.Equal(t, uint64(1), receipt.Barrier.Sequence)
	assert.Len(t, repo.enqueued, 1)
	again, err := s.DeleteHost(ctx, selection)
	require.NoError(t, err)
	assert.Equal(t, receipt, again)
	assert.Len(t, journal.records, 1)
}
func TestDeletionPreviewRequiresExactExpandedSelection(t *testing.T) {
	repo := newLifecycleFake()
	s := NewLifecycleService(&identityFixture{}, repo, repo, repo, repo, &journalFake{}, time.Now)
	_, err := s.DeleteHost(context.Background(), domain.DeleteSelection{ProjectID: "p", IDs: []string{"a"}, IdempotencyKey: "key", ExpectedSelectionDigest: "wrong"})
	require.ErrorIs(t, err, domain.ErrStale)
	assert.False(t, repo.excluded)
}
func TestPendingGraphIntentBlocksReadiness(t *testing.T) {
	repo := newLifecycleFake()
	repo.pending = true
	s := NewLifecycleService(&identityFixture{}, repo, repo, repo, repo, &journalFake{}, time.Now)
	require.ErrorIs(t, s.Recover(context.Background()), domain.ErrUnavailable)
}

type lifecycleFake struct {
	op                domain.DeletionOperation
	excluded, pending bool
	enqueued          []domain.Job
}

func newLifecycleFake() *lifecycleFake { return &lifecycleFake{} }
func (f *lifecycleFake) Owner(context.Context, Tx) (OwnerIdentity, error) {
	return OwnerIdentity{ID: "owner", PublicKey: "public"}, nil
}
func (f *lifecycleFake) Select(context.Context, Tx, domain.DeleteSelection) (domain.DeletionManifest, error) {
	return domain.DeletionManifest{ProjectID: "p", IDs: []string{"a", "copy"}, SourceDigests: []string{TokenDigest("body")}, Artifacts: []domain.DeletionArtifact{{ID: "a", Generation: "g", Digest: TokenDigest("body")}}}, nil
}
func (f *lifecycleFake) Operation(_ context.Context, _ Tx, id string) (domain.DeletionOperation, error) {
	if f.op.Receipt.ID != id {
		return domain.DeletionOperation{}, domain.ErrIncomplete
	}
	return f.op, nil
}
func (f *lifecycleFake) Begin(_ context.Context, _ Tx, op domain.DeletionOperation) error {
	f.op = op
	f.excluded = true
	return nil
}
func (f *lifecycleFake) Accept(_ context.Context, _ Tx, id string, b domain.JournalBarrier) (domain.DeletionReceipt, error) {
	f.op.Receipt.Barrier = b
	f.op.Receipt.State = "purge_pending"
	f.pending = false
	return f.op.Receipt, nil
}
func (f *lifecycleFake) Barrier(context.Context, Tx) (domain.JournalBarrier, error) {
	return domain.JournalBarrier{Namespace: "test"}, nil
}
func (f *lifecycleFake) SetBarrier(context.Context, Tx, domain.JournalBarrier) error { return nil }
func (f *lifecycleFake) PendingOperations(context.Context, Tx, int) ([]domain.DeletionOperation, error) {
	if f.pending {
		if f.op.Receipt.ID != "" {
			return []domain.DeletionOperation{f.op}, nil
		}
		return []domain.DeletionOperation{{Receipt: domain.DeletionReceipt{ID: "pending"}}}, nil
	}
	return nil, nil
}
func (f *lifecycleFake) PurgeObjects(context.Context, Tx, string) ([]domain.PurgeObject, error) {
	return nil, nil
}
func (f *lifecycleFake) FinishPurge(context.Context, Tx, string, []domain.PurgeObject) error {
	return nil
}
func (f *lifecycleFake) Expiry(context.Context, Tx, time.Time, int) ([]domain.DeleteSelection, error) {
	return nil, nil
}
func (f *lifecycleFake) Policy(context.Context, Tx, string) (domain.Policy, error) {
	return domain.Policy{}, nil
}
func (f *lifecycleFake) UpdatePolicy(context.Context, Tx, string, string, domain.Policy) error {
	return nil
}
func (f *lifecycleFake) ReconcilePolicy(context.Context, Tx, string, string, int) (bool, error) {
	return false, nil
}
func (f *lifecycleFake) Enqueue(_ context.Context, _ Tx, j domain.Job) error {
	f.enqueued = append(f.enqueued, j)
	return nil
}
func (f *lifecycleFake) Delete(context.Context, string) error { return nil }

type journalFake struct {
	records []domain.JournalRecord
	fail    bool
}

func (f *journalFake) Append(_ context.Context, r domain.JournalRecord) (domain.JournalBarrier, error) {
	if f.fail {
		return domain.JournalBarrier{}, domain.ErrUnavailable
	}
	for _, x := range f.records {
		if x.OperationID == r.OperationID {
			return domain.JournalBarrier{Namespace: "test", Sequence: 1, Digest: "head"}, nil
		}
	}
	f.records = append(f.records, r)
	return domain.JournalBarrier{Namespace: "test", Sequence: 1, Digest: "head"}, nil
}
func (f *journalFake) CurrentHead(context.Context) (domain.JournalBarrier, error) {
	return domain.JournalBarrier{Namespace: "test"}, nil
}
func (f *journalFake) ReadSince(context.Context, domain.JournalBarrier) ([]domain.JournalRecord, error) {
	return f.records, nil
}
func (f *journalFake) VerifyContinuity(context.Context, domain.JournalBarrier, domain.JournalBarrier) error {
	return nil
}
func (f *journalFake) Initialize(context.Context) (domain.JournalBarrier, error) {
	return domain.JournalBarrier{Namespace: "test"}, nil
}

func (f *lifecycleFake) PolicyExclusions(context.Context, Tx, string, string, int) ([]domain.DeleteSelection, error) {
	return nil, nil
}

func (f *lifecycleFake) ReindexLegacy(context.Context, Tx, int) (bool, error) { return false, nil }

func TestLifecycleHostInputsAreBoundedBeforeEffects(t *testing.T) {
	repo := newLifecycleFake()
	s := NewLifecycleService(&identityFixture{}, repo, repo, repo, repo, &journalFake{}, nil)
	ctx := context.Background()
	for _, selection := range []domain.DeleteSelection{{}, {ProjectID: "p"}, {ProjectID: "p", IDs: []string{""}}, {ProjectID: "p", SessionIDs: []string{""}}, {ProjectID: "p", TaskIDs: []string{""}}, {ProjectID: "p", IDs: make([]string, 1001)}} {
		_, err := s.PreviewHost(ctx, selection)
		require.ErrorIs(t, err, domain.ErrInvalidInput)
	}
	_, err := s.DeleteHost(ctx, domain.DeleteSelection{ProjectID: "p", IDs: []string{"a"}})
	require.ErrorIs(t, err, domain.ErrInvalidInput)
	_, err = s.DeletionStatusHost(ctx, "")
	require.ErrorIs(t, err, domain.ErrInvalidInput)
	_, err = s.PolicyHost(ctx, "")
	require.ErrorIs(t, err, domain.ErrInvalidInput)
	_, err = s.Expire(ctx, time.Time{})
	require.ErrorIs(t, err, domain.ErrInvalidInput)
	require.ErrorIs(t, s.Purge(ctx, domain.Job{}, domain.Lease{}, fakeLifecycleCommitter{}), domain.ErrInvalidInput)
	require.ErrorIs(t, s.ReconcilePolicy(ctx, domain.Job{}, domain.Lease{}, fakeLifecycleCommitter{}), domain.ErrInvalidInput)
	assert.False(t, repo.excluded)
}
func TestLifecycleManifestRejectsUnsafeJournalIdentityBeforeExclusion(t *testing.T) {
	for _, m := range []domain.DeletionManifest{{ProjectID: ""}, {ProjectID: "p", IDs: []string{""}}, {ProjectID: "p", SourceDigests: []string{"bad"}}, {ProjectID: "p", Artifacts: []domain.DeletionArtifact{{ID: "a", Generation: "g", Digest: "bad"}}}, {ProjectID: "p", IDs: make([]string, 1001)}} {
		require.Error(t, validateManifest(m))
	}
	m := canonicalManifest(domain.DeletionManifest{ProjectID: "p", IDs: []string{"b", "a", "b"}, Artifacts: []domain.DeletionArtifact{{ID: "b"}, {ID: "a"}, {ID: "a"}}})
	assert.Equal(t, []string{"a", "b"}, m.IDs)
	assert.Len(t, m.Artifacts, 2)
}
func TestLifecyclePolicyValidation(t *testing.T) {
	p := domain.Policy{Revision: "revision", SpoolLimitBytes: 1024}
	require.True(t, validPolicy(p))
	for _, mutate := range []func(*domain.Policy){func(p *domain.Policy) { p.Revision = "" }, func(p *domain.Policy) { p.SpoolLimitBytes = -1 }, func(p *domain.Policy) { p.RawHistoryRetention = -1 }, func(p *domain.Policy) { p.ExcludedPaths = []string{"../outside"} }, func(p *domain.Policy) { p.ExcludedPaths = []string{""} }, func(p *domain.Policy) { p.ExcludedFields = []string{""} }, func(p *domain.Policy) { p.AllowedModelRoutes = make([]string, 101) }} {
		bad := p
		mutate(&bad)
		assert.False(t, validPolicy(bad))
	}
	repo := newLifecycleFake()
	s := NewLifecycleService(&identityFixture{}, repo, repo, repo, repo, &journalFake{}, time.Now)
	ctx := context.Background()
	_, err := s.UpdatePolicyHost(ctx, "p", "revision", p)
	require.ErrorIs(t, err, domain.ErrInvalidInput)
	p.Revision = "new"
	got, err := s.UpdatePolicyHost(ctx, "p", "old", p)
	require.NoError(t, err)
	assert.Equal(t, p.Revision, got.Revision)
	assert.Equal(t, 90*24*time.Hour, got.RawHistoryRetention)
	require.Len(t, repo.enqueued, 1)
	assert.Equal(t, "lifecycle.reconcile", repo.enqueued[0].Kind)
	_, err = s.PolicyHost(ctx, "p")
	require.NoError(t, err)
	job := repo.enqueued[0]
	require.NoError(t, s.ReconcilePolicy(ctx, job, domain.Lease{}, fakeLifecycleCommitter{}))
}
func TestLifecycleJournalHostMaintenanceAndPurge(t *testing.T) {
	ctx := context.Background()
	repo := newLifecycleFake()
	j := &journalFake{}
	s := NewLifecycleService(&identityFixture{}, repo, repo, repo, repo, j, time.Now)
	b, err := s.InitializeJournalHost(ctx)
	require.NoError(t, err)
	assert.Equal(t, "test", b.Namespace)
	b, err = s.CheckJournalHost(ctx)
	require.NoError(t, err)
	assert.Equal(t, "test", b.Namespace)
	v := domain.DeleteSelection{ProjectID: "p", IDs: []string{"a"}, IdempotencyKey: "key"}
	pre, err := s.PreviewHost(ctx, v)
	require.NoError(t, err)
	v.ExpectedSelectionDigest = pre.SelectionDigest
	receipt, err := s.DeleteHost(ctx, v)
	require.NoError(t, err)
	got, err := s.DeletionStatusHost(ctx, receipt.ID)
	require.NoError(t, err)
	assert.Equal(t, receipt, got)
	v.ExpectedSelectionDigest = "changed"
	_, err = s.DeleteHost(ctx, v)
	require.ErrorIs(t, err, domain.ErrConflict)
	require.NoError(t, s.Purge(ctx, repo.enqueued[0], domain.Lease{}, fakeLifecycleCommitter{}))
	require.NoError(t, s.Recover(ctx))
	s.journal = nil
	_, err = s.CheckJournalHost(ctx)
	require.ErrorIs(t, err, domain.ErrUnavailable)
	require.ErrorIs(t, s.Recover(ctx), domain.ErrUnavailable)
	_, err = s.InitializeJournalHost(ctx)
	require.ErrorIs(t, err, domain.ErrUnavailable)
}
func TestCrashAfterGraphIntentIsRecoveredWithoutNewSelection(t *testing.T) {
	ctx := context.Background()
	repo := newLifecycleFake()
	j := &journalFake{fail: true}
	s := NewLifecycleService(&identityFixture{}, repo, repo, repo, repo, j, time.Now)
	v := domain.DeleteSelection{ProjectID: "p", IDs: []string{"a"}, IdempotencyKey: "key"}
	pre, err := s.PreviewHost(ctx, v)
	require.NoError(t, err)
	v.ExpectedSelectionDigest = pre.SelectionDigest
	_, err = s.DeleteHost(ctx, v)
	require.Error(t, err)
	j.fail = false
	repo.pending = true
	require.NoError(t, s.Recover(ctx))
	assert.Equal(t, "purge_pending", repo.op.Receipt.State)
	assert.Len(t, j.records, 1)
}

type fakeLifecycleCommitter struct{}

func (fakeLifecycleCommitter) Commit(ctx context.Context, _ domain.Lease, _ string, fn func(context.Context, Tx) error) error {
	return fn(ctx, nil)
}

func (f *lifecycleFake) CanInitializeJournal(context.Context, Tx) error { return nil }

func TestRecoverPagesMoreThanOneThousandJournalIntents(t *testing.T) {
	repo := newLifecycleFake()
	journal := &pagedLifecycleJournal{}
	for i := range 1002 {
		m := domain.DeletionManifest{ProjectID: "project/🧪", Sessions: []string{"session/🧪"}}
		journal.records = append(journal.records, domain.JournalRecord{Version: 1, OperationID: captureID(i), Action: "delete", Manifest: m, ManifestDigest: manifestDigest(m), CreatedAt: time.Now().UTC()})
	}
	s := NewLifecycleService(&identityFixture{}, repo, repo, repo, repo, journal, time.Now)
	require.NoError(t, s.Recover(context.Background()))
	assert.Equal(t, 11, journal.pages)
	assert.Len(t, repo.enqueued, 1002)
}
func TestRecoverAuthenticatesInitializedEmptyGenesis(t *testing.T) {
	repo := newLifecycleFake()
	journal := &pagedLifecycleJournal{}
	s := NewLifecycleService(&identityFixture{}, repo, repo, repo, repo, journal, time.Now)
	require.NoError(t, s.Recover(context.Background()))
	assert.Equal(t, 1, journal.pages)
}

type pagedLifecycleJournal struct {
	journalFake
	pages int
}

func (j *pagedLifecycleJournal) CurrentHead(context.Context) (domain.JournalBarrier, error) {
	return domain.JournalBarrier{Namespace: "test", Sequence: uint64(len(j.records)), Digest: "authenticated-head"}, nil
}
func (j *pagedLifecycleJournal) ReadPage(_ context.Context, after, target domain.JournalBarrier, limit int) ([]domain.JournalRecord, domain.JournalBarrier, error) {
	j.pages++
	end := after.Sequence + uint64(limit)
	if end >= target.Sequence {
		return j.records[after.Sequence:], target, nil
	}
	return j.records[after.Sequence:end], domain.JournalBarrier{Namespace: "test", Sequence: end, Digest: captureID(end)}, nil
}

func TestOversizedJournalIntentFailsBeforeSourceExclusion(t *testing.T) {
	f := newLifecycleFake()
	repo := selectedLifecycleRepository{LifecycleRepository: f, manifest: domain.DeletionManifest{ProjectID: "p", IDs: []string{strings.Repeat("x", domain.MaxJournalRecordBytes)}}}
	j := &journalFake{}
	s := NewLifecycleService(&identityFixture{}, repo, f, f, f, j, time.Now)
	v := domain.DeleteSelection{ProjectID: "p", IDs: []string{"a"}, IdempotencyKey: "key"}
	pre, err := s.PreviewHost(context.Background(), v)
	require.NoError(t, err)
	v.ExpectedSelectionDigest = pre.SelectionDigest
	_, err = s.DeleteHost(context.Background(), v)
	require.ErrorIs(t, err, domain.ErrBudget)
	assert.False(t, f.excluded)
	assert.Empty(t, j.records)
}
func TestJournalInitializationRejectsGraphRecoveryState(t *testing.T) {
	f := newLifecycleFake()
	repo := selectedLifecycleRepository{LifecycleRepository: f, initErr: domain.ErrConflict}
	s := NewLifecycleService(&identityFixture{}, repo, f, f, f, &journalFake{}, time.Now)
	_, err := s.InitializeJournalHost(context.Background())
	require.ErrorIs(t, err, domain.ErrConflict)
}
func TestPurgeUnknownOutcomeAndActiveUploadCannotSettle(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	f := newLifecycleFake()
	repo := selectedLifecycleRepository{LifecycleRepository: f, objects: []domain.PurgeObject{{ID: "a", Key: "key", Generation: "g", Digest: TokenDigest("body"), UploadLeaseUntil: now.Add(time.Minute)}}}
	objects := &lifecycleDeleteSpy{}
	s := NewLifecycleService(&identityFixture{}, repo, f, f, objects, &journalFake{}, func() time.Time { return now })
	v := domain.DeleteSelection{ProjectID: "p", IDs: []string{"a"}, IdempotencyKey: "key"}
	pre, err := s.PreviewHost(ctx, v)
	require.NoError(t, err)
	v.ExpectedSelectionDigest = pre.SelectionDigest
	_, err = s.DeleteHost(ctx, v)
	require.NoError(t, err)
	job := f.enqueued[0]
	committer := &lifecycleCommitSpy{}
	require.ErrorIs(t, s.Purge(ctx, job, domain.Lease{}, committer), domain.ErrIncomplete)
	assert.Empty(t, objects.keys)
	assert.False(t, committer.called)
	repo.objects[0].UploadLeaseUntil = now.Add(-time.Second)
	s.repo = repo
	objects.err = domain.ErrUnavailable
	require.ErrorIs(t, s.Purge(ctx, job, domain.Lease{}, committer), domain.ErrUnavailable)
	assert.False(t, committer.called)
	assert.Equal(t, "purge_pending", f.op.Receipt.State)
	objects.err = nil
	committer.err = domain.ErrStale
	require.ErrorIs(t, s.Purge(ctx, job, domain.Lease{}, committer), domain.ErrStale)
	assert.True(t, committer.called)
}
func TestPolicyExclusionRequiresJournalBarrierBeforeWorkerSettlement(t *testing.T) {
	ctx := context.Background()
	f := newLifecycleFake()
	repo := selectedLifecycleRepository{LifecycleRepository: f, policy: domain.Policy{Revision: "new"}, exclusions: []domain.DeleteSelection{{ProjectID: "p", IDs: []string{"a"}}}}
	j := &journalFake{fail: true}
	s := NewLifecycleService(&identityFixture{}, repo, f, f, f, j, time.Now)
	job := domain.Job{Kind: "lifecycle.reconcile", Scope: domain.Scope{ProjectID: "p"}, PolicyRevision: "new", InputRevisionIDs: []string{"old", "new"}, InputDigest: captureID("p", "old", "new")}
	committer := &lifecycleCommitSpy{}
	require.ErrorIs(t, s.ReconcilePolicy(ctx, job, domain.Lease{}, committer), domain.ErrUnavailable)
	assert.True(t, f.excluded)
	assert.False(t, committer.called)
	assert.Equal(t, "pending_journal", f.op.Receipt.State)
	j.fail = false
	require.NoError(t, s.ReconcilePolicy(ctx, job, domain.Lease{}, committer))
	assert.True(t, committer.called)
	assert.Equal(t, "purge_pending", f.op.Receipt.State)
	repo.policy.Revision = "changed"
	s.repo = repo
	require.ErrorIs(t, s.ReconcilePolicy(ctx, job, domain.Lease{}, committer), domain.ErrStale)
}

type selectedLifecycleRepository struct {
	LifecycleRepository
	manifest   domain.DeletionManifest
	initErr    error
	objects    []domain.PurgeObject
	policy     domain.Policy
	exclusions []domain.DeleteSelection
}

func (r selectedLifecycleRepository) Select(ctx context.Context, tx Tx, v domain.DeleteSelection) (domain.DeletionManifest, error) {
	if r.manifest.ProjectID != "" {
		return r.manifest, nil
	}
	return r.LifecycleRepository.Select(ctx, tx, v)
}
func (r selectedLifecycleRepository) CanInitializeJournal(context.Context, Tx) error {
	return r.initErr
}
func (r selectedLifecycleRepository) PurgeObjects(context.Context, Tx, string) ([]domain.PurgeObject, error) {
	return r.objects, nil
}
func (r selectedLifecycleRepository) Policy(context.Context, Tx, string) (domain.Policy, error) {
	return r.policy, nil
}
func (r selectedLifecycleRepository) PolicyExclusions(_ context.Context, _ Tx, _ string, revision string, _ int) ([]domain.DeleteSelection, error) {
	if revision != r.policy.Revision {
		return nil, domain.ErrStale
	}
	return r.exclusions, nil
}

type lifecycleDeleteSpy struct {
	keys []string
	err  error
}

func (o *lifecycleDeleteSpy) Delete(_ context.Context, key string) error {
	o.keys = append(o.keys, key)
	return o.err
}

type lifecycleCommitSpy struct {
	called bool
	err    error
}

func (c *lifecycleCommitSpy) Commit(ctx context.Context, _ domain.Lease, _ string, fn func(context.Context, Tx) error) error {
	c.called = true
	if c.err != nil {
		return c.err
	}
	return fn(ctx, nil)
}

func (f *lifecycleFake) ExpiryEligible(context.Context, Tx, domain.DeleteSelection, time.Time) (bool, error) {
	return true, nil
}

func TestExpiryRechecksCurrentEligibilityBeforeLockedIntent(t *testing.T) {
	f := newLifecycleFake()
	repo := expiryChangedLifecycleRepository{LifecycleRepository: f}
	j := &journalFake{}
	s := NewLifecycleService(&identityFixture{}, repo, f, f, f, j, time.Now)
	report, err := s.Expire(context.Background(), time.Now().UTC())
	require.NoError(t, err)
	assert.Empty(t, report.Operations)
	assert.False(t, f.excluded)
	assert.Empty(t, j.records)
}

type expiryChangedLifecycleRepository struct{ LifecycleRepository }

func (r expiryChangedLifecycleRepository) Expiry(context.Context, Tx, time.Time, int) ([]domain.DeleteSelection, error) {
	return []domain.DeleteSelection{{ProjectID: "p", IDs: []string{"a"}}}, nil
}
func (r expiryChangedLifecycleRepository) ExpiryEligible(context.Context, Tx, domain.DeleteSelection, time.Time) (bool, error) {
	return false, nil
}
func TestPolicyReconciliationCommitsBoundedContinuation(t *testing.T) {
	f := newLifecycleFake()
	repo := &pagedPolicyLifecycleRepository{LifecycleRepository: f}
	s := NewLifecycleService(&identityFixture{}, repo, f, f, f, &journalFake{}, time.Now)
	job := domain.Job{ID: "job", Kind: "lifecycle.reconcile", Scope: domain.Scope{ProjectID: "p"}, PolicyRevision: "new", InputRevisionIDs: []string{"old", "new"}, InputDigest: captureID("p", "old", "new")}
	require.NoError(t, s.ReconcilePolicy(context.Background(), job, domain.Lease{}, fakeLifecycleCommitter{}))
	require.Len(t, f.enqueued, 1)
	next := f.enqueued[0]
	require.Len(t, next.InputRevisionIDs, 3)
	assert.NotEqual(t, job.ID, next.ID)
	require.NoError(t, s.ReconcilePolicy(context.Background(), next, domain.Lease{}, fakeLifecycleCommitter{}))
	assert.Equal(t, 2, repo.pages)
	assert.Len(t, f.enqueued, 1)
}

type pagedPolicyLifecycleRepository struct {
	LifecycleRepository
	pages int
}

func (r *pagedPolicyLifecycleRepository) ReconcilePolicy(context.Context, Tx, string, string, int) (bool, error) {
	r.pages++
	return r.pages == 1, nil
}

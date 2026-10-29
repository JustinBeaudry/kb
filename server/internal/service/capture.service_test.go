package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sort"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type captureRepo struct {
	artifacts map[string]domain.Artifact
	clients   map[string]string
	events    map[string]domain.Event
	origins   map[string]domain.Origin
	policy    domain.Policy
	checked   map[string]int64
}

func (r *captureRepo) Policy(context.Context, Tx, string) (domain.Policy, error) {
	return r.policy, nil
}
func (r *captureRepo) Artifact(_ context.Context, _ Tx, id string) (domain.Artifact, string, error) {
	a, ok := r.artifacts[id]
	if !ok {
		return a, "", domain.ErrIncomplete
	}
	return a, r.clients[id], nil
}
func (r *captureRepo) SaveArtifact(_ context.Context, _ Tx, a domain.Artifact, client string) error {
	r.artifacts[a.ID] = a
	r.clients[a.ID] = client
	return nil
}
func (r *captureRepo) Event(_ context.Context, _ Tx, key string) (domain.Event, domain.Origin, error) {
	e, ok := r.events[key]
	if !ok {
		return e, domain.Origin{}, domain.ErrIncomplete
	}
	return e, r.origins[key], nil
}
func (r *captureRepo) SaveEvent(_ context.Context, _ Tx, key string, e domain.Event, o domain.Origin) error {
	r.events[key] = e
	r.origins[key] = o
	return nil
}
func (r *captureRepo) Pending(context.Context, Tx, int) ([]domain.Artifact, error) {
	var a []domain.Artifact
	for _, v := range r.artifacts {
		a = append(a, v)
	}
	sort.Slice(a, func(i, j int) bool {
		if r.checked[a[i].ID] == r.checked[a[j].ID] {
			return a[i].ID < a[j].ID
		}
		return r.checked[a[i].ID] < r.checked[a[j].ID]
	})
	return a, nil
}

type captureObjects struct {
	data map[string][]byte
	fail bool
}

func (o *captureObjects) Put(_ context.Context, k string, r io.Reader, _ string) error {
	if o.fail {
		return domain.ErrUnavailable
	}
	b, e := io.ReadAll(r)
	o.data[k] = b
	return e
}
func (o *captureObjects) Open(_ context.Context, k string) (io.ReadCloser, error) {
	b, ok := o.data[k]
	if !ok {
		return nil, domain.ErrIncomplete
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}
func (o *captureObjects) Delete(_ context.Context, k string) error { delete(o.data, k); return nil }
func captureFixture(t *testing.T) (*CaptureService, *captureRepo, *captureObjects, domain.Principal, ArtifactInput) {
	t.Helper()
	r := &captureRepo{artifacts: map[string]domain.Artifact{}, clients: map[string]string{}, events: map[string]domain.Event{}, origins: map[string]domain.Origin{}, policy: domain.Policy{Revision: "policy", SpoolLimitBytes: MaxArtifactBytes}}
	o := &captureObjects{data: map[string][]byte{}}
	p := domain.Principal{ClientID: "client", OwnerID: "owner", Role: domain.RoleCollector, ProjectIDs: []string{"project"}, Capabilities: []string{"events:append", "artifacts:write", "evidence:read", "checkpoint:write"}}
	s := NewCaptureService(directTx{}, r, &fakeJobs{jobs: map[string]domain.Job{}}, o)
	sum := sha256.Sum256([]byte("exit_code=0"))
	return s, r, o, p, ArtifactInput{Scope: domain.Scope{ProjectID: "project", TaskID: "task", SessionID: "session"}, Digest: hex.EncodeToString(sum[:]), Bytes: 11, PolicyRevision: "policy", SourceLocator: "file:one#bytes=0-11"}
}
func readyCapture(t *testing.T, s *CaptureService, p domain.Principal, m ArtifactInput) domain.Artifact {
	t.Helper()
	a, e := s.Stage(t.Context(), p, m, bytes.NewBufferString("exit_code=0"))
	require.NoError(t, e)
	_, e = s.Complete(t.Context(), p, a.ID, a.Digest)
	require.NoError(t, e)
	return a
}
func eventFor(a domain.Artifact) domain.Event {
	return domain.Event{ID: "event", IdempotencyKey: "key", Digest: a.Digest, Scope: a.Scope, Kind: domain.EventToolResult, ObservedAt: time.Now().UTC(), PayloadArtifactID: a.ID, Completeness: domain.CompletenessComplete}
}
func TestReusedEventKeyWithDifferentDigestConflicts(t *testing.T) {
	s, _, _, p, m := captureFixture(t)
	a := readyCapture(t, s, p, m)
	e := eventFor(a)
	_, err := s.Append(t.Context(), p, []domain.Event{e})
	require.NoError(t, err)
	e.Digest = TokenDigest("different")
	_, err = s.Append(t.Context(), p, []domain.Event{e})
	assert.ErrorIs(t, err, domain.ErrConflict)
}
func TestIncompleteArtifactCannotSupportClaim(t *testing.T) {
	s, _, _, p, m := captureFixture(t)
	a, err := s.Stage(t.Context(), p, m, bytes.NewBufferString("exit_code=0"))
	require.NoError(t, err)
	_, err = s.Reference(t.Context(), p, domain.EvidenceRef{ArtifactID: a.ID, Digest: a.Digest, Locator: m.SourceLocator}, a.Scope)
	assert.ErrorIs(t, err, domain.ErrIncomplete)
}
func TestForgedExitCodeStaysSubmitted(t *testing.T) {
	s, r, _, p, m := captureFixture(t)
	a := readyCapture(t, s, p, m)
	_, err := s.Append(t.Context(), p, []domain.Event{eventFor(a)})
	require.NoError(t, err)
	require.Len(t, r.origins, 1)
	for _, storedOrigin := range r.origins {
		assert.Equal(t, "submitted", string(storedOrigin.Trust))
	}
}
func TestCheckpointRejectsWrongTaskOwner(t *testing.T) {
	r := &checkpointRepo{}
	s := NewCheckpointService(directTx{}, r)
	p := domain.Principal{ClientID: "first", OwnerID: "owner", Role: domain.RoleAgent, ProjectIDs: []string{"project"}, Capabilities: []string{"checkpoint:write"}}
	c := domain.Checkpoint{ID: "checkpoint", Scope: domain.Scope{ProjectID: "project", TaskID: "task"}, Objective: "test"}
	_, err := s.Put(t.Context(), p, "", c)
	require.NoError(t, err)
	p.ClientID = "other"
	_, err = s.Put(t.Context(), p, r.c.Revision, c)
	assert.ErrorIs(t, err, domain.ErrForbidden)
}

type checkpointRepo struct {
	c      domain.Checkpoint
	client string
}

func (r *checkpointRepo) Get(context.Context, Tx, string) (domain.Checkpoint, string, error) {
	return r.c, r.client, nil
}
func (r *checkpointRepo) TaskOwner(context.Context, Tx, domain.Scope) (string, error) {
	return r.client, nil
}
func (r *checkpointRepo) Save(_ context.Context, _ Tx, c domain.Checkpoint, client string) error {
	r.c = c
	r.client = client
	return nil
}

type directTx struct{}

func (directTx) Within(ctx context.Context, fn func(context.Context, Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(ctx, nil)
}

type fakeJobs struct{ jobs map[string]domain.Job }

func (r *fakeJobs) Enqueue(_ context.Context, _ Tx, j domain.Job) error { r.jobs[j.ID] = j; return nil }
func TestCaptureSweepRecoversStagedAndReportsMissing(t *testing.T) {
	s, r, o, p, m := captureFixture(t)
	a, err := s.Stage(t.Context(), p, m, bytes.NewBufferString("exit_code=0"))
	require.NoError(t, err)
	err = s.Sweep(t.Context(), 100)
	require.NoError(t, err)
	assert.Equal(t, domain.ArtifactReady, r.artifacts[a.ID].State)
	assert.True(t, r.artifacts[a.ID].ObjectAvailable)
	delete(o.data, a.ObjectKey)
	assert.ErrorIs(t, s.Sweep(t.Context(), 100), domain.ErrIncomplete)
	assert.False(t, r.artifacts[a.ID].ObjectAvailable)
}

func (r *captureRepo) Checked(_ context.Context, _ Tx, id, key string, at int64) error {
	if r.checked == nil {
		r.checked = map[string]int64{}
	}
	r.checked[id] = at
	return nil
}

func (r *captureRepo) Availability(_ context.Context, _ Tx, id, key string, available bool) error {
	a := r.artifacts[id]
	if a.ObjectKey != key {
		return domain.ErrStale
	}
	a.ObjectAvailable = available
	r.artifacts[id] = a
	return nil
}

func TestCaptureUploadPolicyAndIntegrity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*captureRepo, *captureObjects, *domain.Principal, *ArtifactInput)
		body  string
		want  error
	}{
		{"missing authenticated policy", func(r *captureRepo, _ *captureObjects, _ *domain.Principal, _ *ArtifactInput) { r.policy.Revision = "" }, "exit_code=0", domain.ErrForbidden},
		{"paused", func(r *captureRepo, _ *captureObjects, _ *domain.Principal, _ *ArtifactInput) { r.policy.Paused = true }, "exit_code=0", domain.ErrForbidden},
		{"stale revision", func(_ *captureRepo, _ *captureObjects, _ *domain.Principal, m *ArtifactInput) {
			m.PolicyRevision = "old"
		}, "exit_code=0", domain.ErrStale},
		{"spool budget", func(r *captureRepo, _ *captureObjects, _ *domain.Principal, _ *ArtifactInput) {
			r.policy.SpoolLimitBytes = 1
		}, "exit_code=0", domain.ErrBudget},
		{"missing path exclusion metadata", func(r *captureRepo, _ *captureObjects, _ *domain.Principal, _ *ArtifactInput) {
			r.policy.ExcludedPaths = []string{"private"}
		}, "exit_code=0", domain.ErrForbidden},
		{"excluded directory", func(r *captureRepo, _ *captureObjects, _ *domain.Principal, m *ArtifactInput) {
			r.policy.ExcludedPaths = []string{"private/"}
			m.SourcePath = "private/transcript"
		}, "exit_code=0", domain.ErrForbidden},
		{"excluded exact", func(r *captureRepo, _ *captureObjects, _ *domain.Principal, m *ArtifactInput) {
			r.policy.ExcludedPaths = []string{"transcript"}
			m.SourcePath = "transcript"
		}, "exit_code=0", domain.ErrForbidden},
		{"path traversal", func(_ *captureRepo, _ *captureObjects, _ *domain.Principal, m *ArtifactInput) {
			m.SourcePath = "../transcript"
		}, "exit_code=0", domain.ErrInvalidInput},
		{"wrong project", func(_ *captureRepo, _ *captureObjects, _ *domain.Principal, m *ArtifactInput) {
			m.Scope.ProjectID = "private"
		}, "exit_code=0", domain.ErrForbidden},
		{"no capability", func(_ *captureRepo, _ *captureObjects, p *domain.Principal, _ *ArtifactInput) { p.Capabilities = nil }, "exit_code=0", domain.ErrForbidden},
		{"bad digest", func(_ *captureRepo, _ *captureObjects, _ *domain.Principal, m *ArtifactInput) { m.Digest = "bad" }, "exit_code=0", domain.ErrInvalidInput},
		{"oversized metadata", func(_ *captureRepo, _ *captureObjects, _ *domain.Principal, m *ArtifactInput) {
			m.Bytes = MaxArtifactBytes + 1
		}, "exit_code=0", domain.ErrInvalidInput},
		{"wrong bytes", func(_ *captureRepo, _ *captureObjects, _ *domain.Principal, m *ArtifactInput) { m.Bytes = 1 }, "exit_code=0", domain.ErrInvalidInput},
		{"wrong content", func(_ *captureRepo, _ *captureObjects, _ *domain.Principal, _ *ArtifactInput) {}, "exit_code=1", domain.ErrInvalidInput},
		{"object upload fails", func(_ *captureRepo, o *captureObjects, _ *domain.Principal, _ *ArtifactInput) { o.fail = true }, "exit_code=0", domain.ErrUnavailable},
		{"unrelated exclusions work", func(r *captureRepo, _ *captureObjects, _ *domain.Principal, m *ArtifactInput) {
			r.policy.ExcludedPaths = []string{"private"}
			r.policy.ExcludedFields = []string{"secret"}
			m.SourcePath = "private-other/transcript"
		}, "exit_code=0", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r, o, p, m := captureFixture(t)
			tc.setup(r, o, &p, &m)
			_, err := s.Stage(t.Context(), p, m, bytes.NewBufferString(tc.body))
			if tc.want == nil {
				require.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, tc.want)
			}
		})
	}
}
func TestCaptureDedupeAndReferenceAuthorization(t *testing.T) {
	s, r, o, p, m := captureFixture(t)
	a := readyCapture(t, s, p, m)
	e := eventFor(a)
	_, err := s.Append(t.Context(), p, []domain.Event{e})
	require.NoError(t, err)
	receipts, err := s.Append(t.Context(), p, []domain.Event{e})
	require.NoError(t, err)
	require.Len(t, receipts, 1)
	assert.True(t, receipts[0].Duplicate)
	ref := domain.EvidenceRef{ArtifactID: a.ID, Digest: a.Digest, Locator: m.SourceLocator, Origin: domain.Origin{Trust: domain.TrustGatewayObserved}}
	for _, tc := range []struct {
		name string
		edit func(*domain.Principal, *domain.EvidenceRef, *domain.Scope)
		want error
	}{
		{"authorized submitted", func(_ *domain.Principal, _ *domain.EvidenceRef, _ *domain.Scope) {}, nil},
		{"wrong locator", func(_ *domain.Principal, r *domain.EvidenceRef, _ *domain.Scope) { r.Locator = "other" }, domain.ErrConflict},
		{"wrong digest", func(_ *domain.Principal, r *domain.EvidenceRef, _ *domain.Scope) { r.Digest = "wrong" }, domain.ErrConflict},
		{"wrong task", func(_ *domain.Principal, _ *domain.EvidenceRef, s *domain.Scope) { s.TaskID = "other" }, domain.ErrForbidden},
		{"wrong project", func(p *domain.Principal, _ *domain.EvidenceRef, _ *domain.Scope) { p.ProjectIDs = []string{"other"} }, domain.ErrForbidden},
		{"no read capability", func(p *domain.Principal, _ *domain.EvidenceRef, _ *domain.Scope) { p.Capabilities = nil }, domain.ErrForbidden},
		{"unknown artifact", func(_ *domain.Principal, r *domain.EvidenceRef, _ *domain.Scope) { r.ArtifactID = "unknown" }, domain.ErrForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pp, rr, scope := p, ref, m.Scope
			tc.edit(&pp, &rr, &scope)
			got, err := s.Reference(t.Context(), pp, rr, scope)
			if tc.want != nil {
				assert.ErrorIs(t, err, tc.want)
			} else {
				require.NoError(t, err)
				assert.Equal(t, domain.TrustSubmitted, got.Origin.Trust)
				assert.Equal(t, p.ClientID, got.Origin.ClientID)
			}
		})
	}
	t.Run("other client cannot complete", func(t *testing.T) {
		other := p
		other.ClientID = "other"
		_, err := s.Complete(t.Context(), other, a.ID, a.Digest)
		assert.ErrorIs(t, err, domain.ErrForbidden)
	})
	t.Run("ready retry immutable identity", func(t *testing.T) {
		again, err := s.Stage(t.Context(), p, m, bytes.NewBufferString("exit_code=0"))
		require.NoError(t, err)
		assert.Equal(t, a.ID, again.ID)
		receipt, err := s.Complete(t.Context(), p, a.ID, a.Digest)
		require.NoError(t, err)
		assert.True(t, receipt.Duplicate)
	})
	t.Run("corrupt object cannot support claim", func(t *testing.T) {
		o.data[a.ObjectKey] = []byte("corrupt")
		_, err := s.Reference(t.Context(), p, ref, m.Scope)
		assert.ErrorIs(t, err, domain.ErrIncomplete)
		assert.False(t, r.artifacts[a.ID].ObjectAvailable)
	})
	t.Run("changed policy prevents ready replay", func(t *testing.T) {
		r.policy.Revision = "new"
		_, err := s.Complete(t.Context(), p, a.ID, a.Digest)
		assert.ErrorIs(t, err, domain.ErrStale)
	})
}
func TestCaptureRejectsMalformedEvents(t *testing.T) {
	for _, edit := range []func(*domain.Event){func(e *domain.Event) { e.ID = "" }, func(e *domain.Event) { e.Kind = "forged" }, func(e *domain.Event) { e.Completeness = "forged" }, func(e *domain.Event) { e.ObservedAt = time.Time{} }, func(e *domain.Event) { e.Sequence = ^uint64(0) }} {
		s, _, _, p, m := captureFixture(t)
		a := readyCapture(t, s, p, m)
		e := eventFor(a)
		edit(&e)
		_, err := s.Append(t.Context(), p, []domain.Event{e})
		assert.ErrorIs(t, err, domain.ErrInvalidInput)
	}
}
func TestCheckpointExpectedRevision(t *testing.T) {
	r := &checkpointRepo{}
	s := NewCheckpointService(directTx{}, r)
	p := domain.Principal{ClientID: "first", ProjectIDs: []string{"project"}, Capabilities: []string{"checkpoint:write"}}
	c := domain.Checkpoint{ID: "c", Scope: domain.Scope{ProjectID: "project", TaskID: "task"}, Objective: "test"}
	initial, err := s.Put(t.Context(), p, "", c)
	require.NoError(t, err)
	_, err = s.Put(t.Context(), p, "", c)
	assert.ErrorIs(t, err, domain.ErrConflict)
	updated, err := s.Put(t.Context(), p, initial.Revision, c)
	require.NoError(t, err)
	assert.NotEqual(t, initial.Revision, updated.Revision)
	c.Scope.TaskID = "other"
	_, err = s.Put(t.Context(), p, updated.Revision, c)
	assert.ErrorIs(t, err, domain.ErrForbidden)
}

type policyChangingObjects struct {
	ObjectStore
	afterPut  func()
	afterOpen func()
}

func (o policyChangingObjects) Put(ctx context.Context, k string, b io.Reader, d string) error {
	err := o.ObjectStore.Put(ctx, k, b, d)
	if o.afterPut != nil {
		o.afterPut()
	}
	return err
}
func (o policyChangingObjects) Open(ctx context.Context, k string) (io.ReadCloser, error) {
	r, err := o.ObjectStore.Open(ctx, k)
	if o.afterOpen != nil {
		o.afterOpen()
	}
	return r, err
}
func TestCapturePolicyChangesDuringObjectIO(t *testing.T) {
	t.Run("upload acknowledgement rechecks", func(t *testing.T) {
		s, r, o, p, m := captureFixture(t)
		s.objects = policyChangingObjects{ObjectStore: o, afterPut: func() { r.policy.Paused = true }}
		_, err := s.Stage(t.Context(), p, m, bytes.NewBufferString("exit_code=0"))
		assert.ErrorIs(t, err, domain.ErrForbidden)
		for _, a := range r.artifacts {
			assert.Equal(t, domain.ArtifactStaged, a.State)
		}
	})
	t.Run("ready publication rechecks", func(t *testing.T) {
		s, r, o, p, m := captureFixture(t)
		a, err := s.Stage(t.Context(), p, m, bytes.NewBufferString("exit_code=0"))
		require.NoError(t, err)
		s.objects = policyChangingObjects{ObjectStore: o, afterOpen: func() { r.policy.Revision = "new" }}
		_, err = s.Complete(t.Context(), p, a.ID, a.Digest)
		assert.ErrorIs(t, err, domain.ErrStale)
		assert.Equal(t, domain.ArtifactStaged, r.artifacts[a.ID].State)
	})
}
func TestCaptureBoundaryValidation(t *testing.T) {
	s, _, _, p, _ := captureFixture(t)
	_, err := s.Complete(t.Context(), p, "id", "bad")
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
	_, err = s.Reference(t.Context(), p, domain.EvidenceRef{}, domain.Scope{})
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
	_, err = s.Append(t.Context(), p, nil)
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
	assert.ErrorIs(t, s.Sweep(t.Context(), 101), domain.ErrInvalidInput)
	_, err = NewCheckpointService(nil, nil).Put(t.Context(), p, "", domain.Checkpoint{})
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
}

type directCommit struct{}

func (directCommit) Commit(ctx context.Context, _ domain.Lease, _ string, fn func(context.Context, Tx) error) error {
	return fn(ctx, nil)
}
func TestCaptureFailedChecksInvalidateEveryPath(t *testing.T)  { testCaptureFailedChecks(t, false) }
func TestCaptureCorruptChecksInvalidateEveryPath(t *testing.T) { testCaptureFailedChecks(t, true) }
func testCaptureFailedChecks(t *testing.T, corrupt bool) {
	t.Helper()
	for _, operation := range []string{"complete", "append", "reconcile"} {
		t.Run(operation, func(t *testing.T) {
			s, r, o, p, m := captureFixture(t)
			a := readyCapture(t, s, p, m)
			if corrupt {
				o.data[a.ObjectKey] = []byte("corrupt bytes")
			} else {
				delete(o.data, a.ObjectKey)
			}
			var err error
			switch operation {
			case "complete":
				_, err = s.Complete(t.Context(), p, a.ID, a.Digest)
			case "append":
				_, err = s.Append(t.Context(), p, []domain.Event{eventFor(a)})
			case "reconcile":
				for _, j := range s.jobs.(*fakeJobs).jobs {
					if j.Kind == "capture.reconcile" {
						err = s.Reconcile(t.Context(), j, domain.Lease{}, directCommit{})
					}
				}
			}
			assert.ErrorIs(t, err, domain.ErrIncomplete)
			assert.False(t, r.artifacts[a.ID].ObjectAvailable)
			assert.Equal(t, domain.ArtifactReady, r.artifacts[a.ID].State)
		})
	}
}

type slowObjects struct {
	ObjectStore
	slowKey string
}

func (o slowObjects) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if key == o.slowKey {
		<-ctx.Done()
		return nil, domain.ErrUnavailable
	}
	return o.ObjectStore.Open(ctx, key)
}
func TestCaptureSweepAdvancesAfterSlowObject(t *testing.T) {
	s, r, o, p, m := captureFixture(t)
	slow := readyCapture(t, s, p, m)
	m.SourceLocator = "file:two"
	later := readyCapture(t, s, p, m)
	r.checked = map[string]int64{slow.ID: 0, later.ID: 1}
	s.objects = slowObjects{ObjectStore: o, slowKey: slow.ObjectKey}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	t.Cleanup(cancel)
	err := s.Sweep(ctx, 2)
	assert.ErrorIs(t, err, domain.ErrUnavailable)
	assert.Greater(t, r.checked[slow.ID], int64(1))
	assert.Greater(t, r.checked[later.ID], int64(1))
	assert.False(t, r.artifacts[slow.ID].ObjectAvailable)
	assert.True(t, r.artifacts[later.ID].ObjectAvailable)
}
func TestCaptureSweepExpiresAbandonedAndAllowsFreshRetry(t *testing.T) {
	s, r, o, p, m := captureFixture(t)
	a, err := s.Stage(t.Context(), p, m, bytes.NewBufferString("exit_code=0"))
	require.NoError(t, err)
	expired := time.Now().UTC().Add(-time.Hour)
	staged := r.artifacts[a.ID]
	staged.ExpiresAt = &expired
	r.artifacts[a.ID] = staged
	require.NoError(t, s.Sweep(t.Context(), 100))
	assert.Equal(t, "abandoned", string(r.artifacts[a.ID].State))
	assert.NotContains(t, o.data, a.ObjectKey)
	replacement, err := s.Stage(t.Context(), p, m, bytes.NewBufferString("exit_code=0"))
	require.NoError(t, err)
	assert.Equal(t, a.ID, replacement.ID)
	assert.NotEqual(t, a.ObjectKey, replacement.ObjectKey)
	_, err = s.Complete(t.Context(), p, replacement.ID, replacement.Digest)
	require.NoError(t, err)
}

func (r *captureRepo) UploadLease(_ context.Context, _ Tx, id, key, lease string, until time.Time) error {
	a := r.artifacts[id]
	if a.ObjectKey != key {
		return domain.ErrStale
	}
	a.UploadLeaseID = lease
	a.UploadLeaseUntil = until
	r.artifacts[id] = a
	return nil
}

type heldDeleteObjects struct {
	ObjectStore
	entered chan struct{}
	release chan struct{}
}

func (o heldDeleteObjects) Delete(ctx context.Context, key string) error {
	close(o.entered)
	select {
	case <-o.release:
		return o.ObjectStore.Delete(ctx, key)
	case <-ctx.Done():
		return domain.ErrUnavailable
	}
}
func TestCaptureCleanupClaimFencesRetryAndCompletion(t *testing.T) {
	s, r, o, p, m := captureFixture(t)
	a, err := s.Stage(t.Context(), p, m, bytes.NewBufferString("exit_code=0"))
	require.NoError(t, err)
	expire := time.Now().UTC().Add(-time.Minute)
	staged := r.artifacts[a.ID]
	staged.ExpiresAt = &expire
	r.artifacts[a.ID] = staged
	entered, release := make(chan struct{}), make(chan struct{})
	s.objects = heldDeleteObjects{ObjectStore: o, entered: entered, release: release}
	result := make(chan error, 1)
	go func() { result <- s.Sweep(t.Context(), 100) }()
	<-entered
	_, err = s.Stage(t.Context(), p, m, bytes.NewBufferString("exit_code=0"))
	assert.ErrorIs(t, err, domain.ErrIncomplete)
	_, err = s.Complete(t.Context(), p, a.ID, a.Digest)
	assert.ErrorIs(t, err, domain.ErrIncomplete)
	close(release)
	require.NoError(t, <-result)
	s.objects = o
	_, err = s.Complete(t.Context(), p, a.ID, a.Digest)
	assert.ErrorIs(t, err, domain.ErrIncomplete)
	replacement, err := s.Stage(t.Context(), p, m, bytes.NewBufferString("exit_code=0"))
	require.NoError(t, err)
	assert.NotEqual(t, a.Generation, replacement.Generation)
	_, err = s.Complete(t.Context(), p, replacement.ID, replacement.Digest)
	require.NoError(t, err)
	var extracts int
	for _, j := range s.jobs.(*fakeJobs).jobs {
		if j.Kind == "evidence.extract" {
			extracts++
		}
	}
	assert.Equal(t, 1, extracts)
}

type failedDeleteObjects struct{ ObjectStore }

func (failedDeleteObjects) Delete(context.Context, string) error { return domain.ErrUnavailable }
func TestCaptureCleanupFailurePersistsAndPausedPolicyCanRetire(t *testing.T) {
	s, r, o, p, m := captureFixture(t)
	a, err := s.Stage(t.Context(), p, m, bytes.NewBufferString("exit_code=0"))
	require.NoError(t, err)
	expired := time.Now().UTC().Add(-time.Hour)
	staged := r.artifacts[a.ID]
	staged.ExpiresAt = &expired
	r.artifacts[a.ID] = staged
	r.policy.Paused = true
	s.objects = failedDeleteObjects{ObjectStore: o}
	assert.ErrorIs(t, s.Sweep(t.Context(), 100), domain.ErrUnavailable)
	assert.Equal(t, domain.ArtifactAbandoning, r.artifacts[a.ID].State)
	assert.Contains(t, o.data, a.ObjectKey)
	s.objects = o
	require.NoError(t, s.Sweep(t.Context(), 100))
	assert.Equal(t, domain.ArtifactAbandoned, r.artifacts[a.ID].State)
	assert.NotContains(t, o.data, a.ObjectKey)
}
func TestCaptureRetryLeaseAndDeadline(t *testing.T) {
	s, r, o, p, m := captureFixture(t)
	a, err := s.Stage(t.Context(), p, m, bytes.NewBufferString("exit_code=0"))
	require.NoError(t, err)
	require.NotNil(t, a.ExpiresAt)
	assert.WithinDuration(t, time.Now().UTC().Add(CaptureAbandonAfter), *a.ExpiresAt, time.Second)
	held := r.artifacts[a.ID]
	held.UploadLeaseID = "active"
	held.UploadLeaseUntil = time.Now().UTC().Add(CaptureUploadLease)
	expired := time.Now().UTC().Add(-time.Hour)
	held.ExpiresAt = &expired
	r.artifacts[a.ID] = held
	_, err = s.Stage(t.Context(), p, m, bytes.NewBufferString("exit_code=0"))
	assert.ErrorIs(t, err, domain.ErrConflict)
	assert.ErrorIs(t, s.Sweep(t.Context(), 100), domain.ErrIncomplete)
	assert.Contains(t, o.data, a.ObjectKey)
	assert.Equal(t, domain.ArtifactStaged, r.artifacts[a.ID].State)
	held.UploadLeaseUntil = time.Now().UTC().Add(-time.Second)
	r.artifacts[a.ID] = held
	retried, err := s.Stage(t.Context(), p, m, bytes.NewBufferString("exit_code=0"))
	require.NoError(t, err)
	assert.Equal(t, a.ObjectKey, retried.ObjectKey)
	_, err = s.Complete(t.Context(), p, a.ID, a.Digest)
	require.NoError(t, err)
}
func TestCaptureInvalidBodyCreatesNoManifest(t *testing.T) {
	s, r, _, p, m := captureFixture(t)
	_, err := s.Stage(t.Context(), p, m, bytes.NewBufferString("wrong"))
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
	assert.Empty(t, r.artifacts)
	assert.Empty(t, s.jobs.(*fakeJobs).jobs)
}
func TestCaptureOldGenerationCannotInvalidateReplacement(t *testing.T) {
	s, r, o, p, m := captureFixture(t)
	a := readyCapture(t, s, p, m)
	s.objects = policyChangingObjects{ObjectStore: o, afterOpen: func() {
		changed := r.artifacts[a.ID]
		changed.Generation = "replacement"
		changed.ObjectKey = "replacement-key"
		r.artifacts[a.ID] = changed
	}}
	delete(o.data, a.ObjectKey)
	_, err := s.Complete(t.Context(), p, a.ID, a.Digest)
	assert.ErrorIs(t, err, domain.ErrIncomplete)
	assert.True(t, r.artifacts[a.ID].ObjectAvailable)
}

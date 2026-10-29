package service

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io"
	"strings"
	"testing"
)

type retrievalFixture struct {
	fail          string
	unavailable   bool
	snapshotCalls int
	claims        []domain.ClaimRevision
	eligible      map[string]bool
	snapshot      RetrievalSnapshot
	checkpoint    domain.Checkpoint
	examined      int
}

func (f *retrievalFixture) Snapshot(context.Context, Tx, domain.Principal, domain.Scope) (RetrievalSnapshot, error) {
	f.snapshotCalls++
	if f.fail == "snapshot" {
		return f.snapshot, domain.ErrUnavailable
	}
	if f.fail == "final epoch" && f.snapshotCalls > 1 {
		v := f.snapshot
		v.KnowledgeEpoch++
		return v, nil
	}
	return f.snapshot, nil
}
func (f *retrievalFixture) Seeds(_ context.Context, _ Tx, _ domain.Scope, _ string, limit int) ([]domain.ClaimRevision, error) {
	if f.fail == "seeds" {
		return nil, domain.ErrUnavailable
	}
	n := min(limit, len(f.claims))
	f.examined += n
	return f.claims[:n], nil
}
func (f *retrievalFixture) Neighbors(_ context.Context, _ Tx, _ []string, _ []string, limit int) ([]domain.ClaimRevision, error) {
	if f.fail == "neighbors" {
		return nil, domain.ErrUnavailable
	}
	n := min(limit, len(f.claims))
	f.examined += n
	return f.claims[:n], nil
}
func (f *retrievalFixture) Eligible(_ context.Context, _ Tx, _ domain.Principal, ids []string) (EligibilityResult, error) {
	if f.fail == "eligible" {
		return EligibilityResult{}, domain.ErrUnavailable
	}
	r := EligibilityResult{Snapshot: f.snapshot, Unavailable: f.unavailable}
	for _, id := range ids {
		for _, c := range f.claims {
			if c.Revision == id && f.eligible[id] {
				r.Claims = append(r.Claims, c)
			}
		}
	}
	return r, nil
}
func (f *retrievalFixture) Latest(context.Context, Tx, domain.Scope) (domain.Checkpoint, string, error) {
	if f.fail == "task" {
		return domain.Checkpoint{}, "", domain.ErrUnavailable
	}
	return f.checkpoint, f.snapshot.Principal.ClientID, nil
}
func retrievalSetup(t *testing.T) (*RetrievalService, *retrievalFixture, domain.Principal, domain.RetrievalRequest) {
	t.Helper()
	p := domain.Principal{ClientID: "agent", ProjectIDs: []string{"demo"}, Capabilities: []string{"retrieval:read"}}
	f := &retrievalFixture{eligible: map[string]bool{}, snapshot: RetrievalSnapshot{Principal: p}}
	s, e := NewRetrievalService(directTx{}, f, f, nil, nil, []byte(strings.Repeat("k", 32)))
	require.NoError(t, e)
	return s, f, p, domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "demo"}, Mode: domain.RetrievalOrientation, BudgetBytes: 4096, MaxDepth: 2, MaxVisited: 50}
}
func TestRetrievalBudgetsAndTrust(t *testing.T) {
	for _, mode := range []domain.RetrievalMode{domain.RetrievalOrientation, domain.RetrievalEvidence, domain.RetrievalWorking} {
		t.Run(string(mode), func(t *testing.T) {
			s, f, p, r := retrievalSetup(t)
			r.Mode = mode
			if mode == domain.RetrievalWorking {
				r.Scope.TaskID = "task"
			}
			if mode == domain.RetrievalEvidence {
				r.BudgetBytes = 16384
			}
			for i := range 80 {
				id := fmt.Sprintf("revision-%03d", i)
				f.claims = append(f.claims, domain.ClaimRevision{ClaimID: id, Revision: id, Text: strings.Repeat("<猫>\"\\\n", 30), Scope: domain.Scope{ProjectID: "demo"}, Status: domain.ClaimVerified})
				f.eligible[id] = true
			}
			f.claims[0].Status = domain.ClaimCandidate
			f.claims[0].Scope.TaskID = "task"
			result, e := s.Retrieve(t.Context(), p, r)
			require.NoError(t, e)
			wire, e := domain.RetrievalWire(result)
			require.NoError(t, e)
			assert.LessOrEqual(t, len(wire)+1, r.BudgetBytes)
			assert.LessOrEqual(t, result.Visited, 50)
			assert.LessOrEqual(t, f.examined, 50)
			var ids []string
			for _, c := range result.Chunks {
				ids = append(ids, c.Revision)
			}
			if mode != domain.RetrievalWorking {
				assert.NotContains(t, ids, "revision-000")
			}
			assert.True(t, json.Valid(wire))
			assert.True(t, result.Truncated)
		})
	}
	t.Run("tiny", func(t *testing.T) {
		s, _, p, r := retrievalSetup(t)
		r.BudgetBytes = 1
		_, e := s.Retrieve(t.Context(), p, r)
		assert.ErrorIs(t, e, domain.ErrBudget)
	})
	t.Run("cycle", func(t *testing.T) {
		s, f, p, r := retrievalSetup(t)
		f.claims = []domain.ClaimRevision{{ClaimID: "one", Revision: "one", Text: "cycle", Scope: r.Scope, Status: domain.ClaimVerified}}
		f.eligible["one"] = true
		v, e := s.Retrieve(t.Context(), p, r)
		require.NoError(t, e)
		assert.LessOrEqual(t, v.Visited, 3)
	})
}
func TestRevokedCursorCannotFetch(t *testing.T) {
	s, f, p, r := retrievalSetup(t)
	for i := range 10 {
		id := fmt.Sprint(i)
		f.claims = append(f.claims, domain.ClaimRevision{ClaimID: id, Revision: id, Text: strings.Repeat("x", 500), Scope: r.Scope, Status: domain.ClaimVerified})
		f.eligible[id] = true
	}
	r.BudgetBytes = 1800
	first, e := s.Retrieve(t.Context(), p, r)
	require.NoError(t, e)
	require.NotEmpty(t, first.Continuation)
	r.Continuation = first.Continuation
	f.snapshot.AuthEpoch++
	_, e = s.Retrieve(t.Context(), p, r)
	assert.ErrorIs(t, e, domain.ErrStale)
}
func TestInvalidatedDependencyExcludedWithStaleIndex(t *testing.T) {
	s, f, p, r := retrievalSetup(t)
	f.claims = []domain.ClaimRevision{{ClaimID: "claim", Revision: "stale", Text: "hidden", Scope: r.Scope, Status: domain.ClaimVerified, DependsOn: []string{"invalid"}}}
	v, e := s.Retrieve(t.Context(), p, r)
	require.NoError(t, e)
	assert.Empty(t, v.Chunks)
	assert.True(t, v.NoResults)
	assert.Equal(t, 1, v.Visited)
}
func TestCrossProjectLessonHidesSourceMetadata(t *testing.T) {
	s, f, p, r := retrievalSetup(t)
	f.claims = []domain.ClaimRevision{{ClaimID: "lesson", Revision: "safe", Text: "general lesson", Status: domain.ClaimVerified}}
	f.eligible["safe"] = true
	v, e := s.Retrieve(t.Context(), p, r)
	require.NoError(t, e)
	require.Len(t, v.Chunks, 1)
	wire, e := domain.RetrievalWire(v)
	require.NoError(t, e)
	assert.NotContains(t, string(wire), "secret")
	assert.Empty(t, v.Chunks[0].Evidence)
}
func TestConflictingVersionsRemainVisible(t *testing.T) {
	s, f, p, r := retrievalSetup(t)
	for _, id := range []string{"a", "b"} {
		f.claims = append(f.claims, domain.ClaimRevision{ClaimID: "same", Revision: id, Text: id, Scope: r.Scope, Status: domain.ClaimDisputed})
		f.eligible[id] = true
	}
	v, e := s.Retrieve(t.Context(), p, r)
	require.NoError(t, e)
	require.Len(t, v.Chunks, 2)
	for _, c := range v.Chunks {
		assert.Contains(t, c.Warnings, "conflicting_versions")
	}
}
func TestRetrievalTaskState(t *testing.T) {
	for _, test := range []string{"resume", "other owner", "oversized"} {
		t.Run(test, func(t *testing.T) {
			s, f, p, r := retrievalSetup(t)
			r.Scope.TaskID = "task"
			f.snapshot.TaskOwner = p.ClientID
			f.snapshot.TaskHead = "head"
			f.checkpoint = domain.Checkpoint{ID: "cp", Revision: "head", Scope: r.Scope, Objective: "resume", Pending: []string{"next"}}
			if test == "other owner" {
				f.snapshot.TaskOwner = "other"
			}
			if test == "oversized" {
				f.checkpoint.Objective = strings.Repeat("猫<", 5000)
			}
			v, e := s.Retrieve(t.Context(), p, r)
			if test == "other owner" {
				assert.ErrorIs(t, e, domain.ErrForbidden)
				return
			}
			require.NoError(t, e)
			assert.Empty(t, v.Chunks)
			if test == "resume" {
				require.NotNil(t, v.TaskCheckpoint)
				assert.Equal(t, "task_state", v.TaskCheckpoint.Label)
				assert.False(t, v.NoResults)
			} else {
				assert.True(t, v.TaskStateOmitted)
				assert.Nil(t, v.TaskCheckpoint)
			}
			wire, _ := domain.RetrievalWire(v)
			assert.LessOrEqual(t, len(wire)+1, 4096)
		})
	}
}

func TestRetrievalContinuationAndInputValidation(t *testing.T) {
	s, f, p, r := retrievalSetup(t)
	for i := range 8 {
		id := fmt.Sprint(i)
		f.claims = append(f.claims, domain.ClaimRevision{ClaimID: id, Revision: id, Text: strings.Repeat("x", 500), Scope: r.Scope, Status: domain.ClaimVerified})
		f.eligible[id] = true
	}
	r.BudgetBytes = 1800
	first, e := s.Retrieve(t.Context(), p, r)
	require.NoError(t, e)
	require.NotEmpty(t, first.Continuation)
	r.Continuation = first.Continuation
	second, e := s.Retrieve(t.Context(), p, r)
	require.NoError(t, e)
	require.NotEmpty(t, second.Chunks)
	assert.NotEqual(t, first.Chunks[0].Revision, second.Chunks[0].Revision)
	for _, raw := range []string{"garbage", "a.b", "e30.invalid", strings.Repeat("x", 2049)} {
		r.Continuation = raw
		_, e = s.Retrieve(t.Context(), p, r)
		assert.ErrorIs(t, e, domain.ErrStale)
	}
	r.Continuation = first.Continuation
	r.Query = "other"
	_, e = s.Retrieve(t.Context(), p, r)
	assert.ErrorIs(t, e, domain.ErrStale)
	for _, change := range []func(*domain.RetrievalRequest){func(r *domain.RetrievalRequest) { r.Scope.ProjectID = "" }, func(r *domain.RetrievalRequest) { r.Mode = "bad" }, func(r *domain.RetrievalRequest) { r.Mode = domain.RetrievalWorking }, func(r *domain.RetrievalRequest) { r.BudgetBytes = 20000 }, func(r *domain.RetrievalRequest) { r.MaxVisited = 51 }, func(r *domain.RetrievalRequest) { r.MaxDepth = 3 }, func(r *domain.RetrievalRequest) { r.EntityIDs = []string{" "} }} {
		v := domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "demo"}}
		change(&v)
		_, e = s.Retrieve(t.Context(), p, v)
		assert.ErrorIs(t, e, domain.ErrInvalidInput)
	}
	_, e = NewRetrievalService(directTx{}, f, f, nil, nil, nil)
	assert.ErrorIs(t, e, domain.ErrInvalidInput)
}
func TestRetrievalApplicability(t *testing.T) {
	s, f, p, r := retrievalSetup(t)
	for i, version := range []string{"1", "2"} {
		id := fmt.Sprint(i)
		f.claims = append(f.claims, domain.ClaimRevision{ClaimID: id, Revision: id, Text: "version", Scope: r.Scope, Status: domain.ClaimVerified, Applicability: domain.Applicability{Versions: map[string]string{"go": version}}})
		f.eligible[id] = true
	}
	r.Applicability.Versions = map[string]string{"go": "2"}
	got, e := s.Retrieve(t.Context(), p, r)
	require.NoError(t, e)
	require.Len(t, got.Chunks, 1)
	assert.Equal(t, "1", got.Chunks[0].Revision)
}

func TestRetrievalFailsClosedOnBoundaryErrors(t *testing.T) {
	for _, failure := range []string{"snapshot", "seeds", "neighbors", "eligible", "task", "final epoch", "source unavailable", "authoritative capability"} {
		t.Run(failure, func(t *testing.T) {
			s, f, p, r := retrievalSetup(t)
			f.fail = failure
			f.claims = []domain.ClaimRevision{{ClaimID: "one", Revision: "one", Text: "safe", Scope: r.Scope, Status: domain.ClaimVerified}}
			f.eligible["one"] = true
			want := domain.ErrUnavailable
			if failure == "task" {
				r.Scope.TaskID = "task"
			}
			if failure == "final epoch" {
				want = domain.ErrStale
			}
			if failure == "source unavailable" {
				f.eligible["one"] = false
				f.unavailable = true
			}
			if failure == "authoritative capability" {
				f.snapshot.Principal.Capabilities = nil
				want = domain.ErrForbidden
			}
			_, e := s.Retrieve(t.Context(), p, r)
			assert.ErrorIs(t, e, want)
		})
	}
}
func TestRetrievalFetchIntegrityAndAuthority(t *testing.T) {
	for _, test := range []struct {
		name string
		want error
	}{{"valid", nil}, {"missing evidence capability", domain.ErrForbidden}, {"authoritative evidence capability", domain.ErrForbidden}, {"missing", domain.ErrForbidden}, {"task", domain.ErrForbidden}, {"locator", domain.ErrForbidden}, {"unavailable", domain.ErrUnavailable}, {"policy", domain.ErrForbidden}, {"no publication", domain.ErrForbidden}, {"corrupt", domain.ErrUnavailable}, {"missing object", domain.ErrUnavailable}, {"tiny", domain.ErrBudget}, {"invalid", domain.ErrInvalidInput}} {
		t.Run(test.name, func(t *testing.T) {
			capture, evidence, objects, p, m := captureFixture(t)
			p.Capabilities = append(p.Capabilities, "retrieval:read")
			m.Scope.TaskID = ""
			m.Scope.SessionID = ""
			a := readyCapture(t, capture, p, m)
			f := &retrievalFixture{snapshot: RetrievalSnapshot{Principal: p}, eligible: map[string]bool{"verified": true}, claims: []domain.ClaimRevision{{ClaimID: "verified", Revision: "verified", Status: domain.ClaimVerified, Scope: m.Scope, Evidence: []domain.EvidenceRef{{ArtifactID: a.ID, Digest: a.Digest, Locator: a.SourceLocator}}}}}
			counted := &countRetrievalObjects{ObjectStore: objects}
			s, e := NewRetrievalService(directTx{}, f, nil, evidence, counted, []byte(strings.Repeat("k", 32)))
			require.NoError(t, e)
			id, locator, budget := a.ID, a.SourceLocator, 16384
			switch test.name {
			case "missing evidence capability":
				p.Capabilities = []string{"retrieval:read"}
			case "authoritative evidence capability":
				f.snapshot.Principal.Capabilities = []string{"retrieval:read"}
			case "missing":
				id = "missing"
			case "task":
				c := evidence.artifacts[a.ID]
				c.Scope.TaskID = "task"
				evidence.artifacts[a.ID] = c
			case "locator":
				locator = "wrong"
			case "unavailable":
				c := evidence.artifacts[a.ID]
				c.ObjectAvailable = false
				evidence.artifacts[a.ID] = c
			case "policy":
				evidence.policy.Paused = true
			case "no publication":
				f.eligible["verified"] = false
			case "corrupt":
				objects.data[a.ObjectKey] = []byte("corrupt")
			case "missing object":
				delete(objects.data, a.ObjectKey)
			case "tiny":
				budget = 1
			case "invalid":
				locator = ""
			}
			got, e := s.Fetch(t.Context(), p, id, locator, budget)
			if test.name == "missing evidence capability" || test.name == "authoritative evidence capability" {
				assert.Zero(t, counted.opens)
			}
			if test.want != nil {
				assert.ErrorIs(t, e, test.want)
				assert.Empty(t, got.Chunks)
				if test.name == "corrupt" || test.name == "missing object" {
					assert.False(t, evidence.artifacts[a.ID].ObjectAvailable)
				}
				return
			}
			require.NoError(t, e)
			require.Len(t, got.Chunks, 1)
			assert.Equal(t, "exit_code=0", got.Chunks[0].Text)
			assert.Equal(t, domain.ClaimCandidate, got.Chunks[0].ClaimStatus)
			assert.Nil(t, got.Chunks[0].VerificationOutcome)
		})
	}
}
func TestRetrievalFrontierTruncationExplainsNextAction(t *testing.T) {
	s, f, p, r := retrievalSetup(t)
	r.MaxVisited = 1
	f.claims = []domain.ClaimRevision{{ClaimID: "one", Revision: "one", Text: "safe", Scope: r.Scope, Status: domain.ClaimVerified}}
	f.eligible["one"] = true
	v, e := s.Retrieve(t.Context(), p, r)
	require.NoError(t, e)
	assert.True(t, v.Truncated)
	assert.Empty(t, v.Continuation)
	wire, e := domain.RetrievalWire(v)
	require.NoError(t, e)
	assert.Contains(t, string(wire), `"truncation_reason":"visited_limit"`)
	assert.Contains(t, string(wire), `"action":`)
}

type countRetrievalObjects struct {
	ObjectStore
	opens int
}

func (o *countRetrievalObjects) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	o.opens++
	return o.ObjectStore.Open(ctx, key)
}

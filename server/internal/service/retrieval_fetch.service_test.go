package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// largeSource is a synthetic >16 KiB UTF-8 source with multi-byte runes and
// JSON-escaped characters on every line.
func largeSource() []byte {
	var b strings.Builder
	for i := 0; b.Len() < 40<<10; i++ {
		fmt.Fprintf(&b, "line %04d <猫> \"quoted\" é\\ done\n", i)
	}
	return []byte(b.String())
}

type fetchFixture struct {
	service  *RetrievalService
	repo     *retrievalFixture
	evidence *captureRepo
	objects  *countRetrievalObjects
	raw      *captureObjects
	p        domain.Principal
	artifact domain.Artifact
	data     []byte
}

// newFetchFixture stores content as a ready artifact. Task-scoped artifacts are
// cited by an owned candidate; project artifacts by a verified publication.
func newFetchFixture(t *testing.T, content []byte, task bool) *fetchFixture {
	t.Helper()
	capture, evidence, objects, p, m := captureFixture(t)
	p.Capabilities = append(p.Capabilities, "retrieval:read")
	sum := sha256.Sum256(content)
	m.Digest, m.Bytes = hex.EncodeToString(sum[:]), int64(len(content))
	if !task {
		m.Scope.TaskID, m.Scope.SessionID = "", ""
	}
	a, e := capture.Stage(t.Context(), p, m, bytes.NewReader(content))
	require.NoError(t, e)
	_, e = capture.Complete(t.Context(), p, a.ID, a.Digest)
	require.NoError(t, e)
	a = evidence.artifacts[a.ID]
	ref := domain.EvidenceRef{ArtifactID: a.ID, Digest: a.Digest, Locator: a.SourceLocator, SourceRevision: a.Generation}
	claim := domain.ClaimRevision{ClaimID: "verified", Revision: "verified", Status: domain.ClaimVerified, Scope: a.Scope, Evidence: []domain.EvidenceRef{ref}}
	snapshot := RetrievalSnapshot{Principal: p}
	if task {
		claim = domain.ClaimRevision{ClaimID: "finding", Revision: "finding-r1", Status: domain.ClaimCandidate, SubmittedBy: p.ClientID, Scope: a.Scope, Evidence: []domain.EvidenceRef{ref}}
		snapshot.TaskOwner = p.ClientID
	}
	f := &retrievalFixture{snapshot: snapshot, eligible: map[string]bool{claim.Revision: true}, claims: []domain.ClaimRevision{claim}}
	counted := &countRetrievalObjects{ObjectStore: objects}
	s, e := NewRetrievalService(directTx{}, f, nil, evidence, counted, []byte(strings.Repeat("k", 32)))
	require.NoError(t, e)
	return &fetchFixture{service: s, repo: f, evidence: evidence, objects: counted, raw: objects, p: p, artifact: a, data: content}
}
func (f *fetchFixture) publication(budget int) domain.FetchRequest {
	return domain.FetchRequest{ArtifactID: f.artifact.ID, Locator: f.artifact.SourceLocator, Mode: domain.FetchPublication, BudgetBytes: budget}
}
func (f *fetchFixture) working(budget int) domain.FetchRequest {
	return domain.FetchRequest{ArtifactID: f.artifact.ID, Locator: f.artifact.SourceLocator, Mode: domain.FetchWorking, Scope: domain.Scope{ProjectID: f.artifact.Scope.ProjectID, TaskID: f.artifact.Scope.TaskID}, BudgetBytes: budget}
}
func wireLen(t *testing.T, r domain.RetrievalResult) int {
	t.Helper()
	wire, e := domain.RetrievalWire(r)
	require.NoError(t, e)
	return len(wire) + 1
}

func TestFetchLargeStoredSourceReturnsBoundedPassage(t *testing.T) {
	f := newFetchFixture(t, largeSource(), false)
	require.Greater(t, len(f.data), 16384, "precondition: stored source exceeds the evidence budget")

	got, e := f.service.FetchEvidence(t.Context(), f.p, f.publication(16384))

	require.NoError(t, e)
	require.Len(t, got.Chunks, 1)
	c := got.Chunks[0]
	require.NotNil(t, c.Passage)
	assert.Equal(t, int64(0), c.Passage.StartByte)
	assert.Equal(t, int64(len(f.data)), c.Passage.SourceBytes)
	assert.Less(t, c.Passage.EndByte, c.Passage.SourceBytes)
	assert.Equal(t, string(f.data[c.Passage.StartByte:c.Passage.EndByte]), c.Text)
	assert.True(t, utf8.ValidString(c.Text))
	assert.True(t, got.Truncated)
	assert.Equal(t, "wire_budget", got.TruncationReason)
	assert.NotEmpty(t, got.Continuation)
	assert.Contains(t, c.Warnings, "partial_source_passage")
	assert.Contains(t, c.Warnings, "source_evidence_is_not_a_verified_claim")
	assert.Equal(t, f.artifact.Digest, c.Evidence[0].Digest, "immutable artifact digest, not an excerpt digest")
	assert.Equal(t, f.artifact.SourceLocator, c.Evidence[0].Locator, "base locator preserved")
	assert.LessOrEqual(t, wireLen(t, got), 16384)
	assert.Greater(t, wireLen(t, got), 16384-256, "passage fills the budget instead of returning a token excerpt")
}

func TestFetchContinuationCoversWholeSourceExactlyOnce(t *testing.T) {
	for _, budget := range []int{16384, 1500} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			f := newFetchFixture(t, largeSource(), false)
			request := f.publication(budget)
			var assembled []byte
			next := int64(0)
			pages := 0

			for {
				got, e := f.service.FetchEvidence(t.Context(), f.p, request)
				require.NoError(t, e)
				pages++
				require.Less(t, pages, 1000, "precondition: pagination terminates")
				c := got.Chunks[0]
				require.Equal(t, next, c.Passage.StartByte, "no gap or overlap")
				assert.LessOrEqual(t, wireLen(t, got), budget)
				assembled = append(assembled, c.Text...)
				next = c.Passage.EndByte
				if got.Continuation == "" {
					assert.False(t, got.Truncated)
					break
				}
				request.Continuation = got.Continuation
			}

			assert.Equal(t, f.data, assembled)
			assert.Greater(t, pages, 1)
			t.Logf("pages=%d bytes=%d", pages, len(assembled))
		})
	}
}

func TestFetchSpanSelectionHonorsUTF8Boundaries(t *testing.T) {
	data := largeSource()
	cat := int64(bytes.Index(data, []byte("猫")))
	require.Positive(t, cat)
	for _, tc := range []struct {
		name               string
		start              int64
		end                *int64
		wantStart, wantEnd int64
	}{
		{"start inside rune moves forward", cat + 1, ptr(cat + 20), cat + 3, cat + 20},
		{"end inside rune moves back", cat - 4, ptr(cat + 2), cat - 4, cat},
		{"exact rune boundaries kept", cat, ptr(cat + 3), cat, cat + 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFetchFixture(t, data, false)
			request := f.publication(16384)
			request.StartByte, request.EndByte = tc.start, tc.end

			got, e := f.service.FetchEvidence(t.Context(), f.p, request)

			require.NoError(t, e)
			c := got.Chunks[0]
			assert.Equal(t, tc.wantStart, c.Passage.StartByte)
			assert.Equal(t, tc.wantEnd, c.Passage.EndByte)
			assert.Equal(t, string(data[tc.wantStart:tc.wantEnd]), c.Text)
			assert.False(t, got.Truncated)
			assert.Empty(t, got.Continuation)
			assert.Contains(t, c.Warnings, "partial_source_passage")
		})
	}
}
func ptr[T any](v T) *T { return &v }

func TestFetchArtifactEdgeWithSplitRuneExcludesPartialBytes(t *testing.T) {
	// F4 chunks filtered JSON at byte boundaries, so a stored chunk may end inside a rune.
	content := append([]byte("synthetic <猫"), []byte("猫")[:2]...)
	f := newFetchFixture(t, content, false)

	got, e := f.service.FetchEvidence(t.Context(), f.p, f.publication(16384))

	require.NoError(t, e)
	c := got.Chunks[0]
	assert.Equal(t, "synthetic <猫", c.Text)
	assert.Equal(t, int64(len(content)-2), c.Passage.EndByte)
	assert.Equal(t, int64(len(content)), c.Passage.SourceBytes)
	assert.Contains(t, c.Warnings, "partial_source_passage", "excerpt is not the whole source")
}

func TestFetchSmallSourceIsWholeWithExactBoundaries(t *testing.T) {
	f := newFetchFixture(t, []byte("exit_code=0"), false)

	got, e := f.service.FetchEvidence(t.Context(), f.p, f.publication(16384))

	require.NoError(t, e)
	c := got.Chunks[0]
	assert.Equal(t, "exit_code=0", c.Text)
	assert.Equal(t, domain.Passage{StartByte: 0, EndByte: 11, SourceBytes: 11}, *c.Passage)
	assert.False(t, got.Truncated)
	assert.NotContains(t, c.Warnings, "partial_source_passage")
}

func TestFetchPassageContinuationIsAuthenticatedAndCurrent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*fetchFixture, *domain.FetchRequest)
		want   error
	}{
		{"knowledge epoch changed", func(f *fetchFixture, _ *domain.FetchRequest) { f.repo.snapshot.KnowledgeEpoch++ }, domain.ErrStale},
		{"credential epoch changed", func(f *fetchFixture, _ *domain.FetchRequest) { f.repo.snapshot.AuthEpoch++ }, domain.ErrStale},
		{"deletion epoch changed", func(f *fetchFixture, _ *domain.FetchRequest) { f.repo.snapshot.DeletionEpoch++ }, domain.ErrStale},
		{"budget changed", func(_ *fetchFixture, r *domain.FetchRequest) { r.BudgetBytes-- }, domain.ErrStale},
		{"tampered cursor", func(_ *fetchFixture, r *domain.FetchRequest) { r.Continuation = "x" + r.Continuation[1:] }, domain.ErrStale},
		{"different client", func(f *fetchFixture, _ *domain.FetchRequest) { f.p.ClientID = "other" }, domain.ErrStale},
		{"artifact invalidated", func(f *fetchFixture, _ *domain.FetchRequest) { f.repo.eligible["verified"] = false }, domain.ErrForbidden},
		{"source unavailable", func(f *fetchFixture, _ *domain.FetchRequest) {
			a := f.evidence.artifacts[f.artifact.ID]
			a.ObjectAvailable = false
			f.evidence.artifacts[f.artifact.ID] = a
		}, domain.ErrUnavailable},
		{"credential revoked", func(f *fetchFixture, _ *domain.FetchRequest) { f.repo.fail = "snapshot" }, domain.ErrUnavailable},
		{"generation changed", func(f *fetchFixture, _ *domain.FetchRequest) {
			a := f.evidence.artifacts[f.artifact.ID]
			a.Generation = "new-generation"
			f.evidence.artifacts[a.ID] = a
		}, domain.ErrStale},
		{"generation missing", func(f *fetchFixture, _ *domain.FetchRequest) {
			a := f.evidence.artifacts[f.artifact.ID]
			a.Generation = ""
			f.evidence.artifacts[a.ID] = a
		}, domain.ErrUnavailable},
		{"start with continuation", func(_ *fetchFixture, r *domain.FetchRequest) { r.StartByte = 5 }, domain.ErrInvalidInput},
		{"end changed", func(_ *fetchFixture, r *domain.FetchRequest) { r.EndByte = ptr(int64(30000)) }, domain.ErrStale},
		{"cursor offset altered", func(f *fetchFixture, r *domain.FetchRequest) {
			data, err := f.service.verified(r.Continuation)
			if err != nil {
				panic(err)
			}
			var cursor passageCursor
			if err := json.Unmarshal(data, &cursor); err != nil {
				panic(err)
			}
			cursor.Offset = cursor.Start
			r.Continuation = f.service.sign(retrievalCursor{}, cursor)
		}, domain.ErrStale},
		{"cursor start altered", func(f *fetchFixture, r *domain.FetchRequest) {
			data, err := f.service.verified(r.Continuation)
			if err != nil {
				panic(err)
			}
			var cursor passageCursor
			if err := json.Unmarshal(data, &cursor); err != nil {
				panic(err)
			}
			cursor.Start++
			r.Continuation = f.service.sign(retrievalCursor{}, cursor)
		}, domain.ErrStale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFetchFixture(t, largeSource(), false)
			first, e := f.service.FetchEvidence(t.Context(), f.p, f.publication(16384))
			require.NoError(t, e)
			require.NotEmpty(t, first.Continuation)
			request := f.publication(16384)
			request.Continuation = first.Continuation
			tc.mutate(f, &request)
			opens := f.objects.opens

			got, e := f.service.FetchEvidence(t.Context(), f.p, request)

			assert.ErrorIs(t, e, tc.want)
			assert.Empty(t, got.Chunks)
			assert.Equal(t, opens, f.objects.opens, "denied before object I/O")
		})
	}
}

func TestFetchPassageRequestValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*domain.FetchRequest)
		want   error
	}{
		{"start beyond source", func(r *domain.FetchRequest) { r.StartByte = 1 << 30 }, domain.ErrInvalidInput},
		{"negative start", func(r *domain.FetchRequest) { r.StartByte = -1 }, domain.ErrInvalidInput},
		{"end before start", func(r *domain.FetchRequest) { r.StartByte, r.EndByte = 10, ptr(int64(5)) }, domain.ErrInvalidInput},
		{"scope in publication mode", func(r *domain.FetchRequest) { r.Scope = domain.Scope{ProjectID: "project", TaskID: "task"} }, domain.ErrInvalidInput},
		{"unknown mode", func(r *domain.FetchRequest) { r.Mode = "owner" }, domain.ErrInvalidInput},
		{"budget above evidence limit", func(r *domain.FetchRequest) { r.BudgetBytes = 16385 }, domain.ErrInvalidInput},
		{"tiny budget", func(r *domain.FetchRequest) { r.BudgetBytes = 64 }, domain.ErrBudget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFetchFixture(t, largeSource(), false)
			request := f.publication(16384)
			tc.mutate(&request)

			got, e := f.service.FetchEvidence(t.Context(), f.p, request)

			assert.ErrorIs(t, e, tc.want)
			assert.Empty(t, got.Chunks)
			if tc.name != "start beyond source" && tc.name != "end before start" {
				assert.Zero(t, f.objects.opens, "rejected before object I/O")
			}
		})
	}
}

func TestWorkingFetchReturnsOwnedTaskEvidenceUnverified(t *testing.T) {
	f := newFetchFixture(t, largeSource(), true)

	got, e := f.service.FetchEvidence(t.Context(), f.p, f.working(16384))

	require.NoError(t, e)
	require.Len(t, got.Chunks, 1)
	c := got.Chunks[0]
	assert.Equal(t, domain.ClaimCandidate, c.ClaimStatus)
	assert.Equal(t, domain.TrustSubmitted, c.OriginTrust)
	assert.Nil(t, c.VerificationOutcome)
	assert.Contains(t, c.Warnings, "unverified_task_evidence")
	assert.Contains(t, c.Warnings, "source_evidence_is_not_a_verified_claim")
	assert.Equal(t, string(f.data[:c.Passage.EndByte]), c.Text)
	assert.True(t, got.Truncated)
	next := f.working(16384)
	next.Continuation = got.Continuation
	page, e := f.service.FetchEvidence(t.Context(), f.p, next)
	require.NoError(t, e)
	assert.Equal(t, c.Passage.EndByte, page.Chunks[0].Passage.StartByte)
}

func TestWorkingFetchDeniesBeforeObjectRead(t *testing.T) {
	for _, tc := range []struct {
		name   string
		task   bool
		mutate func(*fetchFixture, *domain.FetchRequest)
		want   error
	}{
		{"id-only fetch of task artifact", true, func(f *fetchFixture, r *domain.FetchRequest) { *r = f.publication(16384) }, domain.ErrForbidden},
		{"another task", true, func(_ *fetchFixture, r *domain.FetchRequest) { r.Scope.TaskID = "other-task" }, domain.ErrForbidden},
		{"another project", true, func(_ *fetchFixture, r *domain.FetchRequest) { r.Scope.ProjectID = "other" }, domain.ErrForbidden},
		{"another session", true, func(_ *fetchFixture, r *domain.FetchRequest) { r.Scope.SessionID = "other-session" }, domain.ErrForbidden},
		{"candidate submitted by another client", true, func(f *fetchFixture, _ *domain.FetchRequest) { f.repo.claims[0].SubmittedBy = "other-agent" }, domain.ErrForbidden},
		{"collector artifact without owned citation", true, func(f *fetchFixture, _ *domain.FetchRequest) { f.repo.claims = nil }, domain.ErrForbidden},
		{"citation no longer eligible", true, func(f *fetchFixture, _ *domain.FetchRequest) { f.repo.eligible = map[string]bool{} }, domain.ErrForbidden},
		{"task owned by another client", true, func(f *fetchFixture, _ *domain.FetchRequest) { f.repo.snapshot.TaskOwner = "other-agent" }, domain.ErrForbidden},
		{"citation with another locator", true, func(f *fetchFixture, _ *domain.FetchRequest) { f.repo.claims[0].Evidence[0].Locator = "other" }, domain.ErrForbidden},
		{"citation with another session", true, func(f *fetchFixture, _ *domain.FetchRequest) { f.repo.claims[0].Scope.SessionID = "other" }, domain.ErrForbidden},
		{"publication with another project", false, func(f *fetchFixture, r *domain.FetchRequest) {
			*r = f.publication(16384)
			f.repo.claims[0].Scope.ProjectID = "other"
		}, domain.ErrForbidden},
		{"missing evidence capability", true, func(f *fetchFixture, _ *domain.FetchRequest) { f.p.Capabilities = []string{"retrieval:read"} }, domain.ErrForbidden},
		{"authoritative retrieval capability withdrawn", true, func(f *fetchFixture, _ *domain.FetchRequest) {
			f.repo.snapshot.Principal.Capabilities = []string{"evidence:read"}
		}, domain.ErrForbidden},
		{"project artifact in working mode", false, func(f *fetchFixture, r *domain.FetchRequest) {
			*r = f.working(16384)
			r.Scope = domain.Scope{ProjectID: f.artifact.Scope.ProjectID, TaskID: "task"}
		}, domain.ErrForbidden},
		{"working mode without task", true, func(_ *fetchFixture, r *domain.FetchRequest) { r.Scope.TaskID = "" }, domain.ErrInvalidInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFetchFixture(t, largeSource(), tc.task)
			request := f.working(16384)
			tc.mutate(f, &request)

			got, e := f.service.FetchEvidence(t.Context(), f.p, request)

			assert.ErrorIs(t, e, tc.want)
			assert.Empty(t, got.Chunks)
			assert.Zero(t, f.objects.opens, "denied before any object read")
		})
	}
}

type changingObjects struct {
	ObjectStore
	during func()
}

func (o changingObjects) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	o.during()
	return o.ObjectStore.Open(ctx, key)
}

func TestWorkingFetchRechecksAuthorityAfterObjectRead(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*fetchFixture)
		want   error
	}{
		{"task head changed", func(f *fetchFixture) { f.repo.snapshot.TaskHead = "new-head" }, domain.ErrStale},
		{"credential revoked", func(f *fetchFixture) { f.repo.fail = "snapshot" }, domain.ErrUnavailable},
		{"citation invalidated", func(f *fetchFixture) { f.repo.eligible = map[string]bool{} }, domain.ErrForbidden},
		{"artifact generation changed", func(f *fetchFixture) {
			a := f.evidence.artifacts[f.artifact.ID]
			a.Generation = "new-generation"
			f.evidence.artifacts[a.ID] = a
		}, domain.ErrStale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFetchFixture(t, largeSource(), true)
			s, e := NewRetrievalService(directTx{}, f.repo, nil, f.evidence, changingObjects{ObjectStore: f.raw, during: func() { tc.change(f) }}, []byte(strings.Repeat("k", 32)))
			require.NoError(t, e)

			got, e := s.FetchEvidence(t.Context(), f.p, f.working(16384))

			assert.ErrorIs(t, e, tc.want)
			assert.Empty(t, got.Chunks)
		})
	}
}

func TestStatusReportsAuthoritativeOwnGrants(t *testing.T) {
	request := domain.Principal{ClientID: "agent", ProjectIDs: []string{"demo", "stale-grant"}, Capabilities: []string{"status:read", "retrieval:read", "clients:manage"}}
	current := domain.Principal{ClientID: "agent", ProjectIDs: []string{"demo"}, Capabilities: []string{"status:read", "retrieval:read"}}
	for _, tc := range []struct {
		name      string
		principal domain.Principal
		fixture   retrievalFixture
		want      error
		calls     int
	}{
		{"current grants", request, retrievalFixture{snapshot: RetrievalSnapshot{Principal: current}}, nil, 1},
		{"revoked", request, retrievalFixture{fail: "snapshot"}, domain.ErrUnavailable, 1},
		{"status capability withdrawn", request, retrievalFixture{snapshot: RetrievalSnapshot{Principal: domain.Principal{ClientID: "agent", ProjectIDs: []string{"demo"}}}}, domain.ErrForbidden, 1},
		{"no status capability", domain.Principal{ClientID: "agent", ProjectIDs: []string{"demo"}}, retrievalFixture{}, domain.ErrForbidden, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := tc.fixture
			s := NewStatusService(directTx{}, &fixture, "retrieval_unavailable")

			got, e := s.Status(t.Context(), tc.principal)

			assert.ErrorIs(t, e, tc.want)
			assert.Equal(t, tc.calls, fixture.snapshotCalls)
			if tc.want != nil {
				assert.Zero(t, got)
				return
			}
			assert.Equal(t, "available", got.ConnectionState)
			assert.Equal(t, []string{"demo"}, got.AuthorizedProjectIDs)
			assert.Equal(t, []string{"status:read", "retrieval:read"}, got.AuthorizedCapabilities)
			assert.Equal(t, []string{"recovery_unavailable", "retrieval_unavailable"}, got.CapabilityGaps)
			assert.Equal(t, domain.MaxRevisionBytes, got.MaxRevisionBytes)
		})
	}
}

func TestFetchNonzeroStartContinuationPreservesSelectedRange(t *testing.T) {
	for _, task := range []bool{false, true} {
		t.Run(fmt.Sprintf("working=%t", task), func(t *testing.T) {
			f := newFetchFixture(t, largeSource(), task)
			request := f.publication(1500)
			if task {
				request = f.working(1500)
			}
			// Both endpoints are inside a rune: the first page advances and the last
			// page retreats to the nearest valid UTF-8 boundaries.
			firstCat := int64(bytes.Index(f.data, []byte("猫")))
			lastCat := int64(bytes.LastIndex(f.data, []byte("猫")))
			request.StartByte, request.EndByte = firstCat+1, ptr(lastCat+2)
			wantStart, wantEnd := firstCat+3, lastCat
			var assembled []byte
			next := wantStart
			for page := 0; ; page++ {
				require.Less(t, page, 1000)
				result, err := f.service.FetchEvidence(t.Context(), f.p, request)
				require.NoError(t, err)
				require.Len(t, result.Chunks, 1)
				chunk := result.Chunks[0]
				require.Equal(t, next, chunk.Passage.StartByte)
				assert.True(t, utf8.ValidString(chunk.Text))
				assert.LessOrEqual(t, wireLen(t, result), request.BudgetBytes)
				assert.Equal(t, f.artifact.Digest, chunk.Evidence[0].Digest)
				assert.Equal(t, f.artifact.SourceLocator, chunk.Evidence[0].Locator)
				assembled = append(assembled, chunk.Text...)
				next = chunk.Passage.EndByte
				if result.Continuation == "" {
					break
				}
				require.Contains(t, result.Action, "omit start_byte")
				request.StartByte = 0 // continuation carries the original start
				request.Continuation = result.Continuation
			}
			assert.Equal(t, wantEnd, next)
			assert.Equal(t, f.data[wantStart:wantEnd], assembled)
		})
	}
}

func TestFetchCitationBoundDistinguishesNoAuthorityFromIncompleteScan(t *testing.T) {
	for _, task := range []bool{false, true} {
		for _, count := range []int{49, 50, 51} {
			t.Run(fmt.Sprintf("working=%t/citations=%d", task, count), func(t *testing.T) {
				f := newFetchFixture(t, []byte("synthetic content"), task)
				candidate := f.repo.claims[0]
				f.repo.claims = nil
				f.repo.eligible = map[string]bool{}
				for i := range count {
					c := candidate
					c.Revision = fmt.Sprintf("citation-%02d", i)
					f.repo.claims = append(f.repo.claims, c)
				}
				request := f.publication(16384)
				if task {
					request = f.working(16384)
				}
				got, err := f.service.FetchEvidence(t.Context(), f.p, request)
				if count < 50 {
					assert.ErrorIs(t, err, domain.ErrForbidden)
				} else {
					assert.ErrorIs(t, err, domain.ErrIncomplete)
				}
				assert.Empty(t, got.Chunks)
				assert.Zero(t, f.objects.opens)
			})
		}
	}
}

func TestFetchCitationIsReauthorizedAcrossBoundBeforeAndAfterRead(t *testing.T) {
	for _, task := range []bool{false, true} {
		f := newFetchFixture(t, []byte("synthetic content"), task)
		candidate := f.repo.claims[0]
		for i := range 49 {
			c := candidate
			c.Revision = fmt.Sprintf("other-%02d", i)
			f.repo.claims = append(f.repo.claims, c)
		}
		request := f.publication(16384)
		if task {
			request = f.working(16384)
		}
		result, err := f.service.FetchEvidence(t.Context(), f.p, request)
		require.NoError(t, err)
		assert.Len(t, result.Chunks, 1)

		// Eligibility can disappear after the initial authorization; do not
		// release the already-read bytes or misreport a definitive denial.
		changing, err := NewRetrievalService(directTx{}, f.repo, nil, f.evidence, changingObjects{ObjectStore: f.raw, during: func() { f.repo.eligible = map[string]bool{} }}, []byte(strings.Repeat("k", 32)))
		require.NoError(t, err)
		result, err = changing.FetchEvidence(t.Context(), f.p, request)
		assert.ErrorIs(t, err, domain.ErrIncomplete)
		assert.Empty(t, result.Chunks)
	}
}

// SourceRevision identifies the source revision, not the private storage
// generation (which the artifact API does not expose to citation writers).
func TestFetchAcceptsOpaqueSourceRevision(t *testing.T) {
	for _, task := range []bool{false, true} {
		t.Run(fmt.Sprintf("working=%t", task), func(t *testing.T) {
			f := newFetchFixture(t, []byte("synthetic content"), task)
			f.repo.claims[0].Evidence[0].SourceRevision = "repository-revision"
			request := f.publication(16384)
			if task {
				request = f.working(16384)
			}
			got, err := f.service.FetchEvidence(t.Context(), f.p, request)
			require.NoError(t, err)
			require.Len(t, got.Chunks, 1)
			assert.Equal(t, "synthetic content", got.Chunks[0].Text)
		})
	}
}

func TestFetchCitationTimeApplicability(t *testing.T) {
	for _, task := range []bool{false, true} {
		for _, target := range []string{"claim", "reference"} {
			for _, timing := range []string{"expired", "future", "expires during read"} {
				t.Run(fmt.Sprintf("working=%t/%s/%s", task, target, timing), func(t *testing.T) {
					f := newFetchFixture(t, []byte("private evidence"), task)
					set := func(a domain.Applicability) {
						if target == "claim" {
							f.repo.claims[0].Applicability = a
						} else {
							f.repo.claims[0].Evidence[0].Applicability = a
						}
					}
					past, future := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
					switch timing {
					case "expired":
						set(domain.Applicability{ValidUntil: &past})
					case "future":
						set(domain.Applicability{ValidFrom: &future})
					}
					s := f.service
					if timing == "expires during read" {
						var err error
						s, err = NewRetrievalService(directTx{}, f.repo, nil, f.evidence, changingObjects{ObjectStore: f.raw, during: func() { set(domain.Applicability{ValidUntil: &past}) }}, []byte(strings.Repeat("k", 32)))
						require.NoError(t, err)
					}
					r := f.publication(16384)
					if task {
						r = f.working(16384)
					}
					got, err := s.FetchEvidence(t.Context(), f.p, r)
					assert.ErrorIs(t, err, domain.ErrForbidden)
					assert.Empty(t, got.Chunks)
					if timing != "expires during read" {
						assert.Zero(t, f.objects.opens)
					}
				})
			}
		}
	}
}

func TestFetchReturnsEligibleCitationSourceRevision(t *testing.T) {
	for _, task := range []bool{false, true} {
		t.Run(fmt.Sprint(task), func(t *testing.T) {
			f := newFetchFixture(t, []byte("content"), task)
			f.repo.claims[0].Evidence[0].SourceRevision = "actual-source-revision"
			r := f.publication(16384)
			if task {
				r = f.working(16384)
			}
			got, err := f.service.FetchEvidence(t.Context(), f.p, r)
			require.NoError(t, err)
			require.Len(t, got.Chunks, 1)
			assert.Equal(t, "actual-source-revision", got.Chunks[0].Evidence[0].SourceRevision)
		})
	}
}

func TestFetchSelectsOnlyApplicableCitationAndFencesRevision(t *testing.T) {
	for _, task := range []bool{false, true} {
		t.Run(fmt.Sprint(task), func(t *testing.T) {
			f := newFetchFixture(t, largeSource(), task)
			invalid := f.repo.claims[0]
			invalid.Revision = "a-ineligible"
			invalid.Evidence = append([]domain.EvidenceRef(nil), invalid.Evidence...)
			invalid.Evidence[0].SourceRevision = "a-expired"
			past := time.Now().Add(-time.Hour)
			invalid.Applicability.ValidUntil = &past
			f.repo.claims[0].Evidence[0].SourceRevision = "z-valid"
			f.repo.claims = append([]domain.ClaimRevision{invalid}, f.repo.claims...)
			f.repo.eligible[invalid.Revision] = true
			r := f.publication(1500)
			if task {
				r = f.working(1500)
			}
			first, err := f.service.FetchEvidence(t.Context(), f.p, r)
			require.NoError(t, err)
			require.NotEmpty(t, first.Continuation)
			assert.Equal(t, "z-valid", first.Chunks[0].Evidence[0].SourceRevision)
			r.Continuation = first.Continuation
			f.repo.claims[1].Evidence[0].SourceRevision = "changed"
			opens := f.objects.opens
			result, err := f.service.FetchEvidence(t.Context(), f.p, r)
			assert.ErrorIs(t, err, domain.ErrStale)
			assert.Empty(t, result.Chunks)
			assert.Equal(t, opens, f.objects.opens)
		})
	}
}

func TestFetchPreservesSelectedCitationApplicability(t *testing.T) {
	for _, task := range []bool{false, true} {
		t.Run(fmt.Sprintf("working=%t", task), func(t *testing.T) {
			f := newFetchFixture(t, largeSource(), task)
			validFrom := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
			validUntil := validFrom.Add(2 * time.Hour)
			want := domain.Applicability{RepositoryRevision: "repository-r1", DirtyDigest: "dirty-r1", Environment: "production", Versions: map[string]string{"go": "1.27", "db": "2"}, ValidFrom: &validFrom, ValidUntil: &validUntil}
			first := f.repo.claims[0]
			first.Revision = "a-claim"
			first.Evidence = append([]domain.EvidenceRef(nil), first.Evidence...)
			first.Evidence[0].SourceRevision = "same-source"
			first.Evidence[0].Applicability = want
			second := first
			second.Revision = "z-claim"
			second.Evidence = append([]domain.EvidenceRef(nil), first.Evidence...)
			second.Evidence[0].Applicability = domain.Applicability{Environment: "other"}
			f.repo.claims = []domain.ClaimRevision{second, first}
			f.repo.eligible = map[string]bool{first.Revision: true, second.Revision: true}
			// Full reference conditions add ~0.6 KiB of fixed wire overhead.
			r := f.publication(2048)
			if task {
				r = f.working(2048)
			}

			result, err := f.service.FetchEvidence(t.Context(), f.p, r)
			require.NoError(t, err)
			require.NotEmpty(t, result.Continuation)
			assert.Equal(t, want, result.Chunks[0].Evidence[0].Applicability)
			wire, err := domain.RetrievalWire(result)
			require.NoError(t, err)
			assert.Contains(t, string(wire), `"environment":"production"`)
			assert.Contains(t, string(wire), `"repository_revision":"repository-r1"`)
			assert.LessOrEqual(t, len(wire)+1, r.BudgetBytes)

			// Repository result ordering must not change which reference authorizes
			// the next page, even when two references share a source revision.
			f.repo.claims[0], f.repo.claims[1] = f.repo.claims[1], f.repo.claims[0]
			r.Continuation = result.Continuation
			next, err := f.service.FetchEvidence(t.Context(), f.p, r)
			require.NoError(t, err)
			assert.Equal(t, want, next.Chunks[0].Evidence[0].Applicability)
			assert.LessOrEqual(t, wireLen(t, next), r.BudgetBytes)
		})
	}
}

func TestFetchFencesSelectedCitationAndConditions(t *testing.T) {
	for _, task := range []bool{false, true} {
		for _, when := range []string{"during read", "continuation"} {
			for _, tc := range []struct {
				name   string
				change func(*fetchFixture)
			}{
				{"reference environment", func(f *fetchFixture) { f.repo.claims[0].Evidence[0].Applicability.Environment = "changed" }},
				{"reference version", func(f *fetchFixture) { f.repo.claims[0].Evidence[0].Applicability.Versions["go"] = "changed" }},
				{"reference validity", func(f *fetchFixture) {
					future := time.Now().UTC().Add(2 * time.Hour)
					f.repo.claims[0].Evidence[0].Applicability.ValidUntil = &future
				}},
				{"claim conditions", func(f *fetchFixture) { f.repo.claims[0].Applicability.Environment = "changed" }},
				{"different eligible claim", func(f *fetchFixture) { f.repo.eligible["a-claim"] = false }},
			} {
				t.Run(fmt.Sprintf("working=%t/%s/%s", task, when, tc.name), func(t *testing.T) {
					f := newFetchFixture(t, largeSource(), task)
					first := f.repo.claims[0]
					first.Revision = "a-claim"
					first.Evidence = append([]domain.EvidenceRef(nil), first.Evidence...)
					first.Evidence[0].SourceRevision = "same-source"
					first.Evidence[0].Applicability = domain.Applicability{Environment: "prod", Versions: map[string]string{"go": "1.27"}}
					first.Applicability = domain.Applicability{Environment: "prod"}
					second := first
					second.Revision = "z-claim"
					second.Evidence = append([]domain.EvidenceRef(nil), first.Evidence...)
					second.Evidence[0].Applicability = domain.Applicability{Environment: "fallback"}
					f.repo.claims = []domain.ClaimRevision{first, second}
					f.repo.eligible = map[string]bool{first.Revision: true, second.Revision: true}
					r := f.publication(1500)
					if task {
						r = f.working(1500)
					}
					s := f.service
					if when == "continuation" {
						firstPage, err := s.FetchEvidence(t.Context(), f.p, r)
						require.NoError(t, err)
						require.NotEmpty(t, firstPage.Continuation)
						r.Continuation = firstPage.Continuation
						tc.change(f)
					} else {
						var err error
						s, err = NewRetrievalService(directTx{}, f.repo, nil, f.evidence, changingObjects{ObjectStore: f.raw, during: func() { tc.change(f) }}, []byte(strings.Repeat("k", 32)))
						require.NoError(t, err)
					}
					opens := f.objects.opens
					result, err := s.FetchEvidence(t.Context(), f.p, r)
					assert.ErrorIs(t, err, domain.ErrStale)
					assert.Empty(t, result.Chunks)
					if when == "continuation" {
						assert.Equal(t, opens, f.objects.opens, "reject stale cursor before object I/O")
					}
				})
			}
		}
	}
}

func TestFetchCitationRevisionChangesDuringRead(t *testing.T) {
	f := newFetchFixture(t, []byte("private"), false)
	s, err := NewRetrievalService(directTx{}, f.repo, nil, f.evidence, changingObjects{ObjectStore: f.raw, during: func() { f.repo.claims[0].Evidence[0].SourceRevision = "different" }}, []byte(strings.Repeat("k", 32)))
	require.NoError(t, err)
	got, err := s.FetchEvidence(t.Context(), f.p, f.publication(16384))
	assert.ErrorIs(t, err, domain.ErrStale)
	assert.Empty(t, got.Chunks)
}

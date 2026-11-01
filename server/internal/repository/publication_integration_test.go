package repository_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOwnerPublicationCreatesDistinctProjectTarget(t *testing.T) {
	store, jobs, _ := jobsGraph(t)
	objects := captureS3(t)
	warmObjects(t, objects)
	evidence := repository.NewEvidenceRepository(store)
	owners := repository.NewIdentityRepository(store)
	auth := repository.NewRetrievalRepository(store)
	identity := service.NewIdentityService(store, owners, gateway.NostrIdentity{})
	collector, err := identity.Setup(t.Context(), domain.OwnerIdentity{ID: "owner", PublicKey: "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"}, service.EnrollInput{ClientID: "collector", Role: domain.RoleCollector, ProjectIDs: []string{"demo"}, Capabilities: []string{"artifacts:write"}})
	require.NoError(t, err)
	agent, err := identity.EnrollHost(t.Context(), service.EnrollInput{ClientID: "agent", Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: []string{"candidate:write", "retrieval:read", "evidence:read"}})
	require.NoError(t, err)
	seedCapturePolicy(t, store, domain.Policy{Revision: "p1", SpoolLimitBytes: service.MaxArtifactBytes})
	capture := service.NewCaptureService(store, evidence, jobs, objects)
	host := service.NewHostCaptureService(store, evidence, jobs, objects, owners)
	verifyRepo := repository.NewVerificationRepository(store)
	pubRepo := repository.NewPublicationRepository(store)
	verify := service.NewVerificationService(store, verifyRepo, pubRepo, owners, evidence, host)
	memory := service.NewMemoryService(store, repository.NewMemoryRepository(store), auth, evidence, capture)
	retrieval, err := service.NewRetrievalService(store, auth, repository.NewCheckpointRepository(store), evidence, objects, bytes.Repeat([]byte{1}, 32))
	require.NoError(t, err)

	t.Run("host listing returns bounded credential-free metadata", func(t *testing.T) {
		page, e := identity.ListHost(t.Context(), 1, "")
		require.NoError(t, e)
		require.Len(t, page.Clients, 1)
		assert.Equal(t, "agent", page.Clients[0].Principal.ClientID)
		assert.Equal(t, "agent", page.Next)
		page, e = identity.ListHost(t.Context(), 1, page.Next)
		require.NoError(t, e)
		require.Len(t, page.Clients, 1)
		assert.Equal(t, "collector", page.Clients[0].Principal.ClientID)
		assert.Empty(t, page.Next)
		raw, e := json.Marshal(page)
		require.NoError(t, e)
		assert.NotContains(t, string(raw), collector.Bearer)
		assert.NotContains(t, string(raw), service.TokenDigest(collector.Bearer))
	})
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		n, e := store.Unwrap(tx)
		if e != nil {
			return e
		}
		res, e := n.Run(ctx, `SHOW CONSTRAINTS YIELD name WHERE name='owner_decision_key' RETURN name`, nil)
		if e != nil {
			return e
		}
		rows, e := res.Collect(ctx)
		require.Len(t, rows, 1)
		return e
	}))
	text := "PRIVATE prefix; approved excerpt; PRIVATE suffix"
	scope := domain.Scope{ProjectID: "demo", TaskID: "hidden-task", SessionID: "session"}
	artifact, err := capture.Stage(t.Context(), collector.Principal, service.ArtifactInput{Scope: scope, Digest: service.TokenDigest(text), Bytes: int64(len(text)), PolicyRevision: "p1", SourceLocator: "hidden-task/source#bytes", SourcePath: "source.txt"}, strings.NewReader(text))
	require.NoError(t, err)
	_, err = capture.Complete(t.Context(), collector.Principal, artifact.ID, artifact.Digest)
	require.NoError(t, err)
	candidate, err := memory.Submit(t.Context(), agent.Principal, domain.CandidateInput{ClaimID: "task-finding", Scope: scope, Text: "PRIVATE finding", Kind: "finding", Evidence: []domain.EvidenceRef{{ArtifactID: artifact.ID, Digest: artifact.Digest, Locator: artifact.SourceLocator, SourceRevision: "source-revision"}}})
	require.NoError(t, err)
	draft := service.AdjudicationDraft{ProjectID: "demo", CandidateRevision: candidate.Revision, ExpectedCandidateHead: candidate.Revision, Action: "verify", IdempotencyKey: "decision", TargetID: "project-finding", TargetText: "Owner approved finding", TargetKind: "finding", EntityIDs: []string{"safe-entity"}, Excerpts: []service.ExcerptSelection{{ArtifactID: artifact.ID, SourceRevision: "source-revision", SourceDigest: artifact.Digest, Start: 16, End: 32, ExpectedExcerptDigest: service.TokenDigest("approved excerpt")}}}
	preview, err := verify.PreviewHost(t.Context(), draft)
	require.NoError(t, err)
	t.Run("exact confirmation required", func(t *testing.T) {
		input := service.AdjudicationInput{AdjudicationDraft: draft, ExpectedDigest: service.TokenDigest("wrong")}
		_, err := verify.AdjudicateHost(t.Context(), input)
		assert.ErrorIs(t, err, domain.ErrStale)
	})
	result, err := verify.AdjudicateHost(t.Context(), service.AdjudicationInput{AdjudicationDraft: draft, ExpectedDigest: preview.Digest})
	require.NoError(t, err)
	require.NotEmpty(t, result.TargetRevision)
	assert.NotEqual(t, candidate.Revision, result.TargetRevision)
	replay, err := verify.AdjudicateHost(t.Context(), service.AdjudicationInput{AdjudicationDraft: draft, ExpectedDigest: preview.Digest})
	require.NoError(t, err)
	assert.Equal(t, result, replay)
	t.Run("project publication hides task evidence", func(t *testing.T) {
		out, err := retrieval.Retrieve(t.Context(), agent.Principal, domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "demo"}, Mode: domain.RetrievalEvidence})
		require.NoError(t, err)
		require.Len(t, out.Chunks, 1)
		raw, err := json.Marshal(out)
		require.NoError(t, err)
		for _, hidden := range []string{"PRIVATE", artifact.ID, artifact.SourceLocator, candidate.Revision, "hidden-task"} {
			assert.NotContains(t, string(raw), hidden)
		}
		require.Len(t, out.Chunks[0].Evidence, 1)
		ref := out.Chunks[0].Evidence[0]
		fetched, err := retrieval.Fetch(t.Context(), agent.Principal, ref.ArtifactID, ref.Locator, 16384)
		require.NoError(t, err)
		assert.Contains(t, fetched.Chunks[0].Text, "approved excerpt")
		_, err = retrieval.Fetch(t.Context(), agent.Principal, artifact.ID, artifact.SourceLocator, 16384)
		assert.ErrorIs(t, err, domain.ErrForbidden)
	})
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		state, e := verifyRepo.LoadRevision(ctx, tx, candidate.Revision)
		require.NoError(t, e)
		assert.Equal(t, domain.ClaimCandidate, state.Claim.Status)
		assert.Equal(t, domain.ClaimVerified, state.Status)
		head, e := verifyRepo.Head(ctx, tx, candidate.ClaimID)
		assert.Equal(t, candidate.Revision, head)
		return e
	}))
	t.Run("owner evidence applicability requires explicit safe publication fields", func(t *testing.T) {
		until := time.Now().UTC().Add(time.Hour)
		conditions := domain.Applicability{Environment: "production", Versions: map[string]string{"go": "1.24"}, ValidUntil: &until}
		refs := slices.Clone(candidate.Evidence)
		refs[0].Applicability = conditions
		c, e := memory.Submit(t.Context(), agent.Principal, domain.CandidateInput{ClaimID: "private-qualified", Scope: scope, Text: "PRIVATE qualified finding", Kind: "finding", Evidence: refs})
		require.NoError(t, e)
		d := draft
		d.CandidateRevision = c.Revision
		d.ExpectedCandidateHead = c.Revision
		d.TargetID = "public-qualified"
		d.IdempotencyKey = "qualified"
		d.TargetText = "Qualified approved finding"
		_, e = verify.PreviewHost(t.Context(), d)
		assert.ErrorIs(t, e, domain.ErrForbidden)
		d.TargetApplicability = conditions
		pre, e := verify.PreviewHost(t.Context(), d)
		require.NoError(t, e)
		published, e := verify.AdjudicateHost(t.Context(), service.AdjudicationInput{AdjudicationDraft: d, ExpectedDigest: pre.Digest})
		require.NoError(t, e)
		out, e := retrieval.Retrieve(t.Context(), agent.Principal, domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "demo"}, Query: d.TargetID, Mode: domain.RetrievalEvidence, Applicability: conditions})
		require.NoError(t, e)
		require.Len(t, out.Chunks, 1)
		assert.Equal(t, published.TargetRevision, out.Chunks[0].Revision)
		require.Len(t, out.Chunks[0].Evidence, 1)
		assert.Equal(t, conditions, out.Chunks[0].Evidence[0].Applicability)
		out, e = retrieval.Retrieve(t.Context(), agent.Principal, domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "demo"}, Query: d.TargetID, Mode: domain.RetrievalEvidence, Applicability: domain.Applicability{Environment: "development", Versions: conditions.Versions}})
		require.NoError(t, e)
		assert.True(t, out.NoResults)
	})

	freshDraft := func(name string, dependencies ...string) service.AdjudicationDraft {
		c, e := memory.Submit(t.Context(), agent.Principal, domain.CandidateInput{ClaimID: "private-" + name, Scope: scope, Text: "PRIVATE " + name, Kind: "finding", Evidence: candidate.Evidence, DependsOn: dependencies})
		require.NoError(t, e)
		d := draft
		d.CandidateRevision = c.Revision
		d.ExpectedCandidateHead = c.Revision
		d.IdempotencyKey = name
		d.TargetID = "public-" + name
		return d
	}
	t.Run("TestPublicationAtomicRollbackIncludesVerificationAndOutbox", func(t *testing.T) {
		d := freshDraft("rollback")
		pre, e := verify.PreviewHost(t.Context(), d)
		require.NoError(t, e)
		failing := service.NewVerificationService(store, verifyRepo, failLineageRepository{PublicationRepository: pubRepo}, owners, evidence, host)
		_, e = failing.AdjudicateHost(t.Context(), service.AdjudicationInput{AdjudicationDraft: d, ExpectedDigest: pre.Digest})
		assert.ErrorIs(t, e, domain.ErrUnavailable)
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			state, e := verifyRepo.LoadRevision(ctx, tx, d.CandidateRevision)
			require.NoError(t, e)
			assert.Equal(t, domain.ClaimCandidate, state.Status)
			assert.False(t, state.Valid)
			head, e := verifyRepo.Head(ctx, tx, d.TargetID)
			require.NoError(t, e)
			assert.Empty(t, head)
			_, e = verifyRepo.LoadDecisionByKey(ctx, tx, "owner", d.IdempotencyKey)
			assert.ErrorIs(t, e, domain.ErrIncomplete)
			n, e := store.Unwrap(tx)
			require.NoError(t, e)
			res, e := n.Run(ctx, `MATCH (v:Verification {claimRevision:$revision}) RETURN count(v)`, map[string]any{"revision": d.CandidateRevision})
			require.NoError(t, e)
			row, e := res.Single(ctx)
			require.NoError(t, e)
			assert.Equal(t, int64(0), row.Values[0])
			return nil
		}))

		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			queued, e := jobs.List(ctx, tx, domain.JobQueued, 50)
			if e != nil {
				return e
			}
			require.NotEmpty(t, queued)
			for _, job := range queued {
				assert.Contains(t, []string{"capture.reconcile", "evidence.extract"}, job.Kind)
				assert.NotContains(t, job.InputRevisionIDs, d.CandidateRevision)
			}
			return nil
		}))
		// F4 prepared bytes and its supported jobs may remain, but without a committed
		// publication no project artifact can be fetched.

		var unpublished []domain.Artifact
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			n, e := store.Unwrap(tx)
			if e != nil {
				return e
			}
			res, e := n.Run(ctx, `MATCH (a:Artifact {projectID:'demo',taskID:''}) WHERE NOT EXISTS {MATCH (:ClaimRevision)-[:EVIDENCE]->(a)} RETURN a.data`, nil)
			if e != nil {
				return e
			}
			rows, e := res.Collect(ctx)
			if e != nil {
				return e
			}
			for _, row := range rows {
				var a domain.Artifact
				require.NoError(t, json.Unmarshal([]byte(row.Values[0].(string)), &a))
				unpublished = append(unpublished, a)
			}
			return nil
		}))
		require.NotEmpty(t, unpublished)
		for _, a := range unpublished {
			_, e := retrieval.Fetch(t.Context(), agent.Principal, a.ID, a.SourceLocator, 16384)
			assert.ErrorIs(t, e, domain.ErrForbidden)
		}

	})
	t.Run("TestReplayAfterUnknownCommitCreatesOnePublication", func(t *testing.T) {
		d := freshDraft("unknown-commit")
		pre, e := verify.PreviewHost(t.Context(), d)
		require.NoError(t, e)
		uncertain := &lostOwnerCommit{store: store, key: d.IdempotencyKey}
		unstable := service.NewVerificationService(uncertain, verifyRepo, pubRepo, owners, evidence, host)
		_, e = unstable.AdjudicateHost(t.Context(), service.AdjudicationInput{AdjudicationDraft: d, ExpectedDigest: pre.Digest})
		assert.ErrorIs(t, e, domain.ErrUnavailable)
		got, e := unstable.AdjudicateHost(t.Context(), service.AdjudicationInput{AdjudicationDraft: d, ExpectedDigest: pre.Digest})
		require.NoError(t, e)
		assert.NotEmpty(t, got.TargetRevision)
	})
	t.Run("TestPublicationRaceWithEvidenceInvalidation", func(t *testing.T) {
		d := freshDraft("evidence-race")
		pre, e := verify.PreviewHost(t.Context(), d)
		require.NoError(t, e)
		barrier := &barrierOwnerEvidence{OwnerEvidence: host, started: make(chan struct{}), release: make(chan struct{})}
		racing := service.NewVerificationService(store, verifyRepo, pubRepo, owners, evidence, barrier)
		done := make(chan error, 1)
		go func() {
			_, err := racing.AdjudicateHost(t.Context(), service.AdjudicationInput{AdjudicationDraft: d, ExpectedDigest: pre.Digest})
			done <- err
		}()
		<-barrier.started
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			a, _, err := evidence.Artifact(ctx, tx, artifact.ID)
			if err != nil {
				return err
			}
			return evidence.Availability(ctx, tx, a.ID, a.ObjectKey, false)
		}))
		close(barrier.release)
		require.Error(t, <-done)
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			head, err := verifyRepo.Head(ctx, tx, d.TargetID)
			assert.Empty(t, head)
			return err
		}))
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			a, _, err := evidence.Artifact(ctx, tx, artifact.ID)
			if err != nil {
				return err
			}
			return evidence.Availability(ctx, tx, a.ID, a.ObjectKey, true)
		}))
	})
	t.Run("TestPolicyOrHeadChangeDuringExcerptPreparationAborts", func(t *testing.T) {
		d := freshDraft("head-race")
		pre, e := verify.PreviewHost(t.Context(), d)
		require.NoError(t, e)
		barrier := &barrierOwnerEvidence{OwnerEvidence: host, started: make(chan struct{}), release: make(chan struct{})}
		racing := service.NewVerificationService(store, verifyRepo, pubRepo, owners, evidence, barrier)
		done := make(chan error, 1)
		go func() {
			_, err := racing.AdjudicateHost(t.Context(), service.AdjudicationInput{AdjudicationDraft: d, ExpectedDigest: pre.Digest})
			done <- err
		}()
		<-barrier.started
		memoryMutation(t, store, `MATCH (h:Claim {id:$claim})-[head:HEAD]->(c:ClaimRevision) DELETE head`, map[string]any{"claim": "private-head-race"})
		close(barrier.release)
		e = <-done
		require.ErrorIs(t, e, domain.ErrConflict)
	})
	t.Run("TestInvalidationExcludesTransitiveDependentsWithStaleDiscovery", func(t *testing.T) {
		// The current publication is used as direct support for a second target. Both
		// closures retain the original hidden source, so one invalidation excludes all.
		c, e := memory.Submit(t.Context(), agent.Principal, domain.CandidateInput{ClaimID: "private-transitive", Scope: scope, Text: "PRIVATE transitive", Kind: "finding", Evidence: candidate.Evidence, DependsOn: []string{result.TargetRevision}})
		require.NoError(t, e)
		d := draft
		d.CandidateRevision = c.Revision
		d.ExpectedCandidateHead = c.Revision
		d.IdempotencyKey = "transitive"
		d.TargetID = "public-transitive"
		pre, e := verify.PreviewHost(t.Context(), d)
		require.NoError(t, e)
		_, e = verify.AdjudicateHost(t.Context(), service.AdjudicationInput{AdjudicationDraft: d, ExpectedDigest: pre.Digest})
		require.NoError(t, e)
		pending := freshDraft("support-race", result.TargetRevision)
		confirm, e := verify.PreviewHost(t.Context(), pending)
		require.NoError(t, e)
		barrier := &barrierOwnerEvidence{OwnerEvidence: host, started: make(chan struct{}), release: make(chan struct{})}
		racing := service.NewVerificationService(store, verifyRepo, pubRepo, owners, evidence, barrier)
		done := make(chan error, 1)
		go func() {
			_, err := racing.AdjudicateHost(t.Context(), service.AdjudicationInput{AdjudicationDraft: pending, ExpectedDigest: confirm.Digest})
			done <- err
		}()
		<-barrier.started
		invalidation, e := verify.PreviewInvalidationHost(t.Context(), candidate.Revision)
		require.NoError(t, e)
		invalidation.IdempotencyKey = "invalidate-root"
		receipt, e := verify.InvalidateHost(t.Context(), invalidation)
		require.NoError(t, e)
		assert.Equal(t, "invalidated", receipt.State)
		close(barrier.release)
		require.Error(t, <-done)
		out, e := retrieval.Retrieve(t.Context(), agent.Principal, domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "demo"}, Query: "project-finding", Mode: domain.RetrievalEvidence})
		require.NoError(t, e)
		assert.True(t, out.NoResults)
		out, e = retrieval.Retrieve(t.Context(), agent.Principal, domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "demo"}, Query: "public-transitive", Mode: domain.RetrievalEvidence})
		require.NoError(t, e)
		assert.True(t, out.NoResults)
		_, e = retrieval.Fetch(t.Context(), agent.Principal, artifact.ID, artifact.SourceLocator, 16384)
		assert.ErrorIs(t, e, domain.ErrForbidden)
	})

	t.Run("host source honors tombstones", func(t *testing.T) {
		memoryMutation(t, store, `CREATE (:Tombstone {id:$id})`, map[string]any{"id": artifact.ID})
		_, _, err := host.ReadHost(t.Context(), artifact.ID)
		assert.ErrorIs(t, err, domain.ErrDeleted)
		memoryMutation(t, store, `MATCH (t:Tombstone {id:$id}) DELETE t`, map[string]any{"id": artifact.ID})
	})
	t.Run("host source honors indexed state", func(t *testing.T) {
		memoryMutation(t, store, `MATCH (a:Artifact {id:$id}) SET a.state='deleted'`, map[string]any{"id": artifact.ID})
		_, _, err := host.ReadHost(t.Context(), artifact.ID)
		assert.ErrorIs(t, err, domain.ErrDeleted)
		memoryMutation(t, store, `MATCH (a:Artifact {id:$id}) SET a.state='ready'`, map[string]any{"id": artifact.ID})
	})
	t.Run("reserved legacy client collision fails host closed", func(t *testing.T) {
		memoryMutation(t, store, `CREATE (:Client {id:'host-owner:legacy',ownerID:'owner',role:'agent',projectIDs:['demo'],capabilities:['status:read'],tokenDigest:'legacy-fixture'})`, nil)
		_, err := identity.ListHost(t.Context(), 50, "")
		assert.ErrorIs(t, err, domain.ErrForbidden)
		memoryMutation(t, store, `MATCH (c:Client {id:'host-owner:legacy'}) DELETE c`, nil)
	})

}

type failLineageRepository struct{ service.PublicationRepository }

func (f failLineageRepository) PutExcerptLineage(context.Context, domain.Tx, domain.ExcerptLineage) error {
	return domain.ErrUnavailable
}

type barrierOwnerEvidence struct {
	service.OwnerEvidence
	started, release chan struct{}
	once             sync.Once
}

func (b *barrierOwnerEvidence) ReadHost(ctx context.Context, id string) (domain.Artifact, []byte, error) {
	a, data, err := b.OwnerEvidence.ReadHost(ctx, id)
	if err != nil {
		return a, data, err
	}
	b.once.Do(func() {
		close(b.started)
		select {
		case <-b.release:
		case <-ctx.Done():
		}
	})
	return a, data, err
}

type lostOwnerCommit struct {
	store *graph.Store
	key   string
	lost  bool
}

func (l *lostOwnerCommit) Within(ctx context.Context, fn func(context.Context, domain.Tx) error) error {
	committed := false
	err := l.store.Within(ctx, func(ctx context.Context, tx domain.Tx) error {
		if e := fn(ctx, tx); e != nil {
			return e
		}
		n, e := l.store.Unwrap(tx)
		if e != nil {
			return e
		}
		res, e := n.Run(ctx, `MATCH (d:OwnerDecision {key:$key}) RETURN count(d)`, map[string]any{"key": l.key})
		if e != nil {
			return e
		}
		row, e := res.Single(ctx)
		if e != nil {
			return e
		}
		committed = row.Values[0].(int64) > 0
		return nil
	})
	if err == nil && committed && !l.lost {
		l.lost = true
		return domain.ErrUnavailable
	}
	return err
}

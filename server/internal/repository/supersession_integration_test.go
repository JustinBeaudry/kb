package repository_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A newer owner publication of the same claim retires the earlier verified
// revision in the real graph, so retrieval presents one current revision.
func TestNewerPublicationSupersedesPriorRevisionInRetrieval(t *testing.T) {
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
	verify := service.NewVerificationService(store, verifyRepo, repository.NewPublicationRepository(store), owners, evidence, host)
	memory := service.NewMemoryService(store, repository.NewMemoryRepository(store), auth, evidence, capture)
	retrieval, err := service.NewRetrievalService(store, auth, repository.NewCheckpointRepository(store), evidence, objects, bytes.Repeat([]byte{1}, 32))
	require.NoError(t, err)

	text := "PRIVATE prefix; approved excerpt; PRIVATE suffix"
	scope := domain.Scope{ProjectID: "demo", TaskID: "task", SessionID: "session"}
	artifact, err := capture.Stage(t.Context(), collector.Principal, service.ArtifactInput{Scope: scope, Digest: service.TokenDigest(text), Bytes: int64(len(text)), PolicyRevision: "p1", SourceLocator: "task/source#bytes", SourcePath: "source.txt"}, strings.NewReader(text))
	require.NoError(t, err)
	_, err = capture.Complete(t.Context(), collector.Principal, artifact.ID, artifact.Digest)
	require.NoError(t, err)
	refs := []domain.EvidenceRef{{ArtifactID: artifact.ID, Digest: artifact.Digest, Locator: artifact.SourceLocator, SourceRevision: "source-revision"}}

	publish := func(candidateID, targetID, targetText string, dependsOn ...string) string {
		t.Helper()
		c, e := memory.Submit(t.Context(), agent.Principal, domain.CandidateInput{ClaimID: candidateID, Scope: scope, Text: "PRIVATE " + candidateID, Kind: "finding", Evidence: refs, DependsOn: dependsOn})
		require.NoError(t, e)
		var head string
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			head, e = verifyRepo.Head(ctx, tx, targetID)
			return e
		}))
		d := service.AdjudicationDraft{ProjectID: "demo", CandidateRevision: c.Revision, ExpectedCandidateHead: c.Revision, Action: "verify", IdempotencyKey: candidateID, TargetID: targetID, ExpectedTargetHead: head, TargetText: targetText, TargetKind: "finding", Excerpts: []service.ExcerptSelection{{ArtifactID: artifact.ID, SourceRevision: "source-revision", SourceDigest: artifact.Digest, Start: 16, End: 32, ExpectedExcerptDigest: service.TokenDigest("approved excerpt")}}}
		pre, e := verify.PreviewHost(t.Context(), d)
		require.NoError(t, e)
		result, e := verify.AdjudicateHost(t.Context(), service.AdjudicationInput{AdjudicationDraft: d, ExpectedDigest: pre.Digest})
		require.NoError(t, e)
		return result.TargetRevision
	}
	status := func(revision string) domain.ClaimStatus {
		t.Helper()
		var state domain.RevisionState
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			var e error
			state, e = verifyRepo.LoadRevision(ctx, tx, revision)
			return e
		}))
		return state.Status
	}
	retrieve := func(query string) domain.RetrievalResult {
		t.Helper()
		out, e := retrieval.Retrieve(t.Context(), agent.Principal, domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "demo"}, Query: query, Mode: domain.RetrievalEvidence})
		require.NoError(t, e)
		return out
	}

	first := publish("candidate-one", "shared-finding", "Shared finding first wording")
	dependent := publish("candidate-dependent", "dependent-finding", "Dependent finding cites first wording", first)
	second := publish("candidate-two", "shared-finding", "Shared finding second wording")

	t.Run("prior revision is superseded in the same publication", func(t *testing.T) {
		assert.Equal(t, domain.ClaimSuperseded, status(first))
		assert.Equal(t, domain.ClaimVerified, status(second))
	})
	t.Run("retrieval presents only the current revision as verified", func(t *testing.T) {
		out := retrieve("Shared finding")
		require.Len(t, out.Chunks, 1)
		chunk := out.Chunks[0]
		assert.Equal(t, second, chunk.Revision)
		assert.Equal(t, domain.ClaimVerified, chunk.ClaimStatus)
		assert.NotContains(t, chunk.Warnings, "conflicting_versions")
	})
	t.Run("publications pinned to the superseded revision stay visible", func(t *testing.T) {
		out := retrieve("Dependent finding")
		require.Len(t, out.Chunks, 1)
		assert.Equal(t, dependent, out.Chunks[0].Revision)
		assert.Equal(t, domain.ClaimVerified, out.Chunks[0].ClaimStatus)
	})
	t.Run("publications pinned to the superseded revision warn that their support is superseded", func(t *testing.T) {
		out := retrieve("Dependent finding")
		require.Len(t, out.Chunks, 1)
		assert.Contains(t, out.Chunks[0].Warnings, "support_superseded")
		current := retrieve("Shared finding")
		require.Len(t, current.Chunks, 1)
		assert.NotContains(t, current.Chunks[0].Warnings, "support_superseded")
	})
	t.Run("superseded revisions do not consume search seed slots", func(t *testing.T) {
		var seeds []domain.ClaimRevision
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			var e error
			seeds, e = auth.Seeds(ctx, tx, domain.Scope{ProjectID: "demo"}, "shared-finding", 50)
			return e
		}))
		var revisions []string
		for _, c := range seeds {
			revisions = append(revisions, c.Revision)
		}
		assert.Equal(t, []string{second}, revisions)
		out, e := retrieval.Retrieve(t.Context(), agent.Principal, domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "demo"}, Query: "shared-finding", Mode: domain.RetrievalEvidence, MaxVisited: 1})
		require.NoError(t, e)
		require.Len(t, out.Chunks, 1)
		assert.Equal(t, second, out.Chunks[0].Revision)
	})
	t.Run("new candidates cannot build on a claim whose support is superseded", func(t *testing.T) {
		_, e := memory.Submit(t.Context(), agent.Principal, domain.CandidateInput{ClaimID: "candidate-transitive-stale", Scope: scope, Text: "PRIVATE transitive", Kind: "finding", Evidence: refs, DependsOn: []string{dependent}})
		assert.ErrorIs(t, e, domain.ErrIncomplete)
	})
	t.Run("backfill supersedes legacy duplicate verified revisions idempotently", func(t *testing.T) {
		// Simulate data written before publication superseded prior revisions.
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			native, e := store.Unwrap(tx)
			if e != nil {
				return e
			}
			_, e = native.Run(ctx, `MATCH (c:ClaimRevision {id:$id}) SET c.status='verified'`, map[string]any{"id": first})
			return e
		}))
		require.Equal(t, domain.ClaimVerified, status(first))

		for range 2 {
			require.NoError(t, store.InitializeSchema(t.Context()))
			assert.Equal(t, domain.ClaimSuperseded, status(first))
			assert.Equal(t, domain.ClaimVerified, status(second))
			assert.Equal(t, domain.ClaimVerified, status(dependent))
		}
	})
	t.Run("new candidates cannot cite the superseded revision", func(t *testing.T) {
		_, e := memory.Submit(t.Context(), agent.Principal, domain.CandidateInput{ClaimID: "candidate-stale-citation", Scope: scope, Text: "PRIVATE stale", Kind: "finding", Evidence: refs, DependsOn: []string{first}})
		assert.Error(t, e)
	})
	t.Run("invalidating the newer revision does not resurrect the older one", func(t *testing.T) {
		input, e := verify.PreviewInvalidationHost(t.Context(), second)
		require.NoError(t, e)
		input.IdempotencyKey = "invalidate-second"
		_, e = verify.InvalidateHost(t.Context(), input)
		require.NoError(t, e)

		assert.Equal(t, domain.ClaimInvalidated, status(second))
		assert.Equal(t, domain.ClaimSuperseded, status(first))
		assert.True(t, retrieve("Shared finding").NoResults)
	})
	t.Run("invalidating a superseded revision withdraws publications pinned to it", func(t *testing.T) {
		input, e := verify.PreviewInvalidationHost(t.Context(), first)
		require.NoError(t, e)
		input.IdempotencyKey = "invalidate-first"
		_, e = verify.InvalidateHost(t.Context(), input)
		require.NoError(t, e)

		assert.Equal(t, domain.ClaimInvalidated, status(first))
		assert.Equal(t, domain.ClaimInvalidated, status(dependent))
		assert.True(t, retrieve("Dependent finding").NoResults)
	})
}

package repository_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This regression uses legal public admission for the working dependent. Only
// the longer pre-projection candidate chain is seeded as legacy persisted data;
// public Submit must continue refusing candidate-to-candidate dependencies.
func TestLifecycleDeletionScrubsUnpublishedTransitiveDependentsAfterJournalReplay(t *testing.T) {
	store, jobs, jobsService := jobsGraph(t)
	objects := captureS3(t)
	warmObjects(t, objects)
	journal := independentLifecycleJournal(t)
	owners := repository.NewIdentityRepository(store)
	evidence := repository.NewEvidenceRepository(store)
	repo := repository.NewLifecycleRepository(store)
	auth := repository.NewRetrievalRepository(store)
	identity := service.NewIdentityService(store, owners, gateway.NostrIdentity{})
	collector, err := identity.Setup(t.Context(), domain.OwnerIdentity{ID: "owner", PublicKey: "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"}, service.EnrollInput{ClientID: "dependency-collector", Role: domain.RoleCollector, ProjectIDs: []string{"demo", "other"}, Capabilities: []string{"artifacts:write"}})
	require.NoError(t, err)
	agent, err := identity.EnrollHost(t.Context(), service.EnrollInput{ClientID: "dependency-agent", Role: domain.RoleAgent, ProjectIDs: []string{"demo", "other"}, Capabilities: []string{"candidate:write", "retrieval:read", "evidence:read"}})
	require.NoError(t, err)
	lifecycle := service.NewLifecycleService(store, repo, owners, jobs, objects, journal, time.Now)
	genesis, err := lifecycle.InitializeJournalHost(t.Context())
	require.NoError(t, err)
	require.NoError(t, lifecycle.Recover(t.Context()))
	for _, project := range []string{"demo", "other"} {
		_, err := lifecycle.UpdatePolicyHost(t.Context(), project, "", domain.Policy{Revision: "p1", SpoolLimitBytes: service.MaxArtifactBytes})
		require.NoError(t, err)
	}
	capture := service.NewCaptureService(store, evidence, jobs, objects)
	memory := service.NewMemoryService(store, repository.NewMemoryRepository(store), auth, evidence, capture)
	supportScope := domain.Scope{ProjectID: "demo", TaskID: "dependency-support-task", SessionID: "dependency-support-session"}
	text := "PRIVATE support prefix; approved excerpt; PRIVATE support suffix"
	upload := func(scope domain.Scope, locator, content string) domain.Artifact {
		t.Helper()
		a, e := capture.Stage(t.Context(), collector.Principal, service.ArtifactInput{Scope: scope, Digest: service.TokenDigest(content), Bytes: int64(len(content)), PolicyRevision: "p1", SourceLocator: locator, SourcePath: "source.txt"}, strings.NewReader(content))
		require.NoError(t, e)
		_, e = capture.Complete(t.Context(), collector.Principal, a.ID, a.Digest)
		require.NoError(t, e)
		return a
	}
	source := upload(supportScope, "dependency-original", text)
	original, err := memory.Submit(t.Context(), agent.Principal, domain.CandidateInput{ClaimID: "dependency-original-claim", Scope: supportScope, Text: "PRIVATE original support finding", Kind: "finding", Evidence: []domain.EvidenceRef{{ArtifactID: source.ID, Digest: source.Digest, Locator: source.SourceLocator, SourceRevision: "dependency-source-revision"}}})
	require.NoError(t, err)
	verify := service.NewVerificationService(store, repository.NewVerificationRepository(store), repository.NewPublicationRepository(store), owners, evidence, service.NewHostCaptureService(store, evidence, jobs, objects, owners))
	start := int64(strings.Index(text, "approved excerpt"))
	draft := service.AdjudicationDraft{ProjectID: "demo", CandidateRevision: original.Revision, ExpectedCandidateHead: original.Revision, Action: "verify", IdempotencyKey: "dependency-publish", TargetID: "dependency-public-support", TargetText: "Approved project support", TargetKind: "finding", Excerpts: []service.ExcerptSelection{{ArtifactID: source.ID, SourceRevision: "dependency-source-revision", SourceDigest: source.Digest, Start: start, End: start + int64(len("approved excerpt")), ExpectedExcerptDigest: service.TokenDigest("approved excerpt")}}}
	preview, err := verify.PreviewHost(t.Context(), draft)
	require.NoError(t, err)
	publication, err := verify.AdjudicateHost(t.Context(), service.AdjudicationInput{AdjudicationDraft: draft, ExpectedDigest: preview.Digest})
	require.NoError(t, err)
	workingScope := domain.Scope{ProjectID: "demo", TaskID: "dependency-working-task", SessionID: "dependency-working-session"}
	independentBody := "PRIVATE independent B prefix; selected B excerpt; PRIVATE independent B suffix"
	independentSource := upload(workingScope, "dependency-independent-b", independentBody)
	independentRef := domain.EvidenceRef{ArtifactID: independentSource.ID, Digest: independentSource.Digest, Locator: independentSource.SourceLocator, SourceRevision: "dependency-independent-b-revision"}

	working, err := memory.Submit(t.Context(), agent.Principal, domain.CandidateInput{ClaimID: "dependency-working-d", Scope: workingScope, Text: "PRIVATE unpublished D derived from approved support", Kind: "hypothesis", Evidence: []domain.EvidenceRef{independentRef}, DependsOn: []string{publication.TargetRevision}})
	require.NoError(t, err, "published support is legal for a working candidate")
	excerptBody := "selected B excerpt"
	excerptStart := int64(strings.Index(independentBody, excerptBody))
	reservation := domain.ExcerptLineage{ID: "dependency-prepared-d-b", DecisionID: "dependency-prepared-decision", InputDigest: service.TokenDigest("dependency-prepared-input"), SourceArtifactID: independentSource.ID, SourceRevision: independentRef.SourceRevision, SourceDigest: independentSource.Digest, SourceGeneration: independentSource.Generation, SupportRevision: working.Revision, Start: excerptStart, End: excerptStart + int64(len(excerptBody)), ExcerptDigest: service.TokenDigest(excerptBody), SourceOrigin: domain.Origin{ClientID: collector.Principal.ClientID, Trust: domain.TrustSubmitted}}
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		return repository.NewPublicationRepository(store).ReserveDerivation(ctx, tx, "demo", reservation)
	}))
	hostCapture := service.NewHostCaptureService(store, evidence, jobs, objects, owners)
	pendingExcerpt, err := hostCapture.StageHost(t.Context(), service.ArtifactInput{Scope: domain.Scope{ProjectID: "demo"}, Digest: reservation.ExcerptDigest, Bytes: int64(len(excerptBody)), PolicyRevision: "p1", SourceLocator: "excerpt:dependency-prepared-d-b", SourcePath: independentSource.SourcePath, DerivationID: reservation.ID}, strings.NewReader(excerptBody))
	require.NoError(t, err)
	_, err = hostCapture.CompleteHost(t.Context(), pendingExcerpt.ID, pendingExcerpt.Digest)
	require.NoError(t, err)
	require.NotEqual(t, independentSource.Digest, pendingExcerpt.Digest, "whole source B must be independently preservable")

	_, err = memory.Submit(t.Context(), agent.Principal, domain.CandidateInput{ClaimID: "dependency-public-admission-e", Scope: workingScope, Text: "candidate chain remains denied", Kind: "hypothesis", DependsOn: []string{working.Revision}})
	require.ErrorIs(t, err, domain.ErrIncomplete, "do not loosen public admission to manufacture a candidate chain")

	legacy := domain.ClaimRevision{ClaimID: "dependency-legacy-e", Revision: "dependency-legacy-e-revision", Scope: workingScope, Text: "PRIVATE legacy unpublished E transitively derived through D", Kind: "hypothesis", Status: domain.ClaimCandidate, SubmittedBy: agent.Principal.ClientID, DependsOn: []string{working.Revision}}
	dependencySeedLegacyClaim(t, store, legacy)
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		n, e := store.Unwrap(tx)
		if e != nil {
			return e
		}
		result, e := n.Run(ctx, `MATCH (c:ClaimRevision {id:$id}) RETURN c.lifecycleIndexed,coalesce(c.dependencyProjectionVersion,0),EXISTS {MATCH (c)-[:DEPENDS_ON]->()}`, map[string]any{"id": legacy.Revision})
		if e != nil {
			return e
		}
		row, e := result.Single(ctx)
		require.NoError(t, e)
		assert.Equal(t, true, row.Values[0], "legacy row already completed identity indexing")
		assert.Equal(t, int64(0), row.Values[1])
		assert.Equal(t, false, row.Values[2])
		return nil
	}))

	otherScope := domain.Scope{ProjectID: "other", TaskID: "dependency-other-task", SessionID: "dependency-other-session"}
	otherSource := upload(otherScope, "dependency-other-same-bytes", text)
	other, err := memory.Submit(t.Context(), agent.Principal, domain.CandidateInput{ClaimID: "dependency-other-claim", Scope: otherScope, Text: "PRIVATE unselected project finding", Kind: "finding", Evidence: []domain.EvidenceRef{{ArtifactID: otherSource.ID, Digest: otherSource.Digest, Locator: otherSource.SourceLocator, SourceRevision: "dependency-other-source-revision"}}})
	require.NoError(t, err)
	_, err = memory.Submit(t.Context(), agent.Principal, domain.CandidateInput{ClaimID: "dependency-cross-project-admission", Scope: otherScope, Text: "cross-project published dependency must stay denied", Kind: "hypothesis", DependsOn: []string{publication.TargetRevision}})
	require.ErrorIs(t, err, domain.ErrIncomplete)
	// Legacy cross-project data must not turn a project-scoped privacy deletion
	// into a global graph walk. F5 still refuses this dependency at public input.
	otherLegacy := domain.ClaimRevision{ClaimID: "dependency-other-legacy", Revision: "dependency-other-legacy-revision", Scope: otherScope, Text: "PRIVATE unselected legacy cross-project metadata", Kind: "hypothesis", Status: domain.ClaimCandidate, SubmittedBy: agent.Principal.ClientID, DependsOn: []string{publication.TargetRevision}}
	dependencySeedLegacyClaim(t, store, otherLegacy)

	// Retain graph state from before deletion/projection recovery. The independent
	// encrypted journal and evidence object namespace remain outside this restore.
	snapshots := dependencySnapshotGraph(t, store)
	require.NoError(t, lifecycle.Recover(t.Context()), "legacy dependency JSON must be projected before admitting recovery")
	dependencyAssertEdge(t, store, working.Revision, publication.TargetRevision)
	dependencyAssertEdge(t, store, legacy.Revision, working.Revision)
	affected := []string{original.Revision, publication.TargetRevision, working.Revision, legacy.Revision}
	selection := domain.DeleteSelection{ProjectID: "demo", IDs: []string{source.ID}, IdempotencyKey: "delete-unpublished-dependency-source"}
	deletionPreview, err := lifecycle.PreviewHost(t.Context(), selection)
	require.NoError(t, err)
	assert.NotContains(t, deletionPreview.Manifest.IDs, otherSource.ID)
	assert.Contains(t, deletionPreview.Manifest.IDs, pendingExcerpt.ID, "derived excerpt belongs to affected D")
	assert.NotContains(t, deletionPreview.Manifest.IDs, independentSource.ID, "dependent D's independent evidence must not become an explicit deletion root")
	assert.NotContains(t, deletionPreview.Manifest.SourceDigests, independentSource.Digest)

	selection.ExpectedSelectionDigest = deletionPreview.SelectionDigest
	receipt, err := lifecycle.DeleteHost(t.Context(), selection)
	require.NoError(t, err)
	require.Equal(t, "purge_pending", receipt.State)
	dependencyAssertClaims(t, store, affected, false, false)
	dependencyAssertUnchanged(t, store, other)
	dependencyAssertUnchanged(t, store, otherLegacy)
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		for _, id := range []string{independentSource.ID, otherSource.ID} {
			retained, _, e := evidence.Artifact(ctx, tx, id)
			require.NoError(t, e)
			assert.Equal(t, domain.ArtifactReady, retained.State)
			assert.True(t, retained.ObjectAvailable)
		}
		return nil
	}))

	// Model restoring the old graph without graph-side deletion intent/tombstones.
	// Recover must rebuild the legacy projection and replay the durable full intent
	// before these resurrected graph rows can be served.
	dependencyRestoreGraph(t, store, snapshots, receipt.ID, genesis, repo)
	require.NoError(t, lifecycle.Recover(t.Context()))
	dependencyAssertClaims(t, store, affected, false, false)
	status, err := lifecycle.DeletionStatusHost(t.Context(), receipt.ID)
	require.NoError(t, err)
	assert.Equal(t, "purge_pending", status.State)

	job, lease, err := jobsService.Claim(t.Context(), "dependency-purger", []string{"lifecycle.purge"}, time.Now().UTC().Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, []string{receipt.ID}, job.InputRevisionIDs)
	require.NoError(t, lifecycle.Purge(t.Context(), job, lease, jobsService))
	dependencyAssertClaims(t, store, affected, false, true)
	dependencyAssertEdge(t, store, working.Revision, publication.TargetRevision)
	dependencyAssertEdge(t, store, legacy.Revision, working.Revision)
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		_, _, e := evidence.Artifact(ctx, tx, pendingExcerpt.ID)
		assert.ErrorIs(t, e, domain.ErrDeleted)
		retained, _, e := evidence.Artifact(ctx, tx, independentSource.ID)
		require.NoError(t, e)
		assert.Equal(t, domain.ArtifactReady, retained.State)
		assert.True(t, retained.ObjectAvailable)
		return nil
	}))
	_, err = objects.Open(t.Context(), pendingExcerpt.ObjectKey)
	assert.ErrorIs(t, err, domain.ErrIncomplete)
	independentReader, err := objects.Open(t.Context(), independentSource.ObjectKey)
	require.NoError(t, err)
	retainedBody, err := io.ReadAll(independentReader)
	require.NoError(t, err)
	require.NoError(t, independentReader.Close())
	assert.Equal(t, independentBody, string(retainedBody))

	retrieval, err := service.NewRetrievalService(store, auth, repository.NewCheckpointRepository(store), evidence, objects, bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	for _, request := range []domain.RetrievalRequest{{Scope: workingScope, Mode: domain.RetrievalWorking}, {Scope: domain.Scope{ProjectID: "demo"}, Mode: domain.RetrievalEvidence}} {
		out, e := retrieval.Retrieve(t.Context(), agent.Principal, request)
		require.NoError(t, e, "broad reads skip scrubbed rows without decoding deleted JSON")
		assert.True(t, out.NoResults)
		for _, chunk := range out.Chunks {
			assert.NotContains(t, affected, chunk.Revision)
		}
	}

	status, err = lifecycle.DeletionStatusHost(t.Context(), receipt.ID)
	require.NoError(t, err)
	assert.Equal(t, "purge_complete", status.State)
	assert.Equal(t, "expiry_pending", status.BackupState)
	_, err = objects.Open(t.Context(), source.ObjectKey)
	assert.ErrorIs(t, err, domain.ErrIncomplete)
	deniedIDs := []string{}
	for _, dependency := range []string{publication.TargetRevision, working.Revision, legacy.Revision} {
		claimID := "denied-after-delete-" + dependency
		deniedIDs = append(deniedIDs, claimID)
		denied, denyErr := memory.Submit(t.Context(), agent.Principal, domain.CandidateInput{ClaimID: claimID, Scope: workingScope, Text: "must not derive from a deleted dependency", Kind: "hypothesis", DependsOn: []string{dependency}})
		// This contract is fail-closed admission; it does not invent a public
		// error mapping for dependencies whose source payload was scrubbed.
		require.Error(t, denyErr)
		assert.Empty(t, denied.Revision)
	}
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		n, e := store.Unwrap(tx)
		if e != nil {
			return e
		}
		result, e := n.Run(ctx, `MATCH (c:ClaimRevision) WHERE c.claimID IN $ids RETURN count(c)`, map[string]any{"ids": deniedIDs})
		if e != nil {
			return e
		}
		row, e := result.Single(ctx)
		require.NoError(t, e)
		assert.Equal(t, int64(0), row.Values[0], "denied dependency input must persist no candidate")
		return nil
	}))
	dependencyAssertUnchanged(t, store, other)
	dependencyAssertUnchanged(t, store, otherLegacy)
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		retained, _, e := evidence.Artifact(ctx, tx, otherSource.ID)
		require.NoError(t, e)
		assert.True(t, retained.ObjectAvailable)
		assert.Equal(t, domain.ArtifactReady, retained.State)
		return nil
	}))
	reader, err := objects.Open(t.Context(), otherSource.ObjectKey)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
}

func dependencySeedLegacyClaim(t *testing.T, store *graph.Store, c domain.ClaimRevision) {
	t.Helper()
	data, err := json.Marshal(c)
	require.NoError(t, err)
	memoryMutation(t, store, `CREATE (h:Claim {id:$claim}),(c:ClaimRevision {id:$revision,claimID:$claim,projectID:$project,taskID:$task,sessionID:$session,clientID:$client,data:$data,text:$text,kind:$kind,status:'candidate',valid:false,lifecycleIndexed:true,ingestedAt:$now}) CREATE (h)-[:HEAD]->(c)`, map[string]any{"claim": c.ClaimID, "revision": c.Revision, "project": c.Scope.ProjectID, "task": c.Scope.TaskID, "session": c.Scope.SessionID, "client": c.SubmittedBy, "data": string(data), "text": c.Text, "kind": c.Kind, "now": time.Now().UTC().UnixNano()})
}
func dependencyAssertEdge(t *testing.T, store *graph.Store, from, to string) {
	t.Helper()
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		n, e := store.Unwrap(tx)
		if e != nil {
			return e
		}
		result, e := n.Run(ctx, `MATCH (:ClaimRevision {id:$from})-[:DEPENDS_ON]->(:ClaimRevision {id:$to}) RETURN count(*)`, map[string]any{"from": from, "to": to})
		if e != nil {
			return e
		}
		row, e := result.Single(ctx)
		require.NoError(t, e)
		assert.Equal(t, int64(1), row.Values[0])
		return nil
	}))
}
func dependencyAssertClaims(t *testing.T, store *graph.Store, ids []string, valid, scrubbed bool) {
	t.Helper()
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		n, e := store.Unwrap(tx)
		if e != nil {
			return e
		}
		result, e := n.Run(ctx, `MATCH (c:ClaimRevision) WHERE c.id IN $ids RETURN c.id,c.status,c.valid,c.text,c.data ORDER BY c.id`, map[string]any{"ids": ids})
		if e != nil {
			return e
		}
		rows, e := result.Collect(ctx)
		require.NoError(t, e)
		require.Len(t, rows, len(ids))
		for _, row := range rows {
			assert.Equal(t, "invalidated", row.Values[1], "revision %s", row.Values[0])
			assert.Equal(t, valid, row.Values[2], "revision %s", row.Values[0])
			if scrubbed {
				assert.Nil(t, row.Values[3], "text must be scrubbed for %s", row.Values[0])
				assert.Nil(t, row.Values[4], "immutable JSON must be scrubbed for %s", row.Values[0])
			}
		}
		return nil
	}))
}
func dependencyAssertUnchanged(t *testing.T, store *graph.Store, want domain.ClaimRevision) {
	t.Helper()
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		n, e := store.Unwrap(tx)
		if e != nil {
			return e
		}
		result, e := n.Run(ctx, `MATCH (c:ClaimRevision {id:$id}) RETURN c.status,c.text,c.data,c.deletionOperation`, map[string]any{"id": want.Revision})
		if e != nil {
			return e
		}
		row, e := result.Single(ctx)
		require.NoError(t, e)
		assert.Equal(t, "candidate", row.Values[0])
		assert.Equal(t, want.Text, row.Values[1])
		assert.Nil(t, row.Values[3])
		var retained domain.ClaimRevision
		data, ok := row.Values[2].(string)
		require.True(t, ok)
		require.NoError(t, json.Unmarshal([]byte(data), &retained))
		assert.Equal(t, want, retained)
		return nil
	}))
}

type dependencyGraphSnapshot struct {
	label string
	rows  []map[string]any
}

func dependencySnapshotGraph(t *testing.T, store *graph.Store) []dependencyGraphSnapshot {
	t.Helper()
	snapshots := []dependencyGraphSnapshot{}
	for _, label := range []string{"ClaimRevision", "Artifact", "Publication", "PreparedDerivation", "ExcerptLineage"} {
		snapshot := dependencyGraphSnapshot{label: label}
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			n, e := store.Unwrap(tx)
			if e != nil {
				return e
			}
			result, e := n.Run(ctx, "MATCH (n:"+label+") RETURN n.id,properties(n)", nil)
			if e != nil {
				return e
			}
			rows, e := result.Collect(ctx)
			if e != nil {
				return e
			}
			for _, row := range rows {
				snapshot.rows = append(snapshot.rows, map[string]any{"id": row.Values[0], "properties": row.Values[1]})
			}
			return nil
		}))
		snapshots = append(snapshots, snapshot)
	}
	return snapshots
}
func dependencyRestoreGraph(t *testing.T, store *graph.Store, snapshots []dependencyGraphSnapshot, operation string, barrier domain.JournalBarrier, repo *repository.LifecycleRepository) {
	t.Helper()
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		n, e := store.Unwrap(tx)
		if e != nil {
			return e
		}
		for _, snapshot := range snapshots {
			result, e := n.Run(ctx, "UNWIND $rows AS item MATCH (n:"+snapshot.label+" {id:item.id}) SET n=item.properties", map[string]any{"rows": snapshot.rows})
			if e != nil {
				return e
			}
			if _, e = result.Consume(ctx); e != nil {
				return e
			}
		}
		for _, query := range []string{
			`MATCH (o:DeletionOperation {id:$operation}) DETACH DELETE o`,
			`MATCH (j:Job {kind:'lifecycle.purge'}) DETACH DELETE j`,
			`MATCH (t:Tombstone) DETACH DELETE t`,
			`MATCH (t:SourceTombstone {projectID:'demo'}) DETACH DELETE t`,
			`MATCH (c:ClaimRevision {projectID:'demo'})-[r:DEPENDS_ON]->() DELETE r`,
			`MATCH (c:ClaimRevision {projectID:'demo'}) REMOVE c.dependencyProjectionVersion`,
		} {
			result, e := n.Run(ctx, query, map[string]any{"operation": operation})
			if e != nil {
				return e
			}
			if _, e = result.Consume(ctx); e != nil {
				return e
			}
		}
		return repo.SetBarrier(ctx, tx, barrier)
	}))
}

package repository_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
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

func memoryMutation(t *testing.T, store *graph.Store, q string, p map[string]any) {
	t.Helper()
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		n, e := store.Unwrap(tx)
		if e != nil {
			return e
		}
		r, e := n.Run(ctx, q, p)
		if e != nil {
			return e
		}
		_, e = r.Consume(ctx)
		return e
	}))
}
func publishFixture(t *testing.T, store *graph.Store, c domain.ClaimRevision, support []string, sharing, generalized string) {
	t.Helper()
	data, e := json.Marshal(c)
	require.NoError(t, e)
	if support == nil {
		support = []string{}
	}
	memoryMutation(t, store, `CREATE (c:ClaimRevision {id:$id,claimID:$claim,data:$data,status:$status,valid:true,projectID:$project,taskID:$task,clientID:'fixture',text:$text,kind:$kind}) CREATE (:Publication {id:$publication,claimRevision:$id,revision:'fixture-publication',policyRevision:'p1',valid:true,sharingScope:$sharing,supportRevisions:$support,generalizedText:$generalized}) WITH c MATCH (o:OwnerState {id:'owner'}) SET o.revisionEpoch=o.revisionEpoch+1`, map[string]any{"id": c.Revision, "claim": c.ClaimID, "data": string(data), "status": string(c.Status), "project": c.Scope.ProjectID, "task": c.Scope.TaskID, "text": c.Text, "kind": c.Kind, "publication": "pub-" + c.Revision, "sharing": sharing, "support": support, "generalized": generalized})
	for _, ref := range c.Evidence {
		memoryMutation(t, store, `MATCH (c:ClaimRevision {id:$id}),(a:Artifact {id:$artifact}) CREATE (c)-[:EVIDENCE {digest:$digest,locator:$locator}]->(a)`, map[string]any{"id": c.Revision, "artifact": ref.ArtifactID, "digest": ref.Digest, "locator": ref.Locator})
	}
}
func TestRetrievalRealGraphAndUpload(t *testing.T) {
	store, jobs, _ := jobsGraph(t)
	objects := captureS3(t)
	evidence := repository.NewEvidenceRepository(store)
	tasks := repository.NewCheckpointRepository(store)
	retrievalRepo := repository.NewRetrievalRepository(store)
	memoryRepo := repository.NewMemoryRepository(store)
	capture := service.NewCaptureService(store, evidence, jobs, objects)
	identity := service.NewIdentityService(store, repository.NewIdentityRepository(store), gateway.NostrIdentity{})
	collector, e := identity.Setup(t.Context(), domain.OwnerIdentity{ID: "owner", PublicKey: "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"}, service.EnrollInput{ClientID: "collector", Role: domain.RoleCollector, ProjectIDs: []string{"demo", "secret"}, Capabilities: []string{"artifacts:write"}})
	require.NoError(t, e)
	owner := domain.Principal{OwnerID: "owner", Role: domain.RoleOwner}
	agent, e := identity.Enroll(t.Context(), owner, service.EnrollInput{ClientID: "agent", Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: []string{"retrieval:read", "evidence:read", "candidate:write", "checkpoint:write"}})
	require.NoError(t, e)
	other, e := identity.Enroll(t.Context(), owner, service.EnrollInput{ClientID: "other", Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: []string{"retrieval:read", "evidence:read", "candidate:write", "checkpoint:write"}})
	require.NoError(t, e)
	seedCapturePolicy(t, store, domain.Policy{Revision: "p1", SpoolLimitBytes: service.MaxArtifactBytes})
	policyData, e := json.Marshal(domain.Policy{Revision: "p1", SpoolLimitBytes: service.MaxArtifactBytes})
	require.NoError(t, e)
	memoryMutation(t, store, `CREATE (:CapturePolicy {projectID:'secret',data:$data})`, map[string]any{"data": string(policyData)})
	retrieval, e := service.NewRetrievalService(store, retrievalRepo, tasks, evidence, objects, bytes.Repeat([]byte{1}, 32))
	require.NoError(t, e)
	memory := service.NewMemoryService(store, memoryRepo, retrievalRepo, evidence, capture)
	checkpoints := service.NewCheckpointService(store, tasks, retrievalRepo)
	upload := func(scope domain.Scope, text string) domain.Artifact {
		meta := service.ArtifactInput{Scope: scope, Digest: service.TokenDigest(text), Bytes: int64(len(text)), PolicyRevision: "p1", SourceLocator: "synthetic.txt#bytes=0-" + fmt.Sprint(len(text)), SourcePath: "synthetic.txt"}
		a, e := capture.Stage(t.Context(), collector.Principal, meta, strings.NewReader(text))
		require.NoError(t, e)
		_, e = capture.Complete(t.Context(), collector.Principal, a.ID, a.Digest)
		require.NoError(t, e)
		return a
	}
	ref := func(a domain.Artifact) domain.EvidenceRef {
		return domain.EvidenceRef{ArtifactID: a.ID, Digest: a.Digest, Locator: a.SourceLocator, SourceRevision: a.Generation, Origin: domain.Origin{ClientID: "forged", Trust: domain.TrustOwnerAuthenticated}}
	}
	taskScope := domain.Scope{ProjectID: "demo", TaskID: "resume", SessionID: "session"}
	taskArtifact := upload(taskScope, "synthetic retry result")
	input := domain.CandidateInput{ClaimID: "task-finding", Text: "The synthetic retry completed.", Kind: "finding", EntityIDs: []string{"retry-api"}, Scope: taskScope, Evidence: []domain.EvidenceRef{ref(taskArtifact)}}
	t.Run("actual upload candidate task working acceptance", func(t *testing.T) {
		c, e := memory.Submit(t.Context(), agent.Principal, input)
		require.NoError(t, e)
		assert.Equal(t, domain.ClaimCandidate, c.Status)
		assert.Equal(t, domain.TrustSubmitted, c.Evidence[0].Origin.Trust)
		assert.Equal(t, "collector", c.Evidence[0].Origin.ClientID)
		shared, e := retrieval.Retrieve(t.Context(), agent.Principal, domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "demo"}, Mode: domain.RetrievalEvidence})
		require.NoError(t, e)
		assert.True(t, shared.NoResults)
		working, e := retrieval.Retrieve(t.Context(), agent.Principal, domain.RetrievalRequest{Scope: taskScope, Mode: domain.RetrievalWorking, MaxDepth: 2})
		require.NoError(t, e)
		require.Len(t, working.Chunks, 1)
		assert.Equal(t, c.Revision, working.Chunks[0].Revision)
		assert.Nil(t, working.Chunks[0].VerificationOutcome)
		entityResult, e := retrieval.Retrieve(t.Context(), agent.Principal, domain.RetrievalRequest{Scope: taskScope, Mode: domain.RetrievalWorking, EntityIDs: []string{"retry-api"}})
		require.NoError(t, e)
		require.Len(t, entityResult.Chunks, 1)
		assert.Equal(t, taskArtifact.SourceLocator, working.Chunks[0].Evidence[0].Locator)
		_, e = retrieval.Fetch(t.Context(), agent.Principal, taskArtifact.ID, taskArtifact.SourceLocator, 16384)
		assert.ErrorIs(t, e, domain.ErrForbidden)
		_, e = memory.Submit(t.Context(), other.Principal, input)
		assert.ErrorIs(t, e, domain.ErrForbidden)
		_, e = retrieval.Retrieve(t.Context(), other.Principal, domain.RetrievalRequest{Scope: taskScope, Mode: domain.RetrievalWorking})
		assert.ErrorIs(t, e, domain.ErrForbidden)
		input.ExpectedRevision = c.Revision
		input.Text = "Updated task finding"
		updated, e := memory.Submit(t.Context(), agent.Principal, input)
		require.NoError(t, e)
		assert.NotEqual(t, c.Revision, updated.Revision)
		_, e = memory.Submit(t.Context(), agent.Principal, input)
		assert.ErrorIs(t, e, domain.ErrConflict)
	})

	t.Run("evidence free hypothesis stays task local", func(t *testing.T) {
		c, e := memory.Submit(t.Context(), agent.Principal, domain.CandidateInput{ClaimID: "task-hypothesis", Text: "A synthetic task hypothesis", Kind: "hypothesis", Scope: taskScope})
		require.NoError(t, e)
		assert.Empty(t, c.Evidence)
		request := domain.RetrievalRequest{Scope: taskScope, Query: c.ClaimID, Mode: domain.RetrievalWorking}
		got, e := retrieval.Retrieve(t.Context(), agent.Principal, request)
		require.NoError(t, e)
		require.Len(t, got.Chunks, 1)
		assert.Equal(t, domain.ClaimCandidate, got.Chunks[0].ClaimStatus)
		assert.Equal(t, domain.TrustSubmitted, got.Chunks[0].OriginTrust)
		assert.Nil(t, got.Chunks[0].VerificationOutcome)
		request.Mode = domain.RetrievalEvidence
		request.Scope = domain.Scope{ProjectID: "demo"}
		got, e = retrieval.Retrieve(t.Context(), agent.Principal, request)
		require.NoError(t, e)
		assert.True(t, got.NoResults)
	})
	t.Run("checkpoint atomic latest get and scope resume", func(t *testing.T) {
		c, e := checkpoints.Put(t.Context(), agent.Principal, "", domain.Checkpoint{ID: "cp", Scope: taskScope, Objective: "Resume synthetic work", Pending: []string{"Inspect retry"}, EvidenceRevisionIDs: []string{"private-claim"}})
		require.NoError(t, e)
		got, e := checkpoints.Get(t.Context(), agent.Principal, c.ID)
		require.NoError(t, e)
		assert.Equal(t, c.Revision, got.Revision)
		assert.Empty(t, got.EvidenceRevisionIDs)
		assert.True(t, got.EvidenceReferencesOmitted)
		_, e = checkpoints.Get(t.Context(), other.Principal, c.ID)
		assert.ErrorIs(t, e, domain.ErrForbidden)
		resume, e := retrieval.Retrieve(t.Context(), agent.Principal, domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "demo", TaskID: "resume"}, Mode: domain.RetrievalOrientation})
		require.NoError(t, e)
		require.NotNil(t, resume.TaskCheckpoint)
		assert.Equal(t, c.Revision, resume.TaskCheckpoint.Checkpoint.Revision)
		assert.Empty(t, resume.Chunks)
		var wg sync.WaitGroup
		results := make(chan error, 2)
		for range 2 {
			wg.Go(func() { _, err := checkpoints.Put(t.Context(), agent.Principal, c.Revision, c); results <- err })
		}
		wg.Wait()
		close(results)
		success, conflicts := 0, 0
		for err := range results {
			switch err {
			case nil:
				success++
			case domain.ErrConflict:
				conflicts++
			default:
				t.Errorf("unexpected %v", err)
			}
		}
		assert.Equal(t, 1, success)
		assert.Equal(t, 1, conflicts)
		latest, e := checkpoints.Latest(t.Context(), agent.Principal, domain.Scope{ProjectID: "demo", TaskID: "resume"})
		require.NoError(t, e)
		assert.NotEqual(t, c.Revision, latest.Revision)
	})
	projectArtifact := upload(domain.Scope{ProjectID: "demo"}, "synthetic project evidence <猫>")
	projectRef := ref(projectArtifact)
	projectRef.Origin = domain.Origin{ClientID: "collector", Trust: domain.TrustSubmitted}
	base := domain.ClaimRevision{ClaimID: "verified", Revision: "verified-a", Text: "Project finding", Kind: "finding", Scope: domain.Scope{ProjectID: "demo"}, Status: domain.ClaimVerified, Evidence: []domain.EvidenceRef{projectRef}}
	publishFixture(t, store, base, nil, "project", "")
	t.Run("verified fetch eligibility unavailable and invalidated dependency", func(t *testing.T) {
		request := domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "demo"}, Query: "Project finding", Mode: domain.RetrievalEvidence}
		got, e := retrieval.Retrieve(t.Context(), agent.Principal, request)
		require.NoError(t, e)
		require.Len(t, got.Chunks, 1)
		fetched, e := retrieval.Fetch(t.Context(), agent.Principal, projectArtifact.ID, projectArtifact.SourceLocator, 16384)
		require.NoError(t, e)
		require.Len(t, fetched.Chunks, 1)
		assert.Equal(t, "synthetic project evidence <猫>", fetched.Chunks[0].Text)
		for _, phase := range []string{"open", "read", "close"} {
			t.Run("transient "+phase, func(t *testing.T) {
				flaky, e := service.NewRetrievalService(store, retrievalRepo, tasks, evidence, retrievalTransportStore{ObjectStore: objects, phase: phase}, bytes.Repeat([]byte{1}, 32))
				require.NoError(t, e)
				_, e = flaky.Fetch(t.Context(), agent.Principal, projectArtifact.ID, projectArtifact.SourceLocator, 16384)
				assert.ErrorIs(t, e, domain.ErrUnavailable)
				assert.True(t, captureArtifact(t, store, evidence, projectArtifact.ID).ObjectAvailable)
				_, e = retrieval.Fetch(t.Context(), agent.Principal, projectArtifact.ID, projectArtifact.SourceLocator, 16384)
				require.NoError(t, e)
			})
		}
		unknown, e := service.NewRetrievalService(store, retrievalRepo, tasks, evidence, retrievalTransportStore{ObjectStore: objects, phase: "open", after: func() {
			memoryMutation(t, store, `MATCH (a:Artifact {id:$id}) SET a.available=false`, map[string]any{"id": projectArtifact.ID})
		}}, bytes.Repeat([]byte{1}, 32))
		require.NoError(t, e)
		_, e = unknown.Fetch(t.Context(), agent.Principal, projectArtifact.ID, projectArtifact.SourceLocator, 16384)
		assert.ErrorIs(t, e, domain.ErrUnavailable)
		assert.False(t, captureArtifact(t, store, evidence, projectArtifact.ID).ObjectAvailable)
		memoryMutation(t, store, `MATCH (a:Artifact {id:$id}) SET a.available=true`, map[string]any{"id": projectArtifact.ID})

		_, e = retrieval.Fetch(t.Context(), agent.Principal, projectArtifact.ID, "wrong", 16384)
		assert.ErrorIs(t, e, domain.ErrForbidden)
		_, e = retrieval.Fetch(t.Context(), agent.Principal, projectArtifact.ID, projectArtifact.SourceLocator, 1)
		assert.ErrorIs(t, e, domain.ErrBudget)
		memoryMutation(t, store, `MATCH (a:Artifact {id:$id}) SET a.available=false`, map[string]any{"id": projectArtifact.ID})
		_, e = retrieval.Retrieve(t.Context(), agent.Principal, request)
		assert.ErrorIs(t, e, domain.ErrUnavailable)
		memoryMutation(t, store, `MATCH (a:Artifact {id:$id}) SET a.available=true`, map[string]any{"id": projectArtifact.ID})
		dependent := base
		dependent.ClaimID = "dependent"
		dependent.Revision = "dependent-r"
		dependent.Text = "dependent target"
		dependent.DependsOn = []string{base.Revision}
		publishFixture(t, store, dependent, []string{base.Revision}, "project", "")
		memoryMutation(t, store, `MATCH (c:ClaimRevision {id:$id}) SET c.status='invalidated',c.valid=false`, map[string]any{"id": base.Revision})
		request.Query = "dependent target"
		got, e = retrieval.Retrieve(t.Context(), agent.Principal, request)
		require.NoError(t, e)
		assert.True(t, got.NoResults)
		memoryMutation(t, store, `MATCH (c:ClaimRevision {id:$id}) SET c.status='verified',c.valid=true`, map[string]any{"id": base.Revision})
	})
	t.Run("cross project generalized lesson hides restricted metadata", func(t *testing.T) {
		a := upload(domain.Scope{ProjectID: "secret"}, "secret source bytes")
		r := ref(a)
		r.Origin = domain.Origin{ClientID: "collector", Trust: domain.TrustSubmitted}
		lesson := domain.ClaimRevision{ClaimID: "secret-claim", Revision: "lesson-safe", Text: "Generalized retry lesson", Kind: "lesson", Scope: domain.Scope{ProjectID: "secret"}, Status: domain.ClaimVerified, Evidence: []domain.EvidenceRef{r}}
		publishFixture(t, store, lesson, nil, "cross_project", lesson.Text)
		v, e := retrieval.Retrieve(t.Context(), agent.Principal, domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "demo"}, Query: "Generalized retry lesson", Mode: domain.RetrievalEvidence})
		require.NoError(t, e)
		require.Len(t, v.Chunks, 1)
		wire, e := domain.RetrievalWire(v)
		require.NoError(t, e)
		assert.NotContains(t, string(wire), a.ID)
		assert.NotContains(t, string(wire), "secret")
		assert.Empty(t, v.Chunks[0].Evidence)
		_, e = retrieval.Fetch(t.Context(), agent.Principal, a.ID, a.SourceLocator, 16384)
		assert.ErrorIs(t, e, domain.ErrForbidden)
	})

	t.Run("cross project target applicability and grants", func(t *testing.T) {
		broad, e := identity.Enroll(t.Context(), owner, service.EnrollInput{ClientID: "broad-agent", Role: domain.RoleAgent, ProjectIDs: []string{"demo", "secret"}, Capabilities: []string{"retrieval:read", "evidence:read"}})
		require.NoError(t, e)
		a := upload(domain.Scope{ProjectID: "secret"}, "conditional source bytes")
		source := ref(a)
		source.Origin = domain.Origin{ClientID: "collector", Trust: domain.TrustSubmitted}
		source.Applicability = domain.Applicability{Environment: "hidden-support-environment"}
		past := time.Now().UTC().Add(-time.Hour)
		future := time.Now().UTC().Add(time.Hour)
		for _, tc := range []struct {
			name            string
			target, request domain.Applicability
			want            int
		}{{"expired", domain.Applicability{ValidUntil: &past}, domain.Applicability{}, 0}, {"future", domain.Applicability{ValidFrom: &future}, domain.Applicability{}, 0}, {"version mismatch", domain.Applicability{Versions: map[string]string{"runtime": "1"}}, domain.Applicability{Versions: map[string]string{"runtime": "2"}}, 0}, {"environment mismatch", domain.Applicability{Environment: "prod"}, domain.Applicability{Environment: "test"}, 0}, {"repository mismatch", domain.Applicability{RepositoryRevision: "public-target-r1"}, domain.Applicability{RepositoryRevision: "public-target-r2"}, 0}, {"conditional valid", domain.Applicability{Environment: "public-fixture", Versions: map[string]string{"runtime": "1", "unknown": ""}, ValidFrom: &past, ValidUntil: &future}, domain.Applicability{Environment: "public-fixture", Versions: map[string]string{"runtime": "1"}}, 1}} {
			t.Run(tc.name, func(t *testing.T) {
				target := domain.ClaimRevision{ClaimID: "private-target-" + tc.name, Revision: "conditional-" + tc.name, Text: "Shareable conditional lesson " + tc.name, Kind: "lesson", Scope: domain.Scope{ProjectID: "secret"}, Status: domain.ClaimVerified, Evidence: []domain.EvidenceRef{source}, Applicability: tc.target}
				publishFixture(t, store, target, nil, "cross_project", target.Text)
				request := domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "demo"}, Query: target.Revision, Applicability: tc.request, Mode: domain.RetrievalEvidence}
				limited, e := retrieval.Retrieve(t.Context(), agent.Principal, request)
				require.NoError(t, e)
				assert.Len(t, limited.Chunks, tc.want)
				expanded, e := retrieval.Retrieve(t.Context(), broad.Principal, request)
				require.NoError(t, e)
				assert.Equal(t, limited, expanded)
				if tc.want == 1 {
					wire, e := domain.RetrievalWire(limited)
					require.NoError(t, e)
					assert.Contains(t, string(wire), `"applicability":`)
					assert.Contains(t, string(wire), "public-fixture")
					assert.Contains(t, string(wire), `"unknown":""`)
					assert.Contains(t, string(wire), "valid_from")
					assert.Contains(t, string(wire), "valid_until")
					assert.NotContains(t, string(wire), "hidden-support-environment")
					assert.NotContains(t, string(wire), a.ID)
					own := request
					own.Scope.ProjectID = "secret"
					owned, e := retrieval.Retrieve(t.Context(), broad.Principal, own)
					require.NoError(t, e)
					require.Len(t, owned.Chunks, 1)
					assert.Equal(t, target.ClaimID, owned.Chunks[0].RecordID)
					assert.Len(t, owned.Chunks[0].Evidence, 1)
				}
			})
		}
	})

	t.Run("complete hidden task support closure and record bounds", func(t *testing.T) {
		supportRef := ref(taskArtifact)
		supportRef.Origin = domain.Origin{ClientID: "collector", Trust: domain.TrustSubmitted}
		support := domain.ClaimRevision{ClaimID: "hidden-task-support", Revision: "hidden-task-support-r", Text: "task source finding", Kind: "finding", Scope: taskScope, Status: domain.ClaimVerified, Evidence: []domain.EvidenceRef{supportRef}}
		publishFixture(t, store, support, nil, "project", "")
		target := base
		target.ClaimID = "target-with-task-support"
		target.Revision = "target-with-task-support-r"
		target.Text = "Allowed project excerpt"
		target.DependsOn = []string{support.Revision}
		publishFixture(t, store, target, []string{support.Revision}, "project", "")
		request := domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "demo"}, Query: target.Revision, Mode: domain.RetrievalEvidence, MaxDepth: 2}
		got, e := retrieval.Retrieve(t.Context(), agent.Principal, request)
		require.NoError(t, e)
		require.Len(t, got.Chunks, 1)
		wire, e := domain.RetrievalWire(got)
		require.NoError(t, e)
		assert.NotContains(t, string(wire), support.Revision)
		assert.NotContains(t, string(wire), taskArtifact.ID)
		memoryMutation(t, store, `MATCH (c:ClaimRevision {id:$id}) SET c.valid=false`, map[string]any{"id": support.Revision})
		got, e = retrieval.Retrieve(t.Context(), agent.Principal, request)
		require.NoError(t, e)
		assert.True(t, got.NoResults)
		memoryMutation(t, store, `MATCH (c:ClaimRevision {id:$id}) SET c.valid=true`, map[string]any{"id": support.Revision})
		tooLong := make([]string, 1001)
		for i := range tooLong {
			tooLong[i] = fmt.Sprint(i)
		}
		capTarget := base
		capTarget.ClaimID = "over-cap"
		capTarget.Revision = "over-cap-r"
		publishFixture(t, store, capTarget, tooLong, "project", "")
		request.Query = capTarget.Revision
		got, e = retrieval.Retrieve(t.Context(), agent.Principal, request)
		require.NoError(t, e)
		assert.True(t, got.NoResults)
		memoryMutation(t, store, `CREATE (:Tombstone {id:$id})`, map[string]any{"id": target.Revision})
		request.Query = target.Revision
		got, e = retrieval.Retrieve(t.Context(), agent.Principal, request)
		require.NoError(t, e)
		assert.True(t, got.NoResults)
		for i := range 80 {
			c := base
			c.ClaimID = fmt.Sprintf("fanout-%03d", i)
			c.Revision = c.ClaimID
			c.Text = "Filtered neighbor mentioning " + base.Revision
			c.Status = domain.ClaimInvalidated
			publishFixture(t, store, c, nil, "project", "")
			memoryMutation(t, store, `MATCH (a:ClaimRevision {id:$seed}),(b:ClaimRevision {id:$id}) CREATE (a)-[:RELATED]->(b)`, map[string]any{"seed": base.Revision, "id": c.Revision})
		}
		request.Query = base.Revision
		got, e = retrieval.Retrieve(t.Context(), agent.Principal, request)
		require.NoError(t, e)
		assert.Equal(t, 50, got.Visited)
		require.Len(t, got.Chunks, 1)
		assert.True(t, got.Truncated)
	})
	t.Run("continuation epoch and task head changes", func(t *testing.T) {
		for i := range 6 {
			c := base
			c.ClaimID = fmt.Sprintf("page-%d", i)
			c.Revision = c.ClaimID
			c.Text = "pagination " + strings.Repeat("x", 300)
			publishFixture(t, store, c, nil, "project", "")
		}
		request := domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "demo", TaskID: "resume"}, Query: "pagination", Mode: domain.RetrievalOrientation, BudgetBytes: 1800}
		first, e := retrieval.Retrieve(t.Context(), agent.Principal, request)
		require.NoError(t, e)
		require.NotEmpty(t, first.Continuation)
		request.Continuation = first.Continuation
		latest, e := checkpoints.Latest(t.Context(), agent.Principal, request.Scope)
		require.NoError(t, e)
		_, e = checkpoints.Put(t.Context(), agent.Principal, latest.Revision, latest)
		require.NoError(t, e)
		_, e = retrieval.Retrieve(t.Context(), agent.Principal, request)
		assert.ErrorIs(t, e, domain.ErrStale)
		request.Continuation = ""
		request.Scope.TaskID = ""
		first, e = retrieval.Retrieve(t.Context(), agent.Principal, request)
		require.NoError(t, e)
		require.NotEmpty(t, first.Continuation)
		request.Continuation = first.Continuation
		require.NoError(t, identity.Revoke(t.Context(), owner, other.ClientID))
		fresh, e := identity.Authenticate(t.Context(), agent.Bearer)
		require.NoError(t, e)
		_, e = retrieval.Retrieve(t.Context(), fresh, request)
		assert.ErrorIs(t, e, domain.ErrStale)
		_, e = retrieval.Fetch(t.Context(), other.Principal, projectArtifact.ID, projectArtifact.SourceLocator, 16384)
		assert.ErrorIs(t, e, domain.ErrUnauthorized)
	})
}

type retrievalTransportStore struct {
	service.ObjectStore
	phase string
	after func()
}

func (o retrievalTransportStore) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if o.after != nil {
		o.after()
	}
	if o.phase == "open" {
		return nil, domain.ErrUnavailable
	}
	body, e := o.ObjectStore.Open(ctx, key)
	if e != nil {
		return nil, e
	}
	return &retrievalTransportReader{body: body, phase: o.phase}, nil
}

type retrievalTransportReader struct {
	body  io.ReadCloser
	phase string
	read  bool
}

func (r *retrievalTransportReader) Read(p []byte) (int, error) {
	if r.phase == "read" {
		if r.read {
			return 0, domain.ErrUnavailable
		}
		r.read = true
		return r.body.Read(p[:min(3, len(p))])
	}
	return r.body.Read(p)
}
func (r *retrievalTransportReader) Close() error {
	e := r.body.Close()
	if r.phase == "close" {
		return domain.ErrUnavailable
	}
	return e
}

// The artifact citation query must narrow on scope, candidate ownership and
// publication status before its bounded record selection. Irrelevant links do
// not consume the authorization candidate budget.
func TestFetchCitationSelectionUsesEligibleScopeBeforeLimit(t *testing.T) {
	store, jobs, _ := jobsGraph(t)
	objects := captureS3(t)
	evidence := repository.NewEvidenceRepository(store)
	repo := repository.NewRetrievalRepository(store)
	capture := service.NewCaptureService(store, evidence, jobs, objects)
	identity := service.NewIdentityService(store, repository.NewIdentityRepository(store), gateway.NostrIdentity{})
	collector, err := identity.Setup(t.Context(), domain.OwnerIdentity{ID: "owner", PublicKey: "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"}, service.EnrollInput{ClientID: "collector", Role: domain.RoleCollector, ProjectIDs: []string{"demo"}, Capabilities: []string{"artifacts:write"}})
	require.NoError(t, err)
	agent, err := identity.Enroll(t.Context(), domain.Principal{OwnerID: "owner", Role: domain.RoleOwner}, service.EnrollInput{ClientID: "agent", Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: []string{"retrieval:read", "evidence:read", "candidate:write"}})
	require.NoError(t, err)
	seedCapturePolicy(t, store, domain.Policy{Revision: "p1", SpoolLimitBytes: service.MaxArtifactBytes})
	svc, err := service.NewRetrievalService(store, repo, nil, evidence, objects, bytes.Repeat([]byte{1}, 32))
	require.NoError(t, err)
	memory := service.NewMemoryService(store, repository.NewMemoryRepository(store), repo, evidence, capture)
	for _, task := range []bool{false, true} {
		t.Run(fmt.Sprintf("working=%t", task), func(t *testing.T) {
			scope := domain.Scope{ProjectID: "demo"}
			if task {
				scope.TaskID = "task"
				scope.SessionID = "session"
			}
			text := fmt.Sprintf("citation fixture working=%t", task)
			meta := service.ArtifactInput{Scope: scope, Digest: service.TokenDigest(text), Bytes: int64(len(text)), PolicyRevision: "p1", SourceLocator: "source-" + fmt.Sprint(task), SourcePath: "synthetic.txt"}
			a, e := capture.Stage(t.Context(), collector.Principal, meta, strings.NewReader(text))
			require.NoError(t, e)
			_, e = capture.Complete(t.Context(), collector.Principal, a.ID, a.Digest)
			require.NoError(t, e)
			// Source revision is an opaque source identifier, not the internal
			// storage generation, which is absent from the artifact API.
			ref := domain.EvidenceRef{ArtifactID: a.ID, Digest: a.Digest, Locator: a.SourceLocator, SourceRevision: "repository-revision", Origin: domain.Origin{ClientID: "collector", Trust: domain.TrustSubmitted}}
			if task {
				_, e = memory.Submit(t.Context(), agent.Principal, domain.CandidateInput{ClaimID: "zz-task-citation", Text: "owned", Kind: "finding", Scope: scope, Evidence: []domain.EvidenceRef{ref}})
				require.NoError(t, e)
			} else {
				publishFixture(t, store, domain.ClaimRevision{ClaimID: "zz-publication", Revision: "zz-publication", Text: "eligible", Kind: "finding", Scope: scope, Status: domain.ClaimVerified, Evidence: []domain.EvidenceRef{ref}}, nil, "project", "")
			}
			// Unrelated candidate citations sort before the eligible revision. In
			// working mode they have the right task but the wrong submitting client.
			for i := range 55 {
				id := fmt.Sprintf("!unrelated-%t-%03d", task, i)
				claim := domain.ClaimRevision{ClaimID: id, Revision: id, Scope: scope, Status: domain.ClaimCandidate, Evidence: []domain.EvidenceRef{ref}}
				data, e := json.Marshal(claim)
				require.NoError(t, e)
				memoryMutation(t, store, `MATCH (a:Artifact {id:$artifact}) CREATE (c:ClaimRevision {id:$id,claimID:$id,data:$data,status:'candidate',valid:false,projectID:'demo',taskID:$task,clientID:'other',text:'irrelevant',kind:'finding'}) CREATE (c)-[:EVIDENCE {digest:$digest,locator:$locator}]->(a)`, map[string]any{"id": id, "data": string(data), "task": scope.TaskID, "artifact": a.ID, "digest": a.Digest, "locator": a.SourceLocator})
			}
			request := domain.FetchRequest{ArtifactID: a.ID, Locator: a.SourceLocator, BudgetBytes: 16384}
			if task {
				request.Mode = domain.FetchWorking
				request.Scope = scope
			}
			got, e := svc.FetchEvidence(t.Context(), agent.Principal, request)
			require.NoError(t, e)
			require.Len(t, got.Chunks, 1)
			assert.Equal(t, text, got.Chunks[0].Text)

			// Same-scope/owner citations with invalid dependencies are plausible
			// until full eligibility is checked. The scan stays bounded and reports
			// incomplete rather than falsely claiming that no citation exists.
			if task {
				memoryMutation(t, store, `MATCH (c:ClaimRevision {claimID:'zz-task-citation'}) DETACH DELETE c`, nil)
			} else {
				memoryMutation(t, store, `MATCH (c:ClaimRevision {id:'zz-publication'}) DETACH DELETE c`, nil)
			}
			dataRows := make([]map[string]any, 50)
			for i := range dataRows {
				id := fmt.Sprintf("bounded-%t-%03d", task, i)
				claim := domain.ClaimRevision{ClaimID: id, Revision: id, Scope: scope, Evidence: []domain.EvidenceRef{ref}, DependsOn: []string{"missing-dependency"}}
				if task {
					claim.Status = domain.ClaimCandidate
					claim.SubmittedBy = agent.ClientID
				} else {
					claim.Status = domain.ClaimVerified
				}
				data, err := json.Marshal(claim)
				require.NoError(t, err)
				dataRows[i] = map[string]any{"id": id, "data": string(data)}
			}
			status := string(domain.ClaimVerified)
			if task {
				status = string(domain.ClaimCandidate)
			}
			memoryMutation(t, store, `MATCH (a:Artifact {id:$artifact}) UNWIND $rows AS row CREATE (c:ClaimRevision {id:row.id,claimID:row.id,data:row.data,status:$status,valid:true,projectID:'demo',taskID:$task,clientID:$client}) CREATE (c)-[:EVIDENCE {digest:$digest,locator:$locator}]->(a)`, map[string]any{"artifact": a.ID, "rows": dataRows, "status": status, "task": scope.TaskID, "client": agent.ClientID, "digest": a.Digest, "locator": a.SourceLocator})
			if !task {
				memoryMutation(t, store, `UNWIND $rows AS row CREATE (:Publication {id:row.id,claimRevision:row.id,valid:true,sharingScope:'project',supportRevisions:['missing-dependency']})`, map[string]any{"rows": dataRows})
			}
			got, e = svc.FetchEvidence(t.Context(), agent.Principal, request)
			assert.ErrorIs(t, e, domain.ErrIncomplete)
			assert.Empty(t, got.Chunks)
		})
	}
}

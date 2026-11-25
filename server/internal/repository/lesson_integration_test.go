package repository_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lessonGitRepo(t *testing.T, name, body string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	run("add", "-A")
	run("commit", "-q", "-m", "fixture")
	return dir, run("rev-parse", "HEAD")
}

func TestLessonsAndAttestedVerificationOnRealGraph(t *testing.T) {
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
	outsider, err := identity.EnrollHost(t.Context(), service.EnrollInput{ClientID: "outsider", Role: domain.RoleAgent, ProjectIDs: []string{"other"}, Capabilities: []string{"retrieval:read"}})
	require.NoError(t, err)
	verifierClient, err := identity.EnrollHost(t.Context(), service.EnrollInput{ClientID: "verifier", Role: domain.RoleVerifier, ProjectIDs: []string{"demo"}, Capabilities: []string{"verification:write"}})
	require.NoError(t, err)
	seedCapturePolicy(t, store, domain.Policy{Revision: "p1", SpoolLimitBytes: service.MaxArtifactBytes})
	capture := service.NewCaptureService(store, evidence, jobs, objects)
	host := service.NewHostCaptureService(store, evidence, jobs, objects, owners)
	verifyRepo := repository.NewVerificationRepository(store)
	pubRepo := repository.NewPublicationRepository(store)
	manifest := `{"dependencies":{"left-pad":"1.3.0"}}`
	repoDir, head := lessonGitRepo(t, "package.json", manifest)
	sources, err := gateway.NewGitSourceReader(map[string]string{"demo": repoDir})
	require.NoError(t, err)
	verify := service.NewVerificationService(store, verifyRepo, pubRepo, owners, evidence, host).WithEvidenceChecks(service.EvidenceChecks{Principals: auth, Evidence: evidence, Attestations: verifyRepo, Sources: sources, Objects: objects})
	lessons := service.NewLessonService(store, struct {
		*repository.VerificationRepository
		*repository.PublicationRepository
	}{verifyRepo, pubRepo}, auth)
	memory := service.NewMemoryService(store, repository.NewMemoryRepository(store), auth, evidence, capture)
	retrieval, err := service.NewRetrievalService(store, auth, repository.NewCheckpointRepository(store), evidence, objects, bytes.Repeat([]byte{2}, 32))
	require.NoError(t, err)
	scope := domain.Scope{ProjectID: "demo", TaskID: "task", SessionID: "session"}
	stage := func(t *testing.T, p domain.Principal, body, locator string) domain.Artifact {
		t.Helper()
		a, e := capture.Stage(t.Context(), p, service.ArtifactInput{Scope: scope, Digest: service.TokenDigest(body), Bytes: int64(len(body)), PolicyRevision: "p1", SourceLocator: locator, SourcePath: "source.txt"}, strings.NewReader(body))
		require.NoError(t, e)
		_, e = capture.Complete(t.Context(), p, a.ID, a.Digest)
		require.NoError(t, e)
		return a
	}
	ref := func(a domain.Artifact, revision string) domain.EvidenceRef {
		return domain.EvidenceRef{ArtifactID: a.ID, Digest: a.Digest, Locator: a.SourceLocator, SourceRevision: revision}
	}
	raw := func(t *testing.T, q string, params map[string]any) []any {
		t.Helper()
		var values []any
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			n, e := store.Unwrap(tx)
			if e != nil {
				return e
			}
			res, e := n.Run(ctx, q, params)
			if e != nil {
				return e
			}
			rows, e := res.Collect(ctx)
			for _, row := range rows {
				values = append(values, row.Values...)
			}
			return e
		}))
		return values
	}
	run := stage(t, collector.Principal, "PASS ok example 0.1s", "run/attested")
	att := domain.ExecutionAttestation{ID: "attestation-1", ArtifactID: run.ID, ArtifactDigest: run.Digest, CollectorID: "collector", Command: "go test ./...", ExitCode: 0, WorkspaceRevision: head, Environment: "ci"}
	data, err := json.Marshal(att)
	require.NoError(t, err)
	raw(t, `MATCH (a:Artifact {id:$artifact}) CREATE (x:ExecutionAttestation {id:$id,artifactID:$artifact,collectorID:'collector',artifactDigest:$digest,data:$data})-[:ATTESTS]->(a)`, map[string]any{"id": att.ID, "artifact": run.ID, "digest": run.Digest, "data": string(data)})

	t.Run("attested execution verifies narrow observation and stays task scoped", func(t *testing.T) {
		c, e := memory.Submit(t.Context(), agent.Principal, domain.CandidateInput{ClaimID: "suite", Scope: scope, Text: domain.ExecutionStatement(att), Kind: domain.KindTestOutcome, Applicability: domain.Applicability{Environment: "ci", RepositoryRevision: head}, Evidence: []domain.EvidenceRef{ref(run, "run")}})
		require.NoError(t, e)

		v, e := verify.Check(t.Context(), verifierClient.Principal, c.Revision)

		require.NoError(t, e)
		assert.Equal(t, domain.VerificationSupported, v.Outcome)
		assert.Equal(t, domain.TrustCollectorAttested, v.Origin.Trust)
		working, e := retrieval.Retrieve(t.Context(), agent.Principal, domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "demo", TaskID: "task"}, Query: "suite", Mode: domain.RetrievalWorking})
		require.NoError(t, e)
		require.Len(t, working.Chunks, 1)
		chunk := working.Chunks[0]
		assert.Equal(t, domain.ClaimVerified, chunk.ClaimStatus)
		require.NotNil(t, chunk.VerificationOutcome)
		assert.Equal(t, domain.VerificationSupported, *chunk.VerificationOutcome)
		assert.Equal(t, domain.MethodEvidencePolicy, chunk.VerificationMethod)
		assert.Contains(t, chunk.Warnings, "task_scoped_verified_observation")
		project, e := retrieval.Retrieve(t.Context(), agent.Principal, domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "demo"}, Query: "suite", Mode: domain.RetrievalEvidence})
		require.NoError(t, e)
		assert.True(t, project.NoResults, "task verification is not project publication")
	})

	t.Run("collector upload without attestation stays candidate", func(t *testing.T) {
		log := stage(t, collector.Principal, "PASS ok example", "run/unattested")
		c, e := memory.Submit(t.Context(), agent.Principal, domain.CandidateInput{ClaimID: "agent-suite", Scope: scope, Text: domain.ExecutionStatement(att), Kind: domain.KindTestOutcome, Applicability: domain.Applicability{Environment: "ci", RepositoryRevision: head}, Evidence: []domain.EvidenceRef{ref(log, "run")}})
		require.NoError(t, e)

		v, e := verify.Check(t.Context(), verifierClient.Principal, c.Revision)

		require.NoError(t, e)
		assert.Equal(t, "unsupported", string(v.Outcome))
		assert.Equal(t, []domain.ReasonCode{domain.ReasonUnattestedExecution}, v.Reasons)
		_, e = verify.Check(t.Context(), agent.Principal, c.Revision)
		assert.ErrorIs(t, e, domain.ErrForbidden)
	})

	t.Run("approved repository snapshot verifies declared version", func(t *testing.T) {
		copyOf := stage(t, collector.Principal, manifest, domain.GitLocator("demo", "package.json"))
		c, e := memory.Submit(t.Context(), agent.Principal, domain.CandidateInput{ClaimID: "left-pad", Scope: scope, Text: "left-pad is 1.3.0", Kind: domain.KindDependencyVersion, Applicability: domain.Applicability{RepositoryRevision: head, Versions: map[string]string{"left-pad": "1.3.0"}}, Evidence: []domain.EvidenceRef{ref(copyOf, head)}})
		require.NoError(t, e)

		v, e := verify.Check(t.Context(), verifierClient.Principal, c.Revision)

		require.NoError(t, e)
		assert.Equal(t, domain.VerificationSupported, v.Outcome)
		assert.Equal(t, domain.TrustGatewayObserved, v.Origin.Trust)
		assert.Len(t, v.DistinctOrigins, 1)
	})

	// Owner adjudication publishes a project claim used as lesson support.
	secret := "PRIVATE prefix; approved excerpt; PRIVATE suffix"
	source := stage(t, collector.Principal, secret, "hidden/source")
	candidate, err := memory.Submit(t.Context(), agent.Principal, domain.CandidateInput{ClaimID: "finding", Scope: scope, Text: "PRIVATE finding", Kind: "finding", Evidence: []domain.EvidenceRef{ref(source, "source-revision")}})
	require.NoError(t, err)
	conditions := domain.Applicability{Environment: "ci", Versions: map[string]string{"go": "1.27"}}
	adjudicate := func(t *testing.T, key string) string {
		t.Helper()
		c := candidate
		if key != "first" {
			var e error
			c, e = memory.Submit(t.Context(), agent.Principal, domain.CandidateInput{ClaimID: "finding-" + key, Scope: scope, Text: "PRIVATE finding", Kind: "finding", Evidence: []domain.EvidenceRef{ref(source, "source-revision")}})
			require.NoError(t, e)
		}
		d := service.AdjudicationDraft{ProjectID: "demo", CandidateRevision: c.Revision, ExpectedCandidateHead: c.Revision, Action: "verify", IdempotencyKey: key, TargetID: "approved-" + key, TargetText: "Approved retry finding " + key, TargetKind: "finding", TargetApplicability: conditions, Excerpts: []service.ExcerptSelection{{ArtifactID: source.ID, SourceRevision: "source-revision", SourceDigest: source.Digest, Start: 16, End: 32, ExpectedExcerptDigest: service.TokenDigest("approved excerpt")}}}
		preview, e := verify.PreviewHost(t.Context(), d)
		require.NoError(t, e)
		result, e := verify.AdjudicateHost(t.Context(), service.AdjudicationInput{AdjudicationDraft: d, ExpectedDigest: preview.Digest})
		require.NoError(t, e)
		return result.TargetRevision
	}
	target := adjudicate(t, "first")

	t.Run("retrieval exposes owner adjudication on verified chunk", func(t *testing.T) {
		out, e := retrieval.Retrieve(t.Context(), agent.Principal, domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "demo"}, Query: "approved-first", Mode: domain.RetrievalEvidence})
		require.NoError(t, e)
		require.Len(t, out.Chunks, 1)
		require.NotNil(t, out.Chunks[0].VerificationOutcome)
		assert.Equal(t, domain.VerificationSupported, *out.Chunks[0].VerificationOutcome)
		assert.Equal(t, domain.MethodOwnerAdjudicated, out.Chunks[0].VerificationMethod)
		wire, e := domain.RetrievalWire(out)
		require.NoError(t, e)
		assert.Contains(t, string(wire), `"verification_method":"owner_adjudicated"`)
	})

	lesson := domain.LessonInput{ProjectID: "demo", LessonID: "lesson-retry", Text: "Retry transient network failures once in CI.", SharingScope: domain.SharingCrossProject, Applicability: conditions, Counterexamples: []string{"timing assertions"}, SupportRevisions: []string{target}}
	published, err := lessons.PublishLesson(t.Context(), verifierClient.Principal, lesson)
	require.NoError(t, err)
	require.Equal(t, domain.PublicationPublished, published.State)

	t.Run("cross-project discovery returns only the applicable published lesson", func(t *testing.T) {
		request := domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "other"}, Query: "retry", Mode: domain.RetrievalEvidence, Applicability: conditions}

		out, e := retrieval.Retrieve(t.Context(), outsider.Principal, request)

		require.NoError(t, e)
		require.Len(t, out.Chunks, 1)
		chunk := out.Chunks[0]
		assert.Equal(t, "lesson:"+published.ClaimRevision, chunk.RecordID)
		assert.Contains(t, chunk.Text, "Not applicable when: timing assertions")
		assert.Empty(t, chunk.Evidence)
		assert.Contains(t, chunk.Warnings, "generalized_lesson_source_withheld")
		assert.Equal(t, domain.MethodLessonPolicy, chunk.VerificationMethod)
		body, e := json.Marshal(out)
		require.NoError(t, e)
		for _, hidden := range []string{"PRIVATE", "demo", source.ID, target, candidate.Revision, "approved-first", "Approved retry finding"} {
			assert.NotContains(t, string(body), hidden)
		}
		request.Applicability = domain.Applicability{Environment: "production", Versions: conditions.Versions}
		out, e = retrieval.Retrieve(t.Context(), outsider.Principal, request)
		require.NoError(t, e)
		assert.True(t, out.NoResults, "inapplicable lesson")
		request.Applicability = domain.Applicability{}
		out, e = retrieval.Retrieve(t.Context(), outsider.Principal, request)
		require.NoError(t, e)
		assert.True(t, out.NoResults, "unstated conditions do not match another project's lesson")
		request.Query = "Approved retry finding"
		request.Applicability = conditions
		out, e = retrieval.Retrieve(t.Context(), outsider.Principal, request)
		require.NoError(t, e)
		assert.True(t, out.NoResults, "other project's private claims are never discoverable")
	})

	t.Run("TestPublicationRaceWithInvalidation", func(t *testing.T) {
		for i := range 4 {
			support := adjudicate(t, fmt.Sprintf("race-%d", i))
			preview, e := verify.PreviewInvalidationHost(t.Context(), support)
			require.NoError(t, e)
			preview.IdempotencyKey = fmt.Sprintf("invalidate-%d", i)
			in := lesson
			in.LessonID = fmt.Sprintf("race-lesson-%d", i)
			in.SupportRevisions = []string{support}
			var wg sync.WaitGroup
			var publishErr, invalidateErr error
			var outcome domain.Publication
			wg.Add(2)
			go func() { defer wg.Done(); outcome, publishErr = lessons.PublishLesson(t.Context(), verifierClient.Principal, in) }()
			go func() { defer wg.Done(); _, invalidateErr = verify.InvalidateHost(t.Context(), preview) }()
			wg.Wait()
			require.NoError(t, invalidateErr)
			require.NoError(t, publishErr)
			assert.Contains(t, []string{domain.PublicationPublished, domain.PublicationRejected}, outcome.State)
		}

		eligiblePublications := raw(t, `MATCH (p:Publication {valid:true}) UNWIND p.supportRevisions AS s MATCH (c:ClaimRevision {id:s}) WHERE c.valid=false OR c.status<>'verified' RETURN DISTINCT p.id`, nil)

		assert.Empty(t, eligiblePublications)
		out, e := retrieval.Retrieve(t.Context(), outsider.Principal, domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "other"}, Query: "retry", Mode: domain.RetrievalEvidence, Applicability: conditions})
		require.NoError(t, e)
		require.Len(t, out.Chunks, 1, "only the original, still-supported lesson remains")
		assert.Equal(t, "lesson:"+published.ClaimRevision, out.Chunks[0].RecordID)
	})

	t.Run("invalidating support immediately withdraws the lesson", func(t *testing.T) {
		preview, e := verify.PreviewInvalidationHost(t.Context(), target)
		require.NoError(t, e)
		preview.IdempotencyKey = "invalidate-target"
		_, e = verify.InvalidateHost(t.Context(), preview)
		require.NoError(t, e)

		out, e := retrieval.Retrieve(t.Context(), outsider.Principal, domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "other"}, Query: "retry", Mode: domain.RetrievalEvidence, Applicability: conditions})

		require.NoError(t, e)
		assert.True(t, out.NoResults)
		rejected, e := lessons.PublishLesson(t.Context(), verifierClient.Principal, domain.LessonInput{ProjectID: "demo", LessonID: "late", Text: lesson.Text, SharingScope: domain.SharingCrossProject, Applicability: conditions, SupportRevisions: []string{target}})
		require.NoError(t, e)
		assert.Equal(t, domain.PublicationRejected, rejected.State)
	})
}

package repository_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

type lifecycleJournal struct {
	records []domain.JournalRecord
	head    domain.JournalBarrier
	failed  bool
}

func (j *lifecycleJournal) Append(_ context.Context, r domain.JournalRecord) (domain.JournalBarrier, error) {
	if j.failed {
		return domain.JournalBarrier{}, domain.ErrUnavailable
	}
	for _, v := range j.records {
		if v.OperationID == r.OperationID {
			return j.head, nil
		}
	}
	j.records = append(j.records, r)
	j.head = domain.JournalBarrier{Namespace: "fixture", Sequence: uint64(len(j.records)), Digest: service.TokenDigest(r.OperationID)}
	return j.head, nil
}
func (j *lifecycleJournal) CurrentHead(context.Context) (domain.JournalBarrier, error) {
	return j.head, nil
}
func (j *lifecycleJournal) ReadSince(_ context.Context, b domain.JournalBarrier) ([]domain.JournalRecord, error) {
	return j.records[b.Sequence:], nil
}
func (j *lifecycleJournal) VerifyContinuity(_ context.Context, a, b domain.JournalBarrier) error {
	if a.Namespace != b.Namespace || a.Sequence > b.Sequence {
		return domain.ErrUnavailable
	}
	return nil
}
func TestLifecycleGraphDeletionAndPolicy(t *testing.T) {
	store, jobs, _ := jobsGraph(t)
	objects := captureS3(t)
	warmObjects(t, objects)
	owners := repository.NewIdentityRepository(store)
	evidence := repository.NewEvidenceRepository(store)
	identity := service.NewIdentityService(store, owners, gateway.NostrIdentity{})
	collector, err := identity.Setup(t.Context(), domain.OwnerIdentity{ID: "owner", PublicKey: "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"}, service.EnrollInput{ClientID: "collector", Role: domain.RoleCollector, ProjectIDs: []string{"demo", "other"}, Capabilities: []string{"artifacts:write"}})
	require.NoError(t, err)
	seedCapturePolicy(t, store, domain.Policy{Revision: "p1", SpoolLimitBytes: service.MaxArtifactBytes})
	capture := service.NewCaptureService(store, evidence, jobs, objects)
	upload := func(locator string) domain.Artifact {
		t.Helper()
		text := "PRIVATE deletion original"
		a, e := capture.Stage(t.Context(), collector.Principal, service.ArtifactInput{Scope: domain.Scope{ProjectID: "demo", TaskID: "task", SessionID: "session"}, Digest: service.TokenDigest(text), Bytes: int64(len(text)), PolicyRevision: "p1", SourceLocator: locator, SourcePath: "source.txt"}, strings.NewReader(text))
		require.NoError(t, e)
		_, e = capture.Complete(t.Context(), collector.Principal, a.ID, a.Digest)
		require.NoError(t, e)
		return a
	}
	a := upload("original")
	copyArtifact := upload("copy")
	repo := repository.NewLifecycleRepository(store)
	journal := &lifecycleJournal{head: domain.JournalBarrier{Namespace: "fixture"}}
	lifecycle := service.NewLifecycleService(store, repo, owners, jobs, objects, journal, time.Now)
	excerptText := "derived bytes"
	derivedInput := service.ArtifactInput{Scope: domain.Scope{ProjectID: "demo"}, Digest: service.TokenDigest(excerptText), Bytes: int64(len(excerptText)), PolicyRevision: "p1", SourceLocator: "excerpt", SourcePath: "source.txt"}
	excerpt, e := capture.Stage(t.Context(), collector.Principal, derivedInput, strings.NewReader(excerptText))
	require.NoError(t, e)
	_, e = capture.Complete(t.Context(), collector.Principal, excerpt.ID, excerpt.Digest)
	require.NoError(t, e)
	derivedInput.SourceLocator = "unrelated-copy"
	excerptCopy, e := capture.Stage(t.Context(), collector.Principal, derivedInput, strings.NewReader(excerptText))
	require.NoError(t, e)
	_, e = capture.Complete(t.Context(), collector.Principal, excerptCopy.ID, excerptCopy.Digest)
	require.NoError(t, e)
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		n, e := store.Unwrap(tx)
		if e != nil {
			return e
		}
		res, e := n.Run(ctx, `MATCH (source:Artifact {id:$source}),(excerpt:Artifact {id:$excerpt}) CREATE (l:ExcerptLineage {id:'fixed-point-lineage'}) CREATE (l)-[:PINS_SOURCE]->(source) CREATE (l)-[:PINS_EXCERPT]->(excerpt)`, map[string]any{"source": a.ID, "excerpt": excerpt.ID})
		if e != nil {
			return e
		}
		_, e = res.Consume(ctx)
		return e
	}))
	selection := domain.DeleteSelection{ProjectID: "demo", IDs: []string{a.ID}, IdempotencyKey: "delete"}
	preview, err := lifecycle.PreviewHost(t.Context(), selection)
	require.NoError(t, err)
	assert.Contains(t, preview.Manifest.IDs, copyArtifact.ID)
	assert.Contains(t, preview.Manifest.IDs, excerpt.ID)
	assert.Contains(t, preview.Manifest.IDs, excerptCopy.ID)
	selection.ExpectedSelectionDigest = preview.SelectionDigest
	journal.failed = true
	pending, err := lifecycle.DeleteHost(t.Context(), selection)
	require.ErrorIs(t, err, domain.ErrUnavailable)
	assert.Equal(t, "pending_journal", pending.State)
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		_, _, e := evidence.Artifact(ctx, tx, a.ID)
		assert.ErrorIs(t, e, domain.ErrDeleted)
		_, _, e = evidence.Artifact(ctx, tx, copyArtifact.ID)
		assert.ErrorIs(t, e, domain.ErrDeleted)
		return nil
	}))
	journal.failed = false
	receipt, err := lifecycle.DeleteHost(t.Context(), selection)
	require.NoError(t, err)
	assert.Equal(t, "purge_pending", receipt.State)
	require.NoError(t, lifecycle.Recover(t.Context()))
	repeated := selection
	repeated.IdempotencyKey = "independent-exact-delete"
	repeatPreview, e := lifecycle.PreviewHost(t.Context(), repeated)
	require.NoError(t, e)
	repeated.ExpectedSelectionDigest = repeatPreview.SelectionDigest
	_, e = lifecycle.DeleteHost(t.Context(), repeated)
	require.NoError(t, e)
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		queued, e := jobs.List(ctx, tx, domain.JobQueued, 100)
		require.NoError(t, e)
		var purge []domain.Job
		for _, job := range queued {
			if job.Kind == "lifecycle.purge" {
				purge = append(purge, job)
			}
		}
		require.Len(t, purge, 2)
		assert.NotEqual(t, purge[0].InputDigest, purge[1].InputDigest)
		return nil
	}))
	_, err = capture.Stage(t.Context(), collector.Principal, service.ArtifactInput{Scope: a.Scope, Digest: a.Digest, Bytes: a.Bytes, PolicyRevision: "p1", SourceLocator: "changed-locator", SourcePath: "different.txt"}, strings.NewReader("PRIVATE deletion original"))
	assert.ErrorIs(t, err, domain.ErrDeleted)
}

func TestPolicyUpdateCASAndEligibility(t *testing.T) {
	store, jobs, _ := jobsGraph(t)
	objects := captureS3(t)
	warmObjects(t, objects)
	owners := repository.NewIdentityRepository(store)
	evidence := repository.NewEvidenceRepository(store)
	identity := service.NewIdentityService(store, owners, gateway.NostrIdentity{})
	collector, err := identity.Setup(t.Context(), domain.OwnerIdentity{ID: "owner", PublicKey: "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"}, service.EnrollInput{ClientID: "collector", Role: domain.RoleCollector, ProjectIDs: []string{"demo"}, Capabilities: []string{"artifacts:write"}})
	require.NoError(t, err)
	p1 := domain.Policy{Revision: "p1", SpoolLimitBytes: service.MaxArtifactBytes}
	seedCapturePolicy(t, store, p1)
	capture := service.NewCaptureService(store, evidence, jobs, objects)
	text := "retained policy original"
	a, err := capture.Stage(t.Context(), collector.Principal, service.ArtifactInput{Scope: domain.Scope{ProjectID: "demo"}, Digest: service.TokenDigest(text), Bytes: int64(len(text)), PolicyRevision: "p1", SourceLocator: "source", SourcePath: "source.txt"}, strings.NewReader(text))
	require.NoError(t, err)
	_, err = capture.Complete(t.Context(), collector.Principal, a.ID, a.Digest)
	require.NoError(t, err)
	repo := repository.NewLifecycleRepository(store)
	lifecycle := service.NewLifecycleService(store, repo, owners, jobs, objects, &lifecycleJournal{}, time.Now)
	p2 := p1
	p2.Revision = "p2"
	_, err = lifecycle.UpdatePolicyHost(t.Context(), "demo", "stale", p2)
	require.ErrorIs(t, err, domain.ErrStale)
	_, err = lifecycle.UpdatePolicyHost(t.Context(), "demo", "p1", p2)
	require.NoError(t, err)
	_, err = capture.Complete(t.Context(), collector.Principal, a.ID, a.Digest)
	assert.ErrorIs(t, err, domain.ErrStale)
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		more, e := repo.ReconcilePolicy(ctx, tx, "demo", "p2", 100)
		assert.False(t, more)
		return e
	}))
	_, err = capture.Complete(t.Context(), collector.Principal, a.ID, a.Digest)
	require.NoError(t, err)
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		retained, _, e := evidence.Artifact(ctx, tx, a.ID)
		require.NoError(t, e)
		assert.Equal(t, "p1", retained.PolicyRevision)
		assert.Equal(t, "p2", retained.CurrentPolicyRevision)
		return nil
	}))
	p3 := p2
	p3.Revision = "p3"
	p3.ExcludedFields = []string{"secret"}
	_, err = lifecycle.UpdatePolicyHost(t.Context(), "demo", "p2", p3)
	require.NoError(t, err)
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		_, e := repo.ReconcilePolicy(ctx, tx, "demo", "p3", 100)
		return e
	}))
	_, err = capture.Complete(t.Context(), collector.Principal, a.ID, a.Digest)
	require.ErrorIs(t, err, domain.ErrStale)
}

func independentLifecycleJournal(t *testing.T) *gateway.S3DeletionJournal {
	t.Helper()
	c, err := testcontainers.GenericContainer(t.Context(), testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: "chrislusf/seaweedfs:4.51@sha256:f7a49e3f3972ebed5be2ed4befa4a0c68eb7bdddba6e692d42729ceb545134b8", ExposedPorts: []string{"8333/tcp"}, Cmd: []string{"mini", "-dir=/data", "-ip.bind=0.0.0.0", "-master.telemetry=false"}, Env: map[string]string{"AWS_ACCESS_KEY_ID": "journal-fixture", "AWS_SECRET_ACCESS_KEY": "journal-fixture-secret", "S3_BUCKET": "journal"}, WaitingFor: wait.ForListeningPort("8333/tcp").WithStartupTimeout(90 * time.Second)}, Started: true})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		require.NoError(t, c.Terminate(ctx))
	})
	host, err := c.Host(t.Context())
	require.NoError(t, err)
	port, err := c.MappedPort(t.Context(), "8333/tcp")
	require.NoError(t, err)
	var key [32]byte
	copy(key[:], "independent-only-test-key-32bytes!")
	j, err := gateway.NewS3DeletionJournal(gateway.S3DeletionJournalConfig{Endpoint: "http://" + host + ":" + port.Port(), Region: "local", Bucket: "journal", Prefix: "deletions", Namespace: "lifecycle-fixture", AccessKey: "journal-fixture", SecretKey: "journal-fixture-secret", AddressingStyle: "path", KeyID: "fixture-key", Keys: map[string][32]byte{"fixture-key": key}})
	require.NoError(t, err)
	return j
}

type uncertainLifecycleJournal struct {
	service.DeletionJournal
	after bool
}

func (j uncertainLifecycleJournal) Append(ctx context.Context, r domain.JournalRecord) (domain.JournalBarrier, error) {
	if j.after {
		_, _ = j.DeletionJournal.Append(ctx, r)
	}
	return domain.JournalBarrier{}, domain.ErrUnavailable
}
func TestCrashAndRestoreReplaysIndependentJournalBeforeReadiness(t *testing.T) {
	store, jobs, _ := jobsGraph(t)
	objects := captureS3(t)
	warmObjects(t, objects)
	journal := independentLifecycleJournal(t)
	owners := repository.NewIdentityRepository(store)
	evidence := repository.NewEvidenceRepository(store)
	repo := repository.NewLifecycleRepository(store)
	identity := service.NewIdentityService(store, owners, gateway.NostrIdentity{})
	collector, err := identity.Setup(t.Context(), domain.OwnerIdentity{ID: "owner", PublicKey: "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"}, service.EnrollInput{ClientID: "collector", Role: domain.RoleCollector, ProjectIDs: []string{"demo"}, Capabilities: []string{"artifacts:write"}})
	require.NoError(t, err)
	lifecycle := service.NewLifecycleService(store, repo, owners, jobs, objects, journal, time.Now)
	_, err = lifecycle.InitializeJournalHost(t.Context())
	require.NoError(t, err)
	require.NoError(t, lifecycle.Recover(t.Context()))
	seedCapturePolicy(t, store, domain.Policy{Revision: "p1", SpoolLimitBytes: service.MaxArtifactBytes})
	capture := service.NewCaptureService(store, evidence, jobs, objects)
	for _, after := range []bool{false, true} {
		name := map[bool]string{false: "crash before journal append", true: "unknown append outcome and older graph restore"}[after]
		t.Run(name, func(t *testing.T) {
			text := "PRIVATE durable replay " + name
			artifact, e := capture.Stage(t.Context(), collector.Principal, service.ArtifactInput{Scope: domain.Scope{ProjectID: "demo"}, Digest: service.TokenDigest(text), Bytes: int64(len(text)), PolicyRevision: "p1", SourceLocator: "source-" + service.TokenDigest(text), SourcePath: "source.txt"}, strings.NewReader(text))
			require.NoError(t, e)
			_, e = capture.Complete(t.Context(), collector.Principal, artifact.ID, artifact.Digest)
			require.NoError(t, e)
			selection := domain.DeleteSelection{ProjectID: "demo", IDs: []string{artifact.ID}, IdempotencyKey: service.TokenDigest(name)}
			pre, e := lifecycle.PreviewHost(t.Context(), selection)
			require.NoError(t, e)
			selection.ExpectedSelectionDigest = pre.SelectionDigest
			crashed := service.NewLifecycleService(store, repo, owners, jobs, objects, uncertainLifecycleJournal{DeletionJournal: journal, after: after}, time.Now)
			receipt, e := crashed.DeleteHost(t.Context(), selection)
			require.ErrorIs(t, e, domain.ErrUnavailable)
			assert.Equal(t, "pending_journal", receipt.State)
			if after { // Simulate an older graph snapshot losing graph-side intent and
				// tombstones. Independent encrypted journal remains untouched and durable.
				require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
					n, e := store.Unwrap(tx)
					if e != nil {
						return e
					}
					res, e := n.Run(ctx, `MATCH (o:DeletionOperation {id:$id}) DELETE o WITH 1 AS ignored MATCH (t:Tombstone {id:$artifact}) DELETE t WITH 1 AS ignored MATCH (t:SourceTombstone {projectID:'demo',digest:$digest}) DELETE t WITH 1 AS ignored MATCH (a:Artifact {id:$artifact}) SET a.state='ready',a.available=true REMOVE a.deletionOperation`, map[string]any{"id": receipt.ID, "artifact": artifact.ID, "digest": artifact.Digest})
					if e != nil {
						return e
					}
					_, e = res.Consume(ctx)
					return e
				}))
			}
			require.NoError(t, lifecycle.Recover(t.Context()))
			status, e := lifecycle.DeletionStatusHost(t.Context(), receipt.ID)
			require.NoError(t, e)
			assert.Equal(t, "purge_pending", status.State)
			require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
				_, _, e := evidence.Artifact(ctx, tx, artifact.ID)
				assert.ErrorIs(t, e, domain.ErrDeleted)
				return nil
			}))
			_, e = lifecycle.InitializeJournalHost(t.Context())
			assert.ErrorIs(t, e, domain.ErrConflict)
			jobsService := service.NewJobsService(store, jobs)
			job, lease, e := jobsService.Claim(t.Context(), "purger", []string{"lifecycle.purge"}, time.Now().UTC().Add(time.Second))
			require.NoError(t, e)
			require.NoError(t, lifecycle.Purge(t.Context(), job, lease, jobsService))
			status, e = lifecycle.DeletionStatusHost(t.Context(), receipt.ID)
			require.NoError(t, e)
			assert.Equal(t, "purge_complete", status.State)
			assert.Equal(t, "expiry_pending", status.BackupState)
			_, e = objects.Open(t.Context(), artifact.ObjectKey)
			assert.ErrorIs(t, e, domain.ErrIncomplete)
		})
	}
}

func TestLifecycleRetentionBoundariesPinsAndTaskResurrection(t *testing.T) {
	store, jobs, _ := jobsGraph(t)
	objects := captureS3(t)
	warmObjects(t, objects)
	owners := repository.NewIdentityRepository(store)
	evidence := repository.NewEvidenceRepository(store)
	repo := repository.NewLifecycleRepository(store)
	identity := service.NewIdentityService(store, owners, gateway.NostrIdentity{})
	collector, err := identity.Setup(t.Context(), domain.OwnerIdentity{ID: "owner", PublicKey: "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"}, service.EnrollInput{ClientID: "collector", Role: domain.RoleCollector, ProjectIDs: []string{"demo", "other"}, Capabilities: []string{"artifacts:write"}})
	require.NoError(t, err)
	agent, err := identity.EnrollHost(t.Context(), service.EnrollInput{ClientID: "agent", Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: []string{"checkpoint:write", "retrieval:read", "candidate:write"}})
	require.NoError(t, err)
	now := time.Now().UTC()
	journal := &lifecycleJournal{head: domain.JournalBarrier{Namespace: "fixture"}}
	lifecycle := service.NewLifecycleService(store, repo, owners, jobs, objects, journal, func() time.Time { return now })
	for _, project := range []string{"demo", "other"} {
		p, err := lifecycle.UpdatePolicyHost(t.Context(), project, "", domain.Policy{Revision: "p1", SpoolLimitBytes: service.MaxArtifactBytes})
		require.NoError(t, err)
		assert.Equal(t, 90*24*time.Hour, p.RawHistoryRetention)
	}
	capture := service.NewCaptureService(store, evidence, jobs, objects)
	upload := func(project, text string) domain.Artifact {
		t.Helper()
		a, err := capture.Stage(t.Context(), collector.Principal, service.ArtifactInput{Scope: domain.Scope{ProjectID: project}, Digest: service.TokenDigest(text), Bytes: int64(len(text)), PolicyRevision: "p1", SourceLocator: "source-" + service.TokenDigest(text), SourcePath: "source.txt"}, strings.NewReader(text))
		require.NoError(t, err)
		_, err = capture.Complete(t.Context(), collector.Principal, a.ID, a.Digest)
		require.NoError(t, err)
		return a
	}
	boundary := upload("demo", "boundary")
	other := upload("other", "boundary")
	pinned := upload("demo", "pinned history source")
	unknown := upload("demo", "unknown legacy")
	recent := upload("demo", "old caller observed history")
	checkpoints := service.NewCheckpointService(store, repository.NewCheckpointRepository(store), repository.NewRetrievalRepository(store))
	task := domain.Scope{ProjectID: "demo", TaskID: "task/🧪", SessionID: strings.Repeat("s", 8192) + "/🧪"}
	cp, err := checkpoints.Put(t.Context(), agent.Principal, "", domain.Checkpoint{ID: "checkpoint/🧪", Scope: task, Objective: "private task state"})
	require.NoError(t, err)
	memory := service.NewMemoryService(store, repository.NewMemoryRepository(store), repository.NewRetrievalRepository(store), evidence, capture)
	longClaimID := "long-session-finding"
	candidate, err := memory.Submit(t.Context(), agent.Principal, domain.CandidateInput{ClaimID: longClaimID, Scope: task, Text: "private task hypothesis", Kind: "hypothesis"})
	require.NoError(t, err)
	require.NotEmpty(t, candidate.Revision)
	// Real retained object generations with repository pin metadata mirror the
	// exact prepared-only and cross-task full-support cases reviewed in C2.
	scopedUpload := func(scope domain.Scope, text, locator string) domain.Artifact {
		t.Helper()
		a, e := capture.Stage(t.Context(), collector.Principal, service.ArtifactInput{Scope: scope, Digest: service.TokenDigest(text), Bytes: int64(len(text)), PolicyRevision: "p1", SourceLocator: locator, SourcePath: "source.txt"}, strings.NewReader(text))
		require.NoError(t, e)
		_, e = capture.Complete(t.Context(), collector.Principal, a.ID, a.Digest)
		require.NoError(t, e)
		return a
	}
	preparedSource := scopedUpload(domain.Scope{ProjectID: "demo", TaskID: "prepared-only-task", SessionID: "prepared-session"}, "prepared source bytes", "prepared-source")
	crossSource := scopedUpload(domain.Scope{ProjectID: "demo", TaskID: "full-support-source-task", SessionID: "full-support-session"}, "full support bytes", "full-support-source")
	crossCopy := scopedUpload(domain.Scope{ProjectID: "demo", TaskID: "same-digest-task", SessionID: "same-digest-session"}, "full support bytes", "full-support-copy")
	preparedSupport, e := memory.Submit(t.Context(), agent.Principal, domain.CandidateInput{ClaimID: "prepared-support-finding", Scope: domain.Scope{ProjectID: "demo", TaskID: "prepared-support-task", SessionID: "prepared-support-session"}, Text: "prepared support hypothesis", Kind: "hypothesis"})
	require.NoError(t, e)
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		n, e := store.Unwrap(tx)
		if e != nil {
			return e
		}
		res, e := n.Run(ctx, `MATCH (prepared:Artifact {id:$prepared}),(support:ClaimRevision {id:$support}),(source:Artifact {id:$source}) CREATE (d:PreparedDerivation {id:'prepared-retention-pin',state:'prepared',projectID:'demo',sourceArtifactID:$prepared,supportRevision:$support}) CREATE (d)-[:PINS_SOURCE]->(prepared) CREATE (d)-[:PINS_SUPPORT]->(support) CREATE (p:Publication {id:'full-support-retention-pin',valid:true}) CREATE (p)-[:REQUIRES_ARTIFACT]->(source) WITH 1 AS ignored MATCH (a:Artifact) WHERE a.id IN $artifacts SET a.ingestedAt=$historyAt WITH 1 AS ignored MATCH (t:TaskActivity) WHERE t.taskID IN ['prepared-only-task','prepared-support-task','full-support-source-task','same-digest-task'] SET t.lastActivityAt=$taskAt`, map[string]any{"prepared": preparedSource.ID, "support": preparedSupport.Revision, "source": crossSource.ID, "artifacts": []string{preparedSource.ID, crossSource.ID, crossCopy.ID}, "historyAt": now.Add(-91 * 24 * time.Hour).UnixNano(), "taskAt": now.Add(-30 * 24 * time.Hour).UnixNano()})
		if e != nil {
			return e
		}
		_, e = res.Consume(ctx)
		return e
	}))
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		n, e := store.Unwrap(tx)
		if e != nil {
			return e
		}
		res, e := n.Run(ctx, `MATCH (a:Artifact {id:$boundary}),(p:Artifact {id:$pinned}),(u:Artifact {id:$unknown}) SET a.ingestedAt=$boundaryAt,p.ingestedAt=$pinnedAt REMOVE u.ingestedAt CREATE (pin:ExcerptLineage {id:'pin'}) CREATE (pin)-[:PINS_SOURCE]->(p) WITH 1 AS ignored MATCH (t:TaskActivity {projectID:'demo',taskID:$task}) SET t.lastActivityAt=$taskAt`, map[string]any{"boundary": boundary.ID, "boundaryAt": now.Add(-90 * 24 * time.Hour).UnixNano(), "pinned": pinned.ID, "pinnedAt": now.Add(-91 * 24 * time.Hour).UnixNano(), "unknown": unknown.ID, "task": task.TaskID, "taskAt": now.Add(-30 * 24 * time.Hour).UnixNano()})
		if e != nil {
			return e
		}
		_, e = res.Consume(ctx)
		return e
	}))
	report, err := lifecycle.Expire(t.Context(), now)
	require.NoError(t, err)
	require.Len(t, report.Operations, 2)
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		for _, a := range []domain.Artifact{pinned, unknown, recent, other, preparedSource, crossSource, crossCopy} {
			_, _, e := evidence.Artifact(ctx, tx, a.ID)
			assert.NoError(t, e)
		}
		_, _, e := evidence.Artifact(ctx, tx, boundary.ID)
		assert.ErrorIs(t, e, domain.ErrDeleted)
		retained, _, e := repository.NewMemoryRepository(store).Head(ctx, tx, preparedSupport.ClaimID)
		require.NoError(t, e)
		assert.Equal(t, preparedSupport.Revision, retained.Revision)
		return nil
	}))
	_, err = checkpoints.Put(t.Context(), agent.Principal, "", domain.Checkpoint{ID: "new-checkpoint", Scope: task, Objective: "resurrection"})
	assert.ErrorIs(t, err, domain.ErrDeleted)
	_, err = checkpoints.Get(t.Context(), agent.Principal, cp.ID)
	assert.Error(t, err)
	// Explicit owner deletion overrides the retained source pin.
	selection := domain.DeleteSelection{ProjectID: "demo", IDs: []string{pinned.ID}, IdempotencyKey: "override-pin"}
	pre, err := lifecycle.PreviewHost(t.Context(), selection)
	require.NoError(t, err)
	selection.ExpectedSelectionDigest = pre.SelectionDigest
	_, err = lifecycle.DeleteHost(t.Context(), selection)
	require.NoError(t, err)
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		_, _, e := evidence.Artifact(ctx, tx, pinned.ID)
		assert.ErrorIs(t, e, domain.ErrDeleted)
		return nil
	}))
	longTask := strings.Repeat("t", 8192) + "/🧪"
	longScope := domain.Scope{ProjectID: "demo", TaskID: longTask, SessionID: "long-collector-session"}
	longArtifact := scopedUpload(longScope, "long collector task source", "long-collector-source")
	longSelection := domain.DeleteSelection{ProjectID: "demo", TaskIDs: []string{longTask}, IdempotencyKey: "long-collector-task-delete"}
	longPreview, e := lifecycle.PreviewHost(t.Context(), longSelection)
	require.NoError(t, e)
	assert.Contains(t, longPreview.Manifest.Tasks, longTask)
	longSelection.ExpectedSelectionDigest = longPreview.SelectionDigest
	_, e = lifecycle.DeleteHost(t.Context(), longSelection)
	require.NoError(t, e)
	_, e = capture.Stage(t.Context(), collector.Principal, service.ArtifactInput{Scope: longScope, Digest: service.TokenDigest("new bytes"), Bytes: 9, PolicyRevision: "p1", SourceLocator: "new-collector-source", SourcePath: "source.txt"}, strings.NewReader("new bytes"))
	require.ErrorIs(t, e, domain.ErrDeleted)
	require.NotEmpty(t, longArtifact.Generation)
	sessionSelection := domain.DeleteSelection{ProjectID: "demo", SessionIDs: []string{task.SessionID}, IdempotencyKey: "long-session-delete"}
	sessionPreview, e := lifecycle.PreviewHost(t.Context(), sessionSelection)
	require.NoError(t, e)
	assert.Contains(t, sessionPreview.Manifest.Sessions, task.SessionID)
	sessionSelection.ExpectedSelectionDigest = sessionPreview.SelectionDigest
	_, e = lifecycle.DeleteHost(t.Context(), sessionSelection)
	require.NoError(t, e)
	jobsService := service.NewJobsService(store, jobs)
	for range 5 {
		job, lease, e := jobsService.Claim(t.Context(), "purger", []string{"lifecycle.purge"}, now.Add(time.Second))
		require.NoError(t, e)
		broken := service.NewLifecycleService(store, repo, owners, jobs, uncertainLifecycleObjects{}, journal, func() time.Time { return now })
		var purgeObjects []domain.PurgeObject
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			var e error
			purgeObjects, e = repo.PurgeObjects(ctx, tx, job.InputRevisionIDs[0])
			return e
		}))
		if len(purgeObjects) > 0 {
			expected := purgeObjects[0]
			setGeneration := func(generation string) {
				t.Helper()
				require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
					n, e := store.Unwrap(tx)
					if e != nil {
						return e
					}
					res, e := n.Run(ctx, `MATCH (a:Artifact {id:$id}) SET a.generation=$generation`, map[string]any{"id": expected.ID, "generation": generation})
					if e != nil {
						return e
					}
					_, e = res.Consume(ctx)
					return e
				}))
			}
			setGeneration(expected.Generation + "-changed")
			require.ErrorIs(t, lifecycle.Purge(t.Context(), job, lease, jobsService), domain.ErrStale)
			reader, e := objects.Open(t.Context(), expected.Key)
			require.NoError(t, e)
			require.NoError(t, reader.Close())
			setGeneration(expected.Generation)
			require.ErrorIs(t, broken.Purge(t.Context(), job, lease, jobsService), domain.ErrUnavailable)
			status, e := lifecycle.DeletionStatusHost(t.Context(), job.InputRevisionIDs[0])
			require.NoError(t, e)
			assert.Equal(t, "purge_pending", status.State)
		}
		require.NoError(t, lifecycle.Purge(t.Context(), job, lease, jobsService))
	}
	t.Run("task write after expiry preview prevents deletion", func(t *testing.T) {
		freshScope := domain.Scope{ProjectID: "demo", TaskID: "activity-race", SessionID: "activity-session"}
		current, e := checkpoints.Put(t.Context(), agent.Principal, "", domain.Checkpoint{ID: "activity-checkpoint", Scope: freshScope, Objective: "before task write"})
		require.NoError(t, e)
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			n, e := store.Unwrap(tx)
			if e != nil {
				return e
			}
			res, e := n.Run(ctx, `MATCH (a:TaskActivity {projectID:'demo',taskID:'activity-race'}) SET a.lastActivityAt=$at`, map[string]any{"at": now.Add(-30 * 24 * time.Hour).UnixNano()})
			if e != nil {
				return e
			}
			_, e = res.Consume(ctx)
			return e
		}))
		between := &afterLifecycleTx{Transactor: store, after: func() {
			_, e := checkpoints.Put(t.Context(), agent.Principal, current.Revision, domain.Checkpoint{ID: current.ID, Scope: freshScope, Objective: "accepted concurrent task write"})
			require.NoError(t, e)
		}}
		expiring := service.NewLifecycleService(between, repo, owners, jobs, objects, journal, func() time.Time { return now })
		report, e := expiring.Expire(t.Context(), now)
		require.NoError(t, e)
		assert.Empty(t, report.Operations)
		retained, e := checkpoints.Get(t.Context(), agent.Principal, current.ID)
		require.NoError(t, e)
		assert.Equal(t, "accepted concurrent task write", retained.Objective)
	})
	t.Run("purged artifact does not block later task selection", func(t *testing.T) {
		text := "sequential task source"
		scope := domain.Scope{ProjectID: "demo", TaskID: "sequential-source-task", SessionID: "sequential-session"}
		a, e := capture.Stage(t.Context(), collector.Principal, service.ArtifactInput{Scope: scope, Digest: service.TokenDigest(text), Bytes: int64(len(text)), PolicyRevision: "p1", SourceLocator: "sequential-source", SourcePath: "source.txt"}, strings.NewReader(text))
		require.NoError(t, e)
		_, e = capture.Complete(t.Context(), collector.Principal, a.ID, a.Digest)
		require.NoError(t, e)
		initial := domain.DeleteSelection{ProjectID: "demo", IDs: []string{a.ID}, IdempotencyKey: "sequential-artifact-delete"}
		pre, e := lifecycle.PreviewHost(t.Context(), initial)
		require.NoError(t, e)
		initial.ExpectedSelectionDigest = pre.SelectionDigest
		_, e = lifecycle.DeleteHost(t.Context(), initial)
		require.NoError(t, e)
		job, lease, e := jobsService.Claim(t.Context(), "purger", []string{"lifecycle.purge"}, time.Now().UTC().Add(time.Second))
		require.NoError(t, e)
		require.NoError(t, lifecycle.Purge(t.Context(), job, lease, jobsService))
		selection := domain.DeleteSelection{ProjectID: "demo", TaskIDs: []string{scope.TaskID}, IdempotencyKey: "sequential-task-delete"}
		pre, e = lifecycle.PreviewHost(t.Context(), selection)
		require.NoError(t, e)
		selection.ExpectedSelectionDigest = pre.SelectionDigest
		_, e = lifecycle.DeleteHost(t.Context(), selection)
		require.NoError(t, e)
	})
}

type uncertainLifecycleObjects struct{}

func (uncertainLifecycleObjects) Delete(context.Context, string) error { return domain.ErrUnavailable }

type afterLifecycleTx struct {
	service.Transactor
	calls int
	after func()
}

func (t *afterLifecycleTx) Within(ctx context.Context, fn func(context.Context, service.Tx) error) error {
	err := t.Transactor.Within(ctx, fn)
	t.calls++
	if err == nil && t.calls == 2 {
		t.after()
	}
	return err
}

func TestPolicyBatchProgressAndRetentionFairness(t *testing.T) {
	store, jobs, _ := jobsGraph(t)
	owners := repository.NewIdentityRepository(store)
	repo := repository.NewLifecycleRepository(store)
	identity := service.NewIdentityService(store, owners, gateway.NostrIdentity{})
	_, err := identity.Setup(t.Context(), domain.OwnerIdentity{ID: "owner", PublicKey: "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"}, service.EnrollInput{ClientID: "collector", Role: domain.RoleCollector, ProjectIDs: []string{"long", "short"}, Capabilities: []string{"artifacts:write"}})
	require.NoError(t, err)
	now := time.Now().UTC()
	journal := &lifecycleJournal{head: domain.JournalBarrier{Namespace: "fixture"}}
	lifecycle := service.NewLifecycleService(store, repo, owners, jobs, uncertainLifecycleObjects{}, journal, func() time.Time { return now })
	long := domain.Policy{Revision: "p1", SpoolLimitBytes: service.MaxArtifactBytes, RawHistoryRetention: 365 * 24 * time.Hour}
	_, err = lifecycle.UpdatePolicyHost(t.Context(), "long", "", long)
	require.NoError(t, err)
	_, err = lifecycle.UpdatePolicyHost(t.Context(), "short", "", domain.Policy{Revision: "p1", SpoolLimitBytes: service.MaxArtifactBytes})
	require.NoError(t, err)
	rows := make([]map[string]any, 0, 1203)
	for i := range 1202 {
		project := "long"
		source := "allowed/source.txt"
		if i < 101 {
			source = "blocked/source.txt"
		}
		id := service.TokenDigest(fmt.Sprintf("source-%d", i))
		a := domain.Artifact{ID: id, ObjectKey: "evidence/" + id + "/generation", Digest: id, Generation: "generation", PolicyRevision: "p1", SourcePath: source, SourceLocator: "opaque", Scope: domain.Scope{ProjectID: project}, Bytes: 1, State: domain.ArtifactReady, ObjectAvailable: true}
		data, e := json.Marshal(a)
		require.NoError(t, e)
		rows = append(rows, map[string]any{"id": id, "data": string(data), "path": source, "project": project, "digest": id})
	}
	short := domain.Artifact{ID: "short-source", ObjectKey: "evidence/short/generation", Digest: service.TokenDigest("short"), Generation: "generation", PolicyRevision: "p1", SourcePath: "allowed/source.txt", SourceLocator: "opaque", Scope: domain.Scope{ProjectID: "short"}, Bytes: 1, State: domain.ArtifactReady, ObjectAvailable: true}
	data, e := json.Marshal(short)
	require.NoError(t, e)
	rows = append(rows, map[string]any{"id": short.ID, "data": string(data), "path": short.SourcePath, "project": "short", "digest": short.Digest})
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		n, e := store.Unwrap(tx)
		if e != nil {
			return e
		}
		res, e := n.Run(ctx, `UNWIND $rows AS row CREATE (:Artifact {id:row.id,data:row.data,clientID:'collector',projectID:row.project,taskID:'',sessionID:'',state:'ready',available:true,objectKey:'evidence/'+row.id+'/generation',generation:'generation',digest:row.digest,policyRevision:'p1',sourcePath:row.path,bytes:1,lifecycleMetadataVersion:1,ingestedAt:$now})`, map[string]any{"rows": rows, "now": now.UnixNano()})
		if e != nil {
			return e
		}
		_, e = res.Consume(ctx)
		return e
	}))

	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		n, e := store.Unwrap(tx)
		if e != nil {
			return e
		}
		res, e := n.Run(ctx, `MATCH (a:Artifact) SET a.ingestedAt=CASE WHEN a.projectID='long' THEN $older ELSE $due END`, map[string]any{"older": now.Add(-100 * 24 * time.Hour).UnixNano(), "due": now.Add(-91 * 24 * time.Hour).UnixNano()})
		if e != nil {
			return e
		}
		_, e = res.Consume(ctx)
		return e
	}))
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		due, e := repo.Expiry(ctx, tx, now, 100)
		require.NoError(t, e)
		require.Len(t, due, 1)
		assert.Equal(t, "short-source", due[0].IDs[0])
		return nil
	}))
	long.Revision = "p2"
	long.ExcludedPaths = []string{"blocked"}
	_, err = lifecycle.UpdatePolicyHost(t.Context(), "long", "p1", long)
	require.NoError(t, err)
	jobsService := service.NewJobsService(store, jobs)
	processed := 0
	for range 30 {
		job, lease, e := jobsService.Claim(t.Context(), "policy-worker", []string{"lifecycle.reconcile"}, time.Now().UTC().Add(time.Second))
		if errors.Is(e, domain.ErrIncomplete) {
			break
		}
		require.NoError(t, e)
		current, e := lifecycle.PolicyHost(t.Context(), job.Scope.ProjectID)
		require.NoError(t, e)
		if current.Revision != job.PolicyRevision {
			require.NoError(t, jobsService.Cancel(t.Context(), job.ID))
			continue
		}
		require.NoError(t, lifecycle.ReconcilePolicy(t.Context(), job, lease, jobsService))
		processed++
	}
	assert.GreaterOrEqual(t, processed, 3)
	assert.Len(t, journal.records, 101)
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		n, e := store.Unwrap(tx)
		if e != nil {
			return e
		}
		res, e := n.Run(ctx, `MATCH (e:PolicyEligibility {policyRevision:'p2',eligible:true}) RETURN count(e)`, nil)
		if e != nil {
			return e
		}
		row, e := res.Single(ctx)
		require.NoError(t, e)
		assert.Equal(t, int64(1101), row.Values[0])
		return nil
	}))
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		n, e := store.Unwrap(tx)
		if e != nil {
			return e
		}
		res, e := n.Run(ctx, `MATCH (a:Artifact {projectID:'long',state:'deleted'}) RETURN count(a)`, nil)
		if e != nil {
			return e
		}
		row, e := res.Single(ctx)
		require.NoError(t, e)
		assert.Equal(t, int64(101), row.Values[0])
		return nil
	}))
}

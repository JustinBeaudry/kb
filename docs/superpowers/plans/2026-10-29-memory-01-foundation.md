# Graph Memory Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Provide an authenticated durable graph API for candidate evidence and bounded retrieval.

**Architecture:** Go services own graph transactions and evidence publication; repositories use a consumer-defined transaction handle. S3 objects are staged separately and graph jobs reconcile incomplete writes.

**Tech Stack:** Go 1.24+, Echo v4, strict oapi-codegen, candidate Neo4j/v6 driver, S3-compatible gateway, testcontainers-go.

**Spec:** [Approved design](../specs/2026-10-29-persistent-memory-design.md); [shared plan contracts](2026-10-29-persistent-memory.md).

## Global Constraints

All global constraints and domain/transaction contracts in the shared plan apply verbatim. Orientation 4 KiB; retrieval 16 KiB; depth 2; 50 visited records. No verified publication is exposed before I1 supplies the verification policy. Keep model and Buzz calls out of database callbacks.

## Review Focus

- Forge Origin/verified fields: rejected at request boundary and absent from service inputs (F2/F4).
- Deadlock or retried transaction repeats an effect: rollback/retry stays internal; outbox has one key (F3).
- Dangling objects after crash: never returned as evidence (F4).
- Unauthorized IDs and changed cursor scope: no existence leak (F5).
- Cyclic/high-degree graph and Unicode expansion: limits count actual complete output and visited records (F5).

### Task F1: Qualify storage and establish typed contracts

**Files:** Create `server/go.mod`, `server/go.sum`, `server/api/{openapi.yaml,oapi-codegen.yaml}`, `server/internal/domain/{identity,policy,evidence,claim,checkpoint,job,decision,retrieval,errors,transaction}.go`, `server/internal/service/ports.go`, `server/internal/platform/graph/{connect.go,transaction.go}`, `server/internal/platform/graph/qualification_test.go`, `server/migrations/001_identity.cypher`, `deploy/versions.json`, `docs/adr/0001-memory-storage.md`.

**Interfaces:** Produces shared domain types and `service.Transactor`; `graph.Open(ctx context.Context, cfg Config) (*Store,error)` and `(*Store).Close(ctx context.Context) error`. `Config` holds URI/database/user/secret through nonlogging configuration. Store implements Transactor; driver types stay in platform/repository.

- [ ] Write `TestQualifiedGraphSerializesHeadChanges`, using a real temporary graph and 20 concurrent transactions starting from one expected head: `assert.Equal(t, 1, accepted); assert.Equal(t, 19, conflicted)`. Add rollback, uniqueness, disconnect, schema rerun, fencing, and dump/load identity checks. Store secret-safe fixture outputs only.

Assertion anchors for the separate named cases above (fixture variables come from that case):

```go
assert.Equal(t, 1, accepted)
assert.Equal(t, 19, conflicted)
```

- [ ] Run `go test -race ./internal/platform/graph -run TestQualified -v` from `server/`; first establish Go module/testcontainer prerequisites, then confirm the missing/unsafe transaction implementation fails behaviorally. A missing Docker daemon is blocked infrastructure, not the intended red test.
- [ ] Implement the private OwnerState lock protocol and shared types. Pin Go/compiler, graph image digest, driver, Echo, generator, validator, test libraries and linter after actual compatibility checks. Generate strict server types to `server/internal/handler/api.gen.go`; never hand-edit. Record constraints, dump/restore maintenance, resource measurements, licensing source, and S3 qualification procedure in ADR. A failed capability gate blocks graph-dependent tasks, not a silent storage substitution.
- [ ] Run the qualification tests and `go vet ./...`; expected all pass, with clean shutdown and restored IDs. Check ADR contains exact pinned versions and measured limits. No cloud resources are needed for local qualification.
- [ ] Commit only this task's files as `chore: qualify graph memory storage and contracts`.

### Task F2: Authenticate clients and enforce project capabilities

**Files:** Create `server/internal/service/{identity.service.go,identity.service_test.go}`, `server/internal/repository/{identity.repository.go,identity_integration_test.go}`, `server/internal/handler/{auth.middleware.go,identity.handler.go,identity.handler_test.go}`, `server/internal/platform/config.go`, `server/cmd/{server,kb-admin}/main.go`; extend `server/migrations/002_clients.cypher` and OpenAPI.

**Interfaces:** `IdentityService.Authenticate(ctx, bearer string) (domain.Principal,error)`; `Enroll(ctx, owner domain.Principal, input EnrollInput) (ClientCredential,error)`; `Revoke(ctx, owner domain.Principal, clientID string) error`. EnrollInput has role, project IDs, capabilities; ClientCredential contains client ID and one-time bearer. Repository consumer methods: `FindToken(ctx,tx,digest) (ClientRecord,error)`, `PutClient(ctx,tx,ClientRecord) error`, `RevokeClient(ctx,tx,id) error`. ClientRecord includes principal, token digest, revoked time. Authorization checks each operation and project intersection.

- [ ] Write `TestAgentCannotEnrollVerifier` (`assert.ErrorIs(t, err, domain.ErrForbidden)`), `TestRevokedTokenFailsNextRequest`, `TestClientCannotRequestOtherProject`, and `TestRequestValidatorRejectsForgedOrigin`; response bodies must not contain tokens or private IDs. Test invalid enum/oversized body through actual Echo handler, not only service methods.

Assertion anchors for the separate named cases above (fixture variables come from that case):

```go
assert.ErrorIs(t, err, domain.ErrForbidden)
assert.NotContains(t, responseBody, fixtureSecret)
```

- [ ] Run `go test -race ./internal/service ./internal/handler -run 'Test(Agent|Revoked|Client|Request)' -v`; confirm failures before implementation.
- [ ] Implement service, hash-only token repository, central capability middleware, strict handlers and OpenAPI validation. Bootstrap initial owner via explicit local operator setup with enrolled Buzz owner public identity; never hand owner capabilities to model tools. Owner recovery CLI uses short-lived Buzz-confirmed challenges, completed in I2; fail closed until available. Add request IDs, body/time limits, and metadata-only structured logging.
- [ ] Rerun targeted and real-graph identity tests; require revoked-token and role-forgery cases pass. `kb-admin` help must describe setup prerequisites without emitting secrets.
- [ ] Commit as `feat: enforce memory client identity and project scope`.

### Task F3: Durable jobs, leases, and transaction-bound outbox

**Files:** Create `server/internal/service/{jobs.service.go,jobs.service_test.go}`, `server/internal/repository/{jobs.repository.go,jobs_integration_test.go}`, `server/internal/platform/worker.go`, `server/cmd/worker/main.go`, `server/migrations/003_jobs.cypher`.

**Interfaces:** `JobsService.Claim(ctx, workerID string, kinds []string, now time.Time) (domain.Job,domain.Lease,error)`; `Complete(ctx, lease domain.Lease, expectedDigest string) error`; `Fail(ctx, lease domain.Lease, retryAt time.Time, reasonCode string) error`. Consumer repo `Enqueue(ctx,tx,domain.Job) error`, `Claim(ctx,tx,workerID,kinds,now) (Job,Lease,error)`, `Finish(ctx,tx,Lease,digest) error`; unique job key is kind+input digest+policy revision. Platform worker accepts `RunJob func(context.Context,domain.Job,domain.Lease) error`.

- [ ] Write `TestExpiredWorkerCannotCommit` (`assert.ErrorIs(t,err,domain.ErrStale)`), `TestDuplicateEnqueueCreatesOneJob` (`assert.Len(t,jobs,1)`), and real graph `TestCrashAfterCommitReclaimsOnce`; separate Act per case. Failure state must retain reason code but no source payload.

Assertion anchors for the separate named cases above (fixture variables come from that case):

```go
assert.ErrorIs(t, lateCommitErr, domain.ErrStale)
assert.Len(t, persistedJobs, 1)
```

- [ ] Run `go test -race ./internal/service ./internal/repository -run 'Test(ExpiredWorker|DuplicateEnqueue|CrashAfter)' -v`; confirm red results.
- [ ] Implement 60-second leases, monotonic fencing, heartbeat at 20 seconds, maximum five attempts with bounded backoff, dead-job inspection, cancellation, and graceful worker stop. Retries never publish after lease loss. Use graph transaction and OwnerState lock; no external queue dependency.
- [ ] Run tests including two worker processes racing on the same graph; prove one active lease and one logical effect. Shutdown requeues or allows lease expiry without marking unfinished work complete.
- [ ] Commit as `feat: add fenced memory jobs and outbox`.

### Task F4: Evidence ingestion, artifacts, and task checkpoints

**Files:** Create `server/internal/service/{capture.service.go,capture.service_test.go,checkpoint.service.go}`, `server/internal/repository/{evidence.repository.go,checkpoint.repository.go,capture_integration_test.go}`, `server/internal/gateway/{s3.gateway.go,s3.gateway_test.go}`, `server/internal/handler/{capture.handler.go,checkpoint.handler.go}`, `server/migrations/004_evidence.cypher`; extend OpenAPI.

**Interfaces:** `CaptureService.Append(ctx,p Principal,events []Event) ([]Receipt,error)`; `Stage(ctx,p,meta ArtifactInput,body io.Reader) (Artifact,error)`; `Complete(ctx,p,artifactID,digest string) (Receipt,error)`; `CheckpointService.Put(ctx,p,expectedRevision string,c Checkpoint) (Checkpoint,error)`. ArtifactInput contains scope, digest, length, policy revision; no caller trust field. `ObjectStore.Put(ctx,key string,body io.Reader,digest string) error`, `Open(ctx,key string) (io.ReadCloser,error)`, `Delete(ctx,key string) error` are consumer interfaces. All types are domain types except these local input structs.

- [ ] Write `TestReusedEventKeyWithDifferentDigestConflicts`, `TestIncompleteArtifactCannotSupportClaim`, `TestForgedExitCodeStaysSubmitted`, `TestCheckpointRejectsWrongTaskOwner`; assert sentinel error or submitted Origin. Integration test interrupts each of object write/graph commit/ack and retries: `assert.Len(t,readyArtifacts,1); assert.Len(t,extractionJobs,1)` after successful recovery.

Assertion anchors for the separate named cases above (fixture variables come from that case):

```go
assert.Equal(t, "submitted", string(storedOrigin.Trust))
assert.Len(t, readyArtifacts, 1)
assert.Len(t, extractionJobs, 1)
```

- [ ] Run `go test -race ./internal/service ./internal/repository ./internal/gateway -run 'Test(Reused|Incomplete|Forged|Checkpoint|CaptureCrash)' -v`; use isolated S3-compatible fixture and real graph; confirm red.
- [ ] Implement capped streaming upload (8 MiB per chunk), SHA-256 validation, immutable ready manifests, dedupe, artifact reference authorization, staged-object reconciliation, and checkpoint expected revisions. Derive Origin from authenticated transport capability only. Jobs reference immutable IDs; raw payload never goes in job metadata. Missing authenticated policy fails upload closed.
- [ ] Rerun crash/dedupe/auth tests; require no orphan is retrievable and no double extraction. Verify object gateway errors expose no keys/secrets. Preserve source locators after chunking.
- [ ] Commit as `feat: capture durable evidence and task checkpoints`.

### Task F5: Candidate graph and bounded authorized retrieval

**Files:** Create `server/internal/service/{memory.service.go,retrieval.service.go,retrieval.service_test.go}`, `server/internal/repository/{memory.repository.go,retrieval.repository.go,retrieval_integration_test.go}`, `server/internal/handler/{memory.handler.go,retrieval.handler.go,retrieval.handler_test.go}`, `server/internal/domain/budget.go`, `server/migrations/005_memory.cypher`; extend OpenAPI.

**Interfaces:** `MemoryService.Submit(ctx,p Principal,input CandidateInput) (ClaimRevision,error)`; `RetrievalService.Retrieve(ctx,p Principal,r RetrievalRequest) (RetrievalResult,error)`; `Fetch(ctx,p Principal,artifactID,locator string,budget int) (RetrievalResult,error)`. Handler separately serializes schema-v3 and verifies final wire budget. Repo methods `PutCandidate(ctx,tx,ClaimRevision) error`, `Seeds(ctx,tx,Scope,query string,limit int) ([]ClaimRevision,error)`, `Neighbors(ctx,tx,ids []string,relationKinds []string,limit int) ([]ClaimRevision,error)`, `Eligible(ctx,tx,p,revisionIDs) ([]ClaimRevision,error)`. Define returned authorization epoch with eligibility results for final validation.

- [ ] Write table cases `TestRetrievalBudgetsAndTrust`: orientation <=4096 bytes; evidence <=16384; visited <=50; depth <=2; Unicode/JSON escaping included; tiny budget ErrBudget; cycle terminates; candidate absent except task working mode. Add `TestRevokedCursorCannotFetch`, `TestInvalidatedDependencyExcludedWithStaleIndex`, `TestCrossProjectLessonHidesSourceMetadata`, `TestConflictingVersionsRemainVisible`.

Assertion anchors for the separate named cases above (fixture variables come from that case):

```go
assert.LessOrEqual(t, len(orientationWire), 4096)
assert.LessOrEqual(t, len(evidenceWire), 16384)
assert.LessOrEqual(t, result.Visited, 50)
assert.NotContains(t, returnedRevisionIDs, candidateRevision)
```

- [ ] Run `go test -race ./internal/service ./internal/handler ./internal/repository -run 'Test(Retrieval|RevokedCursor|InvalidatedDependency|CrossProject|Conflicting)' -v`; fixtures insert verified records directly only in test setup; confirm red.
- [ ] Implement exact-ID and lexical seed lookup, relation allowlist, incremental bounded expansion, applicability ranking, provenance, conflict flags, explicit no-results/unavailable distinction, and HMAC continuation bound to client/scope/auth+knowledge epochs. Check every dependency via indexed validity state: each publication stores its transitive supporting revision IDs transactionally; if lineage exceeds a configured 1000-reference verification cap, leave candidate unpublished. Support corrections/deletion predicates in every fetch, including stale projections. New writes never accept verified state. Refuse stale continuations rather than silently mix snapshots.
- [ ] Run targeted tests plus Go full gate. Run `/plan-devex-review` recommendations against the final operation shapes, then record a sample bounded HTTP/CLI envelope for C3. Foundation acceptance is real upload -> candidate -> task-only retrieval; verification awaits I1.
- [ ] Commit as `feat: retrieve scoped graph memory within context budgets`.

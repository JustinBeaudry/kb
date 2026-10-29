# Memory Operations and Acceptance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enforce lifecycle/privacy, demonstrate recoverability and useful retrieval, then deploy and verify the private service.

**Architecture:** Deletion and invalidation are authoritative graph operations that gate every projection. Backups pair graph state with evidence manifests and replay deletion records before serving. Railway runs separately restartable API, worker, graph and optional conversational services.

**Tech Stack:** Go/Bun gates, selected Neo4j and S3 versions from F1, Railway configuration, OTel and structured logging, independent evaluation runner.

**Spec:** [Approved design](../specs/2026-10-29-persistent-memory-design.md); [shared contracts](2026-10-29-persistent-memory.md).

## Global Constraints

All shared constraints apply verbatim. Filtered history 90 days, inactive task state 30 days; explicit deletion overrides evidence pins. Daily backup with 30-day retention, RPO <=24h and RTO <=4h targets. No live acceptance claim from a deployment health probe. No publishing private source content in logs, evaluation reports, or notifications.

## Review Focus

- Deletion races a pending dream/import: no source resurrection (O1).
- Encryption key rotation/recovery is incomplete: refuse readiness rather than serve corrupt or plaintext data (O2).
- Graph backup and object set disagree: restore reports missing evidence and cannot serve it as verified (O2).
- Evaluation exposes hidden answers or unequal budgets: invalidate that trial (O3).
- Harness/cloud restart during active work: foreground continues and recovery reports genuine gaps (O4).

### Task O1: Retention, corrections, deletion, and policy changes

**Files:** Create `server/internal/service/{lifecycle.service.go,lifecycle.service_test.go,policy.service.go}`, `server/internal/repository/{lifecycle.repository.go,lifecycle_integration_test.go}`, `server/internal/handler/{lifecycle.handler.go,policy.handler.go}`, `server/internal/domain/deletion.go`, `server/migrations/009_lifecycle.cypher`, `docs/runbooks/privacy.md`; extend worker jobs and OpenAPI.

**Interfaces:** `LifecycleService.Delete(ctx,p Principal,selection DeleteSelection) (DeletionReceipt,error)`; `Expire(ctx,now time.Time) (ExpiryReport,error)`; `Invalidate(ctx,p Principal,revisionID,reason string) error`; `PolicyService.Update(ctx,p Principal,projectID,expectedRevision string,policy Policy) (Policy,error)`. DeleteSelection contains project and exact claim/artifact/session IDs; never an unrestricted query string. DeletionReceipt identifies tombstone and asynchronous purge status. Repository methods accept Tx; tombstones contain only minimal opaque identities/digests needed to prevent resurrection, with no source prose.

- [ ] Write `TestDeletionBeatsEvidencePin`, `TestNinetyDayHistoryExpiryRetainsPinnedExcerpt`, `TestThirtyDayTaskExpiry`, `TestDeleteRacesDreamPublication`, `TestStaleCursorCannotReadDeletedSource`, `TestUnselectedProjectSurvivesDeletion`, `TestPolicyChangePurgesQueuedNotification`. Assert deleted items are immediately ineligible even while physical purge is pending; day-boundary tests use injected clock.

Assertion anchors for the separate named cases above (fixture variables come from that case):

```go
assert.Empty(t, eligibleDeletedRecords)
assert.True(t, pinnedExcerptStillReadable) // ordinary history expiry
assert.False(t, pinnedExcerptStillReadable) // explicit deletion, separate case
```

- [ ] Run `go test -race ./internal/service ./internal/repository -run 'Test(Deletion|NinetyDay|ThirtyDay|DeleteRaces|StaleCursor|Unselected|PolicyChange)' -v`; confirm red against real graph race cases.
- [ ] Implement transactional tombstones/dependency invalidation, pinned excerpt retention with content hashes, immediate visibility exclusion and idempotent purge jobs for objects/indexes/caches. Update shared knowledge/auth epochs as appropriate. Persist a minimal encrypted append-only deletion journal outside the graph backup lifecycle; a deletion is acknowledged only once its restore barrier is durable. Restore must fail closed if journal freshness cannot be established. Reimport of identical deleted source identity is denied unless explicit new owner-authorized ingestion is recorded. Direct deletion authority never goes to ordinary harness tokens.
- [ ] Run retention/deletion races, restore-journal replay fixture and C4 reimport test. Verify status distinguishes accepted/inaccessible/purge complete and backup expiry; no false promise of immediate deletion from old backups. Run Go gate.
- [ ] Commit as `feat: enforce memory retention and deletion across derivatives`.

### Task O2: Recoverable deployment artifacts and observability

**Files:** Create `deploy/{Dockerfile.api,Dockerfile.worker,Dockerfile.graph,compose.test.yaml,railway.api.toml,railway.worker.toml,railway.graph.toml}`, `server/internal/platform/{telemetry.go,health.go}`, `server/internal/gateway/{encryption.gateway.go,encryption.gateway_test.go}`, `server/internal/acceptance/restore_test.go`, `scripts/{memory-backup.sh,memory-restore.sh,check-memory-coverage.sh}`, `docs/runbooks/{deployment,recovery,credentials,observability}.md`, `.github/workflows/memory-ci.yml`; extend F1 ADR and versions manifest.

**Interfaces:** `EvidenceCipher.Seal(ctx,plaintext io.Reader,metadata CipherMetadata) (io.ReadCloser,error)`; `Open(ctx,ciphertext io.Reader,metadata CipherMetadata) (io.ReadCloser,error)` if provider encryption cannot meet the design. CipherMetadata has key ID, object identity and format version; use audited authenticated-encryption tooling, not custom cryptography. `Health.Check(ctx) (HealthReport,error)` returns startup/readiness dependencies without secrets. Backup manifest contains graph checkpoint ID, object manifest digest, schema/tool versions, deletion journal watermark, encryption key IDs and completion status.

- [ ] Write `TestRestoreRejectsMissingEvidence`, `TestRestoreReplaysDeletionBeforeReadiness`, `TestEncryptionKeyRotationPreservesPinnedEvidence`, `TestTelemetryContainsNoFixtureSecrets`, `TestMissingModelRouteDoesNotFailReadiness`. Assert restored citation hashes match and deleted records remain unavailable. Verify liveness vs readiness vs model/Buzz degradation are distinct.

Assertion anchors for the separate named cases above (fixture variables come from that case):

```go
assert.False(t, health.Ready) // deletion watermark missing
assert.Equal(t, originalEvidenceDigest, restoredEvidenceDigest)
assert.NotContains(t, telemetryJSON, fixtureSecret)
```

- [ ] Run `go test -race ./internal/acceptance ./internal/gateway ./internal/platform -run 'Test(Restore|Encryption|Telemetry|MissingModel)' -v`; confirm red with temporary storage. Real-graph restore tests may be integration-tagged but CI must run them explicitly, not silently skip.
- [ ] Build image/config/scripts and centralized redaction/OTel. Run services as unprivileged users; private graph port only, scoped bucket credentials, TLS external ingress, strict resource/time limits. Verify graph/object/backup encryption from current provider capabilities; document residual server-readable trust. Implement offline Community dump/restore in an explicit maintenance window, pausing graph writes and workers; collectors continue spooling. Always record completed manifest only after evidence integrity verification. Separate decryption key custody from backup objects. CI runs Go race/lint/vet, TS tests/lint, Python adapter gate when present, generation cleanliness, and 80% coverage enforcement for maintained application packages excluding generated files. Record baseline coverage exceptions explicitly, never silently weaken the floor.
- [ ] Run a complete local backup -> fresh graph/object restore -> deletion replay -> authorized retrieval exercise. Measure restore time against <=4h; simulate loss of latest day's work to validate <=24h RPO scheduling. Record actual timings and outage duration; no Enterprise backup claim. Confirm graceful shutdown, fenced worker recovery, and no sensitive telemetry.
- [ ] Commit as `feat: package recoverable memory services and operations`.

### Task O3: Fair retrieval and lifecycle evaluation

**Files:** Create `evals/memory/{protocol.md,fixtures.ts,prepare.ts,run.ts,grade.ts,schemas.ts}`, `tests/memory-eval.test.ts`, `docs/reports/memory-evaluation.md`; modify `package.json` for `eval:memory`. Leave the frozen existing retrieval-control pilot untouched.

**Interfaces:** `prepareTrial(arm: 'files'|'local-kb'|'graph', caseID: string, seed: number): Promise<TrialManifest>`; `validateTrace(trace: TrialTrace, manifest: TrialManifest): ValidationResult`; `grade(answer: SolverAnswer, gold: GoldCase): Grade`. Types in schemas.ts define corpus digest, permitted tools, total output budget, model/harness/version, provenance, reported usage and hidden-gold separation. `run.ts` dispatches an explicitly configured harness; no fake solver output counts as a trial.

- [ ] Write `rejects unequal corpus or resource budgets`, `gold never appears in solver context`, `counts abstention separately from success`, `missing token usage stays unknown`, `stale source citation fails grounding`, `capture contamination fails safety`; use synthetic trace assertions with deliberate invalid cases.

Assertion anchors for the separate named cases above (fixture variables come from that case):

```typescript
expect(validation.valid).toBe(false); // unequal-budget trial
expect(solverInput).not.toContain(hiddenGoldMarker);
expect(grade.reportedTokens).toBeNull(); // unknown usage
```

- [ ] Run `bun test tests/memory-eval.test.ts`; confirm red before runner/validator implementation.
- [ ] Implement 12 paired task types with two fresh-context repetitions per arm: 72 runs total. Types cover fact lookup, API operation, dependency version, superseded decision, failed attempt, unsupported cause, absent fact, contradiction, cross-project applicability, prohibited source, compaction recovery, and cyclic irrelevant neighbors. Same curated evidence and tools except the tested retrieval adapter; 16 retrieval calls and 128 KiB total retrieval output per trial. Freeze corpus/gold/protocol/model before solving. Separately test six lifecycle scenarios: duplicates, concurrent promotion, poisoning, evidence invalidation, deletion restore, and offline capture; these do not masquerade as retrieval quality. Model cost cap must be configured before paid runs.
- [ ] Run fixture/validator tests then actual paired trials when credentials/budget are available. Hard gates: zero unauthorized disclosure or unverified promotion, 100% deterministic budget/lifecycle tests. Usefulness threshold: graph grounded success >= the stronger baseline on 24 cases, plus either >=2 additional solved cases without greater total retrieval bytes, or >=20% fewer retrieval bytes with no success loss. Report latency/cost and paired outcomes separately. Missing runs mean no usefulness claim; a small pilot is not universal proof. If thresholds fail, record causes and revise retrieval before rollout rather than changing gold or weakening baseline.
- [ ] Commit runner and honest report as `test: evaluate graph memory against capable retrieval baselines`.

### Task O4: Private rollout, harness acceptance, and release review

**Files:** Create `server/internal/acceptance/private_test.go`, `docs/reports/memory-private-acceptance.md`, `docs/runbooks/onboarding.md`; update `README.md`, harness matrix, deployment runbook and version manifest with qualified revisions.

**Interfaces:** Acceptance is API/client-facing. It must use the same public interfaces and restricted credentials as the deployed clients, never direct graph writes to manufacture verified results. Report fields: deployed commit/image versions, environment, scenario, observation, artifact ID, pass/fail/blocked, cleanup receipt.

- [ ] Write synthetic acceptance fixtures for upload -> candidate -> authenticated verification -> scoped retrieval; version correction -> invalidation; Buzz exact-revision reply; Hermes cited conversation; cross-project lesson with inaccessible source; pause/offline/restart/resume; snapshot restore honoring deletion. Assert no hidden admin promotion and no stale expected-revision bypass.

Assertion anchors for the separate named cases above (fixture variables come from that case):

```go
assert.Equal(t, "verified", returnedClaim.Trust) // independently verified case
assert.Empty(t, restrictedSourceContents)
assert.Equal(t, 1, acceptedDecisionEffects)
```

- [ ] Run `go test -race -tags acceptance ./internal/acceptance -run TestPrivate -v` against local configured fixtures first; expect intended red until end-to-end wiring is complete. Missing external configuration must produce an explicit blocked report, not a passing skip.
- [ ] Run `/review`, `/cso`, and `/devex-review`; resolve findings with evidence. Prepare `/ship` PRs with atomic dependency order. Configure owner-selected Railway project/region, spend limit, KB-specific Buzz identity/channel, authorized model routes/daily cap, client project scopes and exclusions. Before enabling real capture, review its preview against synthetic secrets. Use `/land-and-deploy` within authorized release scope; no admin bypass. Deploy private services, verify encryption/backup/readiness, then enroll first clients. A live secret/identity limitation blocks only the affected external action, not completion of local work.
- [ ] Execute private scenarios for generic CLI/API, Codex, Claude Code and Hermes at pinned versions. Where a harness lacks trusted execution attestation, demonstrate candidate-only behavior and independent gateway/owner verification instead. Run actual Buzz request/reply with the owner, conversational evidence checks and restart/recovery. Confirm there are no public storage endpoints and rotated credentials fail. Import only the chosen manifest; do not automatically ingest historical sessions. Record each result separately from health/lint/tests and track exact remaining blockers.
- [ ] Commit acceptance/runbook evidence as `docs: record private graph memory acceptance`; complete release only after actual required checks, with canary monitoring and rollback instructions. Retain the local CLI path and original vault unchanged.

## Completion checklist

- [ ] All linked tasks have passing specified tests and review receipts.
- [ ] Graph authority and object evidence survive concurrent writes, correction, deletion, and restore.
- [ ] Automatic capture and bounded retrieval work in the qualified harness matrix; unsupported capabilities are explicit.
- [ ] Dreaming publishes only supported knowledge; Buzz decisions and Hermes citations work live.
- [ ] Fair evaluation meets preregistered criteria or release is held with measured reasons.
- [ ] Owner can inspect/revoke clients, pause capture, delete/export knowledge and recover the service.
- [ ] Deployment health, automated tests, benchmark results, and private acceptance are reported as separate evidence.

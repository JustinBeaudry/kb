# Persistent Memory Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver Justin's private, evidence-backed graph memory service across agent harnesses on Railway.

**Architecture:** A Go service owns the authoritative graph, evidence manifests, scoped retrieval, and promotion. Private object storage retains filtered evidence; workers verify and consolidate knowledge, Hermes accesses the same service, and Buzz carries owner decisions. Letta supplies selected patterns, not a parity target.

**Tech Stack:** Go 1.24+ (pin the locally available maintained Go 1.27.3 in qualification if compatibility passes), Echo v4, oapi-codegen strict, Neo4j candidate with Go driver v6, S3-compatible object storage, existing Bun/TypeScript CLI. No PostgreSQL, document DB, Redis, or vector service by default.

**Spec:** [Approved design](../specs/2026-10-29-persistent-memory-design.md).

## Global Constraints

- One owner for now; no team administration or multi-tenant product.
- The graph is authoritative; Letta is a reference, not a parity target.
- Go is preferred for the API; keep the existing local TypeScript CLI behavior.
- Handler -> Service -> Repository/Gateway -> I/O; services depend on interfaces and own transactions.
- Domain imports standard library only; every Go I/O method takes context.Context first.
- API DTOs never enter services; generated code is never hand-edited.
- Task-local candidates never appear as verified shared knowledge.
- Direct evidence must pass authenticity and applicability gates; model agreement is insufficient.
- 4 KiB complete-wire orientation, 16 KiB complete-wire evidence retrieval, depth 2, maximum 50 visited records.
- Raw filtered history 90 days; inactive task state 30 days; evidence supporting retained claims is pinned unless deletion overrides it.
- Daily findings digest; immediate actionable notices; daily backups retained 30 days; RPO <=24h and RTO <=4h are validation targets.
- Preserve local source-read gates and existing schema-v2 output; use explicit remote commands and schema-v3 envelopes.
- No historical source upload without selected import; no credentials, raw prompts, or source content in telemetry.
- TS casts need SAFETY comments; Result<T,E> in TS services; Go t.Cleanup and table-driven tests.
- TDD for services/domain/handlers; mocks at repo/gateway boundaries; 80% relevant line coverage enforced in CI.
- Go gate: `go vet ./... && go test -race ./... && golangci-lint run` from `server/`.
- TS gate: `bun run lint && bun run test` from repo root. Python adapters, if needed: uv, Ruff, strict mypy, pytest.

## Review Focus

- Harness files or an event's `role=user` are forged: candidate remains untrusted (F4, C2, I1).
- Credentials/scopes change between search and continuation/fetch: no stale disclosure (F5, O1).
- Crash lands between object upload, graph commit, and acknowledgment: one usable artifact/event, or explicit incomplete state (F4, C1).
- A dream result or Buzz approval arrives after evidence changes: no stale publication (I1, I2, I3).
- Deleted knowledge returns through import, backup restore, or delayed jobs: tombstones prevent resurrection (C4, O1, O2).

## Plan structure and dependencies

The approved design spans subsystems. These separate plans keep deliverables independently reviewable while covering the complete product:

| Plan | Deliverable | Dependency |
| --- | --- | --- |
| [01 Foundation](2026-10-29-memory-01-foundation.md) | Authenticated durable graph API and bounded retrieval | None |
| [02 Capture and clients](2026-10-29-memory-02-capture.md) | Filtered offline-safe capture, remote CLI/MCP, initial adapters, selected import | F1-F5 |
| [03 Intelligence](2026-10-29-memory-03-intelligence.md) | Evidence promotion, Buzz, dreaming, Hermes conversation | F1-F5; C2 for harness evidence |
| [04 Operations and acceptance](2026-10-29-memory-04-operations.md) | Deletion, restore, Railway rollout, fair evaluation, private acceptance | All earlier deliverables |

Do not call the product complete after Plan 01. Plans 02 and 03 can run in parallel after their shared contract lands. Within a plan, follow listed dependencies. Any agent owns explicitly assigned files and must preserve others' edits. Use spec-compliance review then code-quality review per task and whole-branch review before PRs.

## Repository map

- `server/`: Go module `github.com/JustinBeaudry/kb/server`; `cmd/{server,worker,kb-admin}` composition roots; `api/openapi.yaml`; `internal/{domain,handler,service,repository,gateway,platform}`.
- `server/migrations/*.cypher`: versioned graph constraints/indexes; no SQL migration tools for graph schema.
- `src/services/remote.service.ts`, `src/gateways/memory.gateway.ts`, `src/commands/remote.ts`: explicit remote CLI.
- `src/capture/`, `src/adapters/`, `src/commands/capture.ts`: new event capture alongside existing conversation-only local capture.
- `server/internal/handler/mcp.handler.go`: transport-independent MCP dispatcher invoking the same service interfaces as HTTP; `server/cmd/kb-mcp/main.go` wires stdio. Remote HTTP transport is separately qualified.
- `integrations/hermes/`: thin plugin for the qualified Hermes version, with independent uv test gate if Python.
- `deploy/`, `docs/runbooks/`, `docs/adr/`, `evals/memory/`: operational configuration, decisions, and evaluation.

No existing file moves. Follow the current citty command registration pattern in `src/cli.ts`, envelope/budget behavior in `src/lib/retrieval.ts`, and fixtures in `tests/transcript-portability.test.ts`. Preserve their local contracts. Read the Software Factory Buzz gateway as an implementation reference, not as a shared runtime dependency or credential source.

## Shared domain contract (F1 owns definitions)

Use explicit structs in `server/internal/domain/`; ID fields are strings validated as nonempty opaque IDs. Revision identifiers are immutable opaque strings. Time fields are UTC `time.Time`; monetary amounts are integer micro-USD, never float. Optional versions/times remain absent, not zero claims.

| Type / file | Required fields |
| --- | --- |
| Principal / `identity.go` | ClientID, OwnerID string; Role enum; ProjectIDs, Capabilities []string; AuthEpoch uint64 |
| Scope / `identity.go` | ProjectID, TaskID, SessionID string |
| Policy / `policy.go` | Revision string; Paused bool; ExcludedPaths, ExcludedFields []string; AllowedModelRoutes []string; retention durations; SpoolLimitBytes int64 |
| Event / `evidence.go` | ID, IdempotencyKey, Digest string; Scope; Kind enum; ObservedAt; PayloadArtifactID string; Sequence uint64; Completeness enum |
| Origin / `evidence.go` | ClientID string; Trust enum(submitted, collector_attested, gateway_observed, owner_authenticated); AttestationID string |
| Artifact / `evidence.go` | ID, ObjectKey, Digest, PolicyRevision, SourceLocator, Generation string; optional SourcePath string; ObjectAvailable and upload lease metadata separate from immutable JSON; Scope; Bytes int64; State(staged, ready, deleted; internal abandoning/abandoned are ineligible); ExpiresAt (staging deadline, cleared on ready) |
| EvidenceRef / `evidence.go` | ArtifactID, Digest, Locator, SourceRevision string; Origin; Applicability |
| Applicability / `claim.go` | RepositoryRevision, DirtyDigest, Environment string; Versions map[string]string; ValidFrom, ValidUntil optional UTC times |
| ClaimRevision / `claim.go` | ClaimID, Revision, Text, Kind string; Scope; Evidence []EvidenceRef; DependsOn []string; Applicability; Status enum |
| Checkpoint / `checkpoint.go` | ID, Revision string; Scope; Objective string; Completed, Pending, Questions, EvidenceRevisionIDs []string; UpdatedAt |
| CandidateInput / `claim.go` | ClaimID, ExpectedRevision, Text, Kind string; Scope; Evidence []EvidenceRef; DependsOn []string; Applicability |
| Verification / `claim.go` | ID, ClaimRevision, PolicyRevision, EvidenceDigest string; Outcome enum; Method string; Origin; CheckedAt |
| Publication / `claim.go` | ID, ClaimRevision, SharingScope, Revision string; SupportRevisions []string; PolicyRevision string |
| Job / `job.go` | ID, Kind, InputDigest, PolicyRevision string; Scope; InputRevisionIDs []string; Status enum; Attempts int; DueAt |
| Lease / `job.go` | JobID, WorkerID string; Fence uint64; ExpiresAt |
| Decision / `decision.go` | ID, CandidateRevision, EvidenceDigest, RequestedAction string; OwnerID string; ExpiresAt; State enum |
| DecisionReply / `decision.go` | DecisionID, CandidateRevision, EvidenceDigest, Action, EventID, SignerID string; Signature []byte |
| Receipt / `evidence.go` | ID string; Duplicate bool; State string |
| RetrievalRequest / `retrieval.go` | Scope; Query string; EntityIDs []string; Applicability; BudgetBytes, MaxDepth, MaxVisited int; Continuation string; Mode enum(orientation,evidence,working) |
| EvidenceChunk / `retrieval.go` | RecordID, Revision, Text string; ClaimStatus closed enum; OriginTrust closed enum; VerificationOutcome optional closed enum; Evidence []EvidenceRef; Warnings []string |
| RetrievalResult / `retrieval.go` | SchemaVersion string; Chunks []EvidenceChunk; optional TaskCheckpoint (explicitly labeled task state, never a verified claim); Truncated bool; Continuation string; Visited int; NoResults bool |

Boundary structs referenced in task signatures are defined in the owning service file; API counterparts are separate generated DTOs. Sentinel errors in `domain/errors.go`: ErrUnauthorized, ErrForbidden, ErrConflict, ErrStale, ErrIncomplete, ErrBudget, ErrUnavailable, ErrDeleted, ErrInvalidInput. Map to consistent API problem responses; never leak source existence to unauthorized callers.

Use exact claim statuses `candidate`, `verified`, `superseded`, `disputed`,
`invalidated`, `rejected`; verification outcomes `supported`, `unsupported`,
`ambiguous`; completeness `complete`, `partial`, `withheld`. Usage carries separate
`TokensKnown` and `CostKnown` booleans. An unknown settlement preserves its outstanding
cost reservation until reconciliation. Verification outcome and published status
are deliberately separate fields.

Signature notation in the task plans abbreviates `ctx` as `context.Context`, `p` and
`owner` as `domain.Principal`, and `tx` as the consumer-defined `service.Tx`. Unqualified
shared types refer to `domain`; local input/result types are defined in the named
service file. Expand these annotations when writing Go, not as implicit language types.

### Transaction and repository contract

Define an opaque `domain.Tx` handle in `internal/domain/transaction.go` with only `TransactionID() string`; it imports no driver types. Define consumer interfaces in `internal/service/ports.go`: `type Tx = domain.Tx`; `type Transactor interface { Within(ctx context.Context, fn func(context.Context, Tx) error) error }`. The Neo4j implementation in platform owns driver handles privately and provides repository-only unwrapping. Repositories use `domain.Tx` without importing services; no repository opens a transaction or calls another repository. Service methods invoke `Within` and coordinate repositories. Network effects occur outside retryable database callbacks, through jobs/outbox records written in the same transaction as state changes.

Initially serialize mutable shared graph decisions through one private `OwnerState` lock node. Acquire its write lock before reading revision/deletion/auth epochs; hold until commit. This intentionally trades throughput for auditable correctness at single-owner scale. Acquire locks in one order. Retrieval must perform authoritative filtering and final epoch validation before returning a bounded buffered response; retry on changed epochs, fail closed after bounded retries. A read completed before a revocation is allowed; any request beginning after committed revocation must observe it. Do not claim a plain MATCH/WHERE revision check alone prevents races.

### HTTP and remote wire contract

F1 owns `server/api/openapi.yaml`: `/v1/events:append`, `/v1/artifacts`, `/v1/artifacts/{id}:complete`, `/v1/candidates`, `/v1/checkpoints/{id}`, `/v1/retrieval:orient`, `/v1/retrieval:search`, `/v1/evidence/{id}`, `/v1/decisions/{id}:respond`, `/v1/policies/{project}`, `/v1/clients`, `/v1/clients/{id}:revoke`, `/v1/deletions`, `/v1/exports`, `/v1/status`.

F1 freezes snake_case JSON property names and golden wire examples for candidate,
verified, conflict, no-results, unavailable-source, and tiny-budget cases. Never
collapse claim status, origin trust, and verification outcome into one field.
Problems carry stable `code`, safe `message`, `action`, `request_id`, and a runbook
reference; optional retry delay/minimum budget must not reveal restricted existence.

Register only implemented operations; unimplemented routes return an explicit unavailable problem. Never accept client-supplied Origin/verified status as authority. Authentication derives Origin and Principal. Remote schema-v3 responses include explicit verification status; preserve existing schema-v2 local parsing without broadening `curated` to mean verified. Budget the HTTP JSON response and the CLI/MCP representation independently, including framing.

## Standard task cycle

Each task includes named behavioral assertions, a red command, implementation signatures, green command, and atomic commit. Test snippets are assertion fragments to place in named table-driven `t.Run` cases with AAA structure, one Act, require preconditions, and t.Cleanup. Harness setup helpers may be introduced in the listed test file; they are not production APIs. Run the relevant full stack gate before each plan's completion, not after every unchanged documentation edit.

## Selection and rollout decisions

Neo4j Community plus its v6 Go driver is the first qualification candidate. Version patches and image digests are selected and pinned by F1, never floating `latest`. Qualification must prove the planned locking, constraints, migration, and backup workflow. Community offline dump/restore must not be described as Enterprise online backup. Use a documented maintenance window with durable capture spooling; if that cannot meet the approved recovery targets, stop for a concrete engine/edition decision.

S3-compatible storage is the evidence interface; Railway buckets are a candidate. Check network/encryption/retention features from current official docs, not assumptions. Do not commit paid resources during planning. No vector index is necessary for the first baseline: exact IDs and lexical seeds plus bounded traversal must work first.

Model routes, provider credentials, Buzz identity/channel, deployment spending limit, and project import selection are operational prerequisites. Complete local synthetic implementation and reviewable deployment artifacts while they are unavailable; report the exact external acceptance still blocked. Do not use another project's credentials or silently enable an external model route.

## Sources and current qualification limits

- Neo4j Go v6 installation/compatibility: https://neo4j.com/docs/go-manual/current/install/
- Neo4j locking semantics: https://neo4j.com/docs/operations-manual/current/database-internals/concurrent-data-access/
- Community/Enterprise backup differences: https://neo4j.com/docs/operations-manual/current/backup-restore/
- Railway volumes: https://docs.railway.com/volumes/reference
- Railway buckets: https://docs.railway.com/storage-buckets
- Existing design sources cover Hermes, Letta, and Buzz references.

Research checked 2026-10-29. These references support planning, not executed engine,
provider, harness, or Railway acceptance. F1 records pinned compatibility evidence.

## Self-review and coverage

Pre-build DX rulings are recorded in [memory-plan-devex](../../reports/memory-plan-devex.md).
Implement within the owning tasks: F2 operator-only one-time setup independent of
Buzz recovery; C3 secret-safe `remote profile add` with TLS/origin pinning and no
cross-origin bearer redirect; explicit --profile/--project/--task/--session/--budget/
--continuation flags; task-required --mode working; authorized queue/loss/policy and
capability status; O2/O4 supported-major upgrade/rollback checks. These correct
interfaces and onboarding within approved scope, without adding a product feature.

| Approved design requirement | Tasks |
| --- | --- |
| Graph authority, schema, concurrent revisions and jobs | F1, F3, F5, I1 |
| Single-owner identity, scope, trusted evidence, source privacy | F2, F4, F5, I1, I2 |
| Automatic filtered capture, pauses, exclusions, offline recovery | C1, C2 |
| Bounded orientation, graph expansion, checkpoint restoration | F4, F5, C2, C3 |
| Project verification, cross-project lessons, invalidation | I1, I3, O1 |
| Buzz owner decisions and recovery access | I2, O4 |
| Dreaming, model cost limits, digests, Hermes conversation | I3, I4 |
| Migration, export, retention, erasure and backup resurrection | C4, O1, O2 |
| Railway security, telemetry, reproducible restore | O2, O4 |
| Fair usefulness tests and actual private acceptance | O3, O4 |

Self-review checked dependency order, interface ownership, missing domain types,
five review-focus tests, complete spec coverage, and local/remote compatibility.
The 17 tasks each include a red/green cycle, named assertions, implementation files,
interfaces, and an atomic commit. Versions are deliberately qualification outputs,
not untested pinned guesses. The implementation has not started; earlier 481 passing
baseline tests apply to unchanged existing code, not the proposed system.

## Execution handoff

- [ ] Owner reviews these linked plans and chooses execution method.
- [ ] Execute F1-F5, then C1-C4 and I1-I4, then O1-O4.
- [ ] Run `/plan-devex-review` before API implementation; `/devex-review` after clients work; `/cso` for auth/capture/storage boundaries.
- [ ] Run `/review` before PRs; `/ship` prepares them; `/land-and-deploy` follows approved release scope, with no admin merge bypass.
- [ ] Complete actual private acceptance and document gaps; do not equate test passes with live acceptance.

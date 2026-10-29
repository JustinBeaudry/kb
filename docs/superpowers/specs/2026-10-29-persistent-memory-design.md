# Private graph-based memory service

Date: 2026-10-29
Status: Draft for owner approval; implementation is not authorized by this document.
Owner: Justin Beaudry
Scope: KB evolution, one owner, Railway deployment, multiple agent harnesses.

## Intent

Turn KB into a private, durable memory service that understands the work currently
being done. Agents should discover relevant internal APIs, project decisions,
versions, verified outcomes, failed approaches, and transferable lessons without
loading unrelated history. Preserve enough evidence to correct mistaken memories.
Provide a conversational agent for exploring accumulated knowledge, with background
discovery of connections and contradictions.

The graph is authoritative. Letta is a source of context-management and background
consolidation patterns, not a parity target or a replacement storage architecture.

## Decisions made with the owner

- One owner for now; no team administration or multi-tenant product.
- Every harness should be able to connect through portable interfaces. Automatic
  capture and restoration depend on each harness's actual integration capabilities.
- Authoritative graph storage, separate object storage for large evidence; no
  separate document database unless a demonstrated requirement warrants it.
- Go is preferred for the API. No established requirement justifies Rust.
- Task-local findings can be captured freely. Shared project knowledge requires
  verification. Agents may promote automatically when direct evidence supports it.
- Cross-project lessons are published asynchronously after verification,
  applicability, and sharing checks. Ambiguous or sensitive cases go to the owner.
- Buzz handles human decisions. Hermes or a comparable runtime supplies conversation.
- Dreaming discovers connections, contradictions, stale claims, and possible lessons.
  A conversational agent can explore and explain those findings.
- Capture filtered conversation and execution history automatically in the
  background, with per-project exclusions and pause-capture controls.
- Capture is evidence collection, not permission to treat transcript claims as true.

## Goals, non-goals, and failure conditions

Goals:

1. Resume work with the right objective, state, constraints, and verified history.
2. Find version-applicable API knowledge and avoid repeating known failed attempts.
3. Discover useful cross-project lessons while respecting project source boundaries.
4. Bound active context and make retrieval omissions and uncertainty explicit.
5. Support concurrent agents without silent overwrite or inference contamination.
6. Trace shared claims to evidence, revise them, and invalidate dependent conclusions.
7. Remain usable during capture, provider, Buzz, and backend outages.

Non-goals for the first release: Letta parity; a new general agent harness; autonomous
code execution to verify arbitrary transcript commands; universal native adapters
for every existing tool; team access; a graph visualization product; replacing all
Markdown workflows; automatic bulk upload of historical vault sessions.

Failure even if deployed: irrelevant context grows unbounded, two agents silently
overwrite shared knowledge, an LLM assertion becomes verified through repetition,
private project details leak through shared lessons, or deployment health is reported
as proof that memory improves agent outcomes.

## Existing system and migration boundary

The inspected checkout is KB v0.7.0 at commit 8b0de6f. It is a Bun/TypeScript CLI
over a local Markdown vault. It provides map/get-node retrieval with source ranges,
complete-wire byte budgets, session manifests, summarization, source access gates,
and optional SessionStart/PostCompact hooks. It has no hosted API or graph store.

Relevant patterns: `src/lib/retrieval.ts`, `src/lib/envelope.ts`,
`src/lib/path-safety.ts`, `src/commands/capture-session.ts`, `src/lib/transcript.ts`,
and `src/hooks/inject.ts`. Existing evaluation documentation distinguishes deterministic
CLI contracts from actual agent task outcomes. Preserve that distinction.

Retain the existing local CLI and vault behavior. Add an explicit remote profile;
never silently upload a selected local vault. Local mode remains available when no
remote profile is selected. Migration is a dry-run inventory followed by an explicitly
selected import. Existing curated text supplies candidates with provenance, not an
automatic verified stamp. Historical raw/session upload is a separate opt-in import.
Do not mutate or consume the currently frozen retrieval-control pilot as a new trial.

## Approaches considered

| Approach | Benefit | Cost / decision |
| --- | --- | --- |
| Hosted Markdown vault | Least migration; directly editable files | Coordination and claim-level verification are awkward; not selected |
| Relational or document store as primary memory | Familiar structured records and transactions | Owner prefers connected facts; not selected as the authoritative model |
| Authoritative documents with a derived graph | Rebuildable graph | Adds synchronization and stale permission/correction problems; not selected |
| Authoritative graph plus object storage | Relationships, evidence, and claim lifecycle in one model | Requires deliberate concurrency and bounded traversal; selected |
| Synchronized local vaults as primary storage | Offline editing | Adds bidirectional conflict handling; deferred |

Reuse an existing conversational runtime rather than build one. Hermes is the first
integration candidate, not a requirement to adopt its independent memory as authority.
Borrow Letta's small active context, indexed deeper memory, background consolidation,
and recoverable changes. Do not implement MemFS or replicate the Letta platform.

## System responsibilities

- **Harness collector:** normalize exposed conversation/tool events, apply local
  exclusions/redaction, queue permitted events, upload asynchronously and retry.
- **Go API:** authenticate, resolve scope, accept evidence/candidates/checkpoints,
  serve bounded retrieval, manage decisions, policies, revisions, and deletion.
- **Go workers:** process durable jobs for extraction, verification, promotion,
  dreaming, index updates, garbage collection, and Buzz delivery.
- **Graph store:** authoritative identities, claims, relationships, revision pointers,
  verification records, decision records, job/outbox state, and evidence manifests.
- **Object store:** private filtered transcript chunks, API specifications, source
  snapshots, and large execution artifacts. No public object URLs.
- **Indexes:** rebuildable search projections; never permission or truth authorities.
- **Conversational agent:** retrieve, explain, compare, and propose corrections using
  the same API; no direct graph/object credentials or special verification bypass.
- **Buzz gateway:** minimal decision/digest delivery and authenticated owner responses.

Layering follows Handler -> Service -> Repository/Gateway -> I/O. Service interfaces
are defined at consumers; services own transaction boundaries. `cmd/server` and
`cmd/worker` wire dependencies. OpenAPI is the HTTP source of truth; generated strict
handlers and request validation enforce it. All I/O takes context.Context. One LLM
gateway normalizes provider outputs and owns generation instrumentation.

The TypeScript CLI remains in place; a new Go service can coexist in a bounded
subdirectory. Exact module layout is a builder choice, not a wholesale CLI rewrite.

## Graph model and trust

Core entities: Owner, Client, Project, Task, Session, Event, Artifact, Entity,
ClaimRevision, Verification, LessonRevision, Decision, Checkpoint, Job, Publication.

An Entity represents an API, component, dependency, person, or other stable identity.
Factual statements are ClaimRevisions, not unqualified properties casually overwritten
on Entity nodes. A relation such as USES_VERSION or FAILED_BECAUSE is itself an
assertion requiring provenance and verification. Storage edges such as SUPPORTED_BY,
ABOUT, SUPERSEDES, and DERIVED_FROM express record structure. A failed execution does
not automatically verify its inferred cause.

Every factual revision carries: stable claim identity, revision ID, project/task
scope, originating client, source event/artifact references and exact locators,
recorded time, observed/valid time where known, applicable versions/environment,
verification policy/version, status, and supersession/dependency references.
Unknown dates or versions remain unknown. A transcript or citation hash proves
identity/integrity, not factual truth.

Lifecycle: candidate -> verified -> superseded/disputed/invalidated. Rejection is
recorded separately. Verification means supported under the recorded conditions,
not universally true. Task-local hypotheses can be read only through an explicitly
unverified task-working-memory view; they are excluded from ordinary shared retrieval.
Routine agent submissions cannot set the verified status or rewrite owner decisions.

Preserve provenance through summaries. Derived text retains claim dependencies and
does not gain independent evidentiary weight. Repeated observations of the same
source are deduplicated, not counted as corroboration.

## Verification and promotion

| Claim | Evidence sufficient for automatic promotion |
| --- | --- |
| Owner preference/decision | Authenticated owner statement with exact source; ambiguity goes to Buzz |
| Dependency version | Manifest/lockfile snapshot at the relevant revision; installed version distinguished from declared range |
| API contract | Versioned specification/source; runtime behavior requires separate execution evidence |
| Test outcome | Collector-recorded command, exit status, relevant output and workspace revision; agent prose is insufficient |
| Deployment or integration behavior | Target/environment-specific observation; local tests do not substitute |
| General lesson or causal explanation | Supporting verified findings plus explicit applicability; unsupported generalization remains a candidate |

Structured validators perform mechanically checkable verification. LLM workers may
extract, match evidence, and propose relationships, but model agreement/confidence
alone never verifies a claim. When evidence cannot establish a causal/general claim,
publish only the narrower observation if justified or request owner adjudication.
An owner-approved judgment is labeled as such, not relabeled as an empirical test.

Evidence authenticity is a separate gate from content validation. Ordinary agent
clients may submit candidates and attributed history, but cannot impersonate a
trusted collector, verifier, or owner by setting event fields. Trusted execution
evidence must be observed by a qualified collector/supervisor outside the agent's
write authority, using a separate ingestion capability inaccessible to its tools.
It binds the actual invocation, result, workspace state (including dirty changes),
and capture identity. A user-editable transcript or agent-supplied exit code alone
does not meet this requirement. Signing establishes origin only when the signer
itself has this isolation; signatures do not make arbitrary content true.

Where a harness cannot provide that boundary, capture remains useful but execution
claims stay candidates until independently checked by an approved read-only source
gateway or adjudicated by the owner. Do not invent stronger trust to satisfy an
automation target. Qualify these capabilities per harness before enabling promotion.
Version/contract snapshots obtained independently by a read-only gateway can support
their own narrow claims without trusting an agent's supplied copy.

A captured message labeled `user` is not automatically an authenticated owner
decision. Automatic preference/decision promotion requires a direct authenticated
owner channel (initially enrolled Buzz identity), or a separately qualified harness
attestation that distinguishes owner input from agent-generated content. Otherwise
it stays a proposed owner statement pending confirmation. Owner capabilities are
never issued to model tools; the recovery CLI/API must use a separate interactive
owner-authentication flow. These are conservative design choices to satisfy the
agreed anti-poisoning requirement, not a claim that existing harnesses attest events.

Verification does not run arbitrary commands from a transcript. First-release
verification consumes evidence and read-only approved source connectors. Further
active checks require a separately bounded execution capability.

Project promotion requires supported evidence and a current revision check.
Cross-project promotion produces a separate lesson with applicability, counterexamples,
source lineage, and an explicit sharing scope. A generic lesson may refer to restricted
evidence without exposing identifiers or source content to clients lacking access.
Sensitive or ambiguous generalization stays unpublished pending Buzz.

When a source or supporting claim is invalidated, dependent publications must be
excluded immediately by authoritative dependency checks; background recomputation
can follow. Never wait for search-index refresh to enforce invalidation.

## Concurrent agents and durable work

Each agent/task owns checkpoints and hypotheses. Agents append immutable events and
candidate revisions with client/session/event idempotency keys. Payload mismatch on
a reused key is a conflict, not a successful retry.

Shared promotion performs an atomic expected-revision check and validates supporting
revisions. A changed head causes reread/reconciliation; no blind last-writer-wins.
Conflicting claims are retained and flagged rather than destructively merged.
Historical revisions are immutable except explicit deletion/redaction policy.

Graph transactions atomically record publication state and outbox work. Workers use
leases, fencing tokens, bounded retries, dedupe keys, and a failed-job view. Delivery
is at-least-once with idempotent effects, not an exactly-once promise. Late workers
cannot commit after losing their lease or after evidence was invalidated.

Object upload is staged: validate filtered content/hash, persist object, then commit
its usable manifest and extraction job. Incomplete uploads never become evidence.
Reconciliation removes abandoned uploads and detects missing referenced objects.
Graph/object writes are not presented as one distributed transaction.

## Retrieval and context management

Request context includes client identity, registered project, task/session, objective,
component/entity hints, repository revision/version constraints, and output budget.
Server authorization intersects requested scope; caller-supplied project IDs are not
authority. Explicit project bindings outrank inferred directory names. Ambiguous
scope returns a compact clarification requirement, not a global search dump.

1. Orient with a compact index of relevant verified records and current task state.
2. Find starting records through exact identifiers, lexical search, and optionally
   semantic search. Graph connections alone are not proof of relevance.
3. Expand selected relation types with depth/fanout/time limits, permission checks,
   applicability checks, and a shared output budget at every step.
4. Return concise claims, evidence IDs/locators, revision/freshness metadata,
   conflicts, and structured continuation. Fetch source passages only as needed.

Cross-project discovery searches published lessons whose conditions match the task;
it does not grant unrestricted traversal of other projects. Contradictions are
short, explicit flags; absence of evidence is distinguished from an unavailable source.

Proposed initial defaults (builder-adjustable after measurement): 4 KiB complete-wire
orientation, 16 KiB complete-wire evidence retrieval, depth 2, maximum 50 visited
records. Reserve space for mandatory warnings/provenance; return an explicit budget
error if even the minimal envelope cannot fit. Continuations are bound to scope,
revision/snapshot, and authorization. Token estimates supplement the hard byte cap;
do not pretend a model-independent token count is exact.

Checkpoints record objective, completed/verified work, pending steps, unresolved
questions, active revisions and evidence references. They are separate from global
facts. Refresh on meaningful task changes and before compaction when supported;
restore from the latest valid checkpoint on resume. A stable context package can be
reused until relevant changes occur, with invalidations checked on retrieval.

## Automatic capture and privacy

Capture new, exposed user/assistant messages, tool calls/results, and available
revision/environment metadata. Do not seek hidden reasoning, read credential stores,
scan arbitrary filesystem paths, or imply every harness exposes identical events.
Unsupported, malformed, filtered, truncated, or missing events have explicit markers.
The current conversation-only transcript parser is not sufficient for execution proof.

Local policy runs before anything reaches the network or upload spool: deny sensitive
paths/fields, redact known configured secret values and recognizable patterns, and
apply project exclusions. Unknown secrets can evade heuristics; the product must
offer capture preview, pause, exclusions, and deletion rather than claim perfect scrub.
Suspicious content is withheld locally with a metadata-only warning, never copied to
Buzz for inspection. Local uploads use restrictive file permissions and OS-backed
credential storage. Graph/object content is data, never executable agent instructions.

Pause stops new capture and queued transmission; previously stored records remain.
Resume does not backfill paused intervals automatically. Exclusions are rechecked
before queued upload. Bound the spool; report overflow/loss, never silently drop it.
Capture failure must not block the user's foreground agent.

Proposed retention defaults, not yet owner-selected: raw filtered history 90 days;
inactive task working state 30 days; verified knowledge until superseded/deleted.
Evidence excerpts supporting retained claims are separately pinned with hashes and
locators. If policy forbids keeping supporting evidence, downgrade affected claims
or retain an explicit unverifiable historical record, never pretend evidence remains.
Owner-directed deletion overrides pins and cascades to derived claims, indexes,
objects and pending jobs. Backups expire on a documented schedule; restores apply
deletion tombstones before serving queries. Metadata-only audit records must not
retain deleted content.

## Single-owner security and model access

Use TLS externally and private service connectivity for storage. No public graph
port or public object bucket. Separate revocable credentials per harness/device;
store token hashes server-side and bind permitted operations and projects. Collector
credentials can append evidence but cannot administer clients or promote knowledge.
Verifier, conversational, and owner roles are distinct service capabilities.

One owner does not mean every client gets every project. Owner conversation can
explicitly request broader authorized inspection; ordinary task clients receive
only their project scope and published shared lessons. Source fetch enforces the
same policy independently of graph navigation and opaque IDs.

Use encryption at rest for objects, graph volumes, and backups, with a documented
key-management/recovery procedure. Verify provider capabilities before deployment;
add application encryption where required rather than assert Railway provides an
unverified guarantee. This is server-readable memory for background processing,
not end-to-end encryption against the running service or its administrator.

Model processing is another disclosure boundary. Configure an owner-selected model
provider and project allowlists; send only the filtered excerpts needed for a job.
No silent fallback to a different provider. Jobs lacking an authorized model route
remain pending with a visible reason. Do not persist raw prompts in telemetry by default.

## Dreaming, conversation, and Buzz

Dreaming runs incrementally on new evidence, task boundaries/compaction notifications,
and scheduled batches. Use bounded per-job context, execution time, attempts, and
daily token/cost limits. Store cursors and input revision hashes; replay is idempotent.
Missing usage is unknown, not zero. Budget exhaustion defers noncritical jobs and
does not stop retrieval or durable capture.

Work includes candidate extraction, duplicate detection, contradiction discovery,
freshness checks, qualified cross-project lessons, and context/index maintenance.
Model-generated output cannot certify itself or bypass publication policy.

Hermes is the first conversational runtime candidate. Its memory-provider hook is
an integration point to qualify against a pinned version. Built-in agent notes are
private working state, never a second authoritative shared store. If hooks are
insufficient, use the API/MCP tools and report the unsupported automatic behavior.

The agent explains findings with evidence, explores alternatives, and submits
corrections through the ordinary lifecycle. Its answers reference their sources;
capturing an answer does not turn it into an independent observation.

Buzz decisions bind owner identity, candidate revision, evidence digest, requested
action, and expiry. Authenticate signatures/identity, deduplicate replies, and reject
replayed, expired, or stale approvals. A decision about one revision does not approve
a later one. Outage leaves decisions pending while independent work continues.
An authenticated owner CLI/API can inspect pending decisions and serve as recovery.

Proposed delivery policy: immediate Buzz notice for a verified issue affecting active
work or a pending owner decision; other findings go into a daily digest, accessible
on demand. Deduplicate by finding/revision and suppress no-change notices. Route and
schedule are configured during onboarding, not guessed from other projects.

Software Factory currently contains `internal/gateway/buzz.gateway.go` and
`internal/gateway/nostr.go`, plus runbooks and tests. These are local reference patterns
for CLI isolation and signature verification, not evidence of a KB integration or
permission to reuse Software Factory identities. KB gets its own bot credentials.

## Portable interfaces and first-release coverage

API capabilities: append events/artifacts; create/update task checkpoints; submit
candidates; orient/search/fetch evidence; inspect provenance/conflicts; manage capture
policies; process owner decisions; revoke clients; delete/export scoped knowledge.
Transport DTOs are separate from domain types. MCP and CLI call the same services.

Proposed first qualification matrix: generic CLI/API client, Codex capture/restore,
Claude Code capture/restore, and Hermes conversation/provider integration. This is a
builder-proposed rollout order, not a promise that every native hook already exists.
Additional harnesses use the common contract; native automation is qualified one at
a time. Report capture, restore, tool evidence, and interruption recovery separately.

Remote CLI retrieval preserves source citation, explicit truncation, and budget
semantics. Any envelope evolution is versioned. No silent remote-to-local substitution
that presents stale local facts as current server facts.

## Railway operations and remaining technical selection

Deploy API, worker, graph, and conversational runtime as separately restartable
services where required. Use persistent graph storage and private object storage;
ephemeral container disks are not durable storage. No deployment resources are
created during design. Start with a single-owner scale target, not an HA cluster claim.

The graph engine remains a builder selection, with Neo4j a candidate, not an approved
dependency. Before implementation depends on it, record an ADR and qualify: supported
Go driver; atomic expected-revision promotion under races; constraints and locking;
job leases/outbox; bounded traversal; search/index options; Railway private networking
and persistence; license suitability; backup/restore and resource cost. A document
store or relational authority would reopen the approved architecture, not be a silent
fallback. A native search feature is preferred over another search service when it
meets the measured need. Graph selection must not assume vector search is necessary.

Select an object-store provider using private access, integrity, retention/deletion,
encryption, and Railway connectivity requirements. Verify official current docs and
an actual restore test before claiming durability. Keep logical graph/evidence backups
consistent via manifests and restore checks. Proposed daily backups with 30-day
retention; RPO <=24h and RTO <=4h are targets to validate, not current guarantees.

Structured logs redact centrally; request IDs propagate through gateways. OTel covers
HTTP/storage; the LLM gateway records model, usage, cost, and policy revision. Track
capture lag/loss, pending verification, invalidation lag, budget exhaustion, retrieval
bytes/latency, Buzz decisions, and backup health. Do not log source content or tokens.

## Acceptance and evaluation

Security/correctness gates precede any usefulness claim:

- Concurrent conflicting writes cannot silently replace the same head; stale workers
  and approvals cannot publish. Crash/retry tests preserve dedupe and evidence links.
- Unverified inference, poisoned source instructions, and repeated summaries cannot
  enter verified shared results. Relationship assertions receive the same treatment.
- Cross-project discovery returns only published, applicable lessons. Unauthorized
  source IDs, continuations, cache entries, and derived summaries reveal no content.
- Source corrections/deletion immediately exclude dependent claims even with stale
  indexes; restoration honors deletion records.
- Capture redacts seeded known secrets before upload/spooling, honors pause/exclusions,
  reports unsupported events and overflow, and recovers from offline/restart states.
- Buzz accepts only the enrolled owner's valid response for the exact pending revision.
- Budgets cover complete output and graph traversal; evidence truncation is explicit.
- Restore a graph/object backup and verify referenced evidence plus deletion behavior.

Use TDD for services/handlers and real selected-graph integration tests for concurrency,
transactions, and durability. Mock only repository/gateway boundaries. Go gate:
`go vet ./... && go test -race ./... && golangci-lint run`. Changed TypeScript gate:
`bun run lint && bun run test`. Enforce 80% coverage for the relevant maintained code.
If a Python adapter is necessary, use uv/Ruff/mypy/pytest and its prescribed gate.

Measure usefulness with paired fresh-context tasks using the same corpus, model,
tools, and resource limits: ordinary file search, current KB, then graph-enabled KB.
Include version drift, failed attempts, contradictory evidence, absent facts,
cross-project transfer, context compaction, and misleading links. Keep gold answers
hidden. Score task success, evidence recall, grounding, safe abstention, context bytes,
tokens/cost when available, and latency separately. Pre-register thresholds in the
implementation plan before trials. Graph superiority remains unproven until measured.

A release requires private end-to-end acceptance across the initial harness matrix,
capture-to-verification-to-retrieval, Buzz round trip, conversational citations,
offline recovery, and restore. Healthy Railway services are only deployment evidence.

## Proposed delivery sequence and explicit builder choices

This is a project, delivered in reviewable increments:

1. Qualify graph/storage/Buzz/harness boundaries and pin versions; record ADRs and costs.
2. Build authenticated Go services, evidence capture, graph lifecycle, and bounded retrieval.
3. Add verification workers, promotion, invalidation, and Buzz decisions.
4. Add dreaming and the conversational integration; import only selected existing material.
5. Run paired evaluations, private acceptance, restore exercises, and operational rollout.

Builder choices requiring no new product decision: exact graph and object providers
within the qualification gates; directory/module layout; generated client tooling;
index implementation; scheduling library; initial model routes selected from the
owner's configured providers; tuning within the agreed budgets. New external paid
commitments and unavailable credentials must be surfaced concretely during planning.

Proposals for owner review: initial retention durations, first harness qualification
order, digest cadence, recovery targets, and initial context budgets above. Operational
credentials, provider/model budget, Buzz channel/identity, and imported project scopes
are deployment configuration prerequisites, not reasons to fabricate successful checks.

After approval of this design, create the implementation plan using
`superpowers:writing-plans`. No product code, dependencies, or cloud resources are
created by this design step.

## Builder check

A fresh read-only builder review asked which events can count as trusted execution
evidence or authenticated owner statements. The design now separates ordinary
submissions from qualified collectors and owner channels, and explicitly keeps
unattested events as candidates. This resolves the question using the agreed rule
that direct evidence, rather than agent inference, gates shared knowledge.
The reviewer reread the amended document and reported "nothing important left".

Design-stage validation: whitespace/required-section checks passed. The existing
application at the matching base commit passed `bun run lint` and
`bun run test --reporter=dot`: 481 passed, 0 failed, 3,380 assertions across 44 files.
These checks ran in the original checkout using its existing dependencies; this
isolated worktree changes only this design document. They do not validate the
proposed service, which has not been implemented.

## References inspected

- Local KB README, capture/retrieval source, and `evals/README.md` (2026-10-29).
- Local Software Factory Buzz gateway and onboarding runbook (2026-10-29).
- Letta MemFS: https://docs.letta.com/concepts/memfs
- Letta conversations: https://docs.letta.com/concepts/conversations
- Letta dreaming: https://docs.letta.com/configuration/memory
- Hermes memory providers: https://hermes-agent.nousresearch.com/docs/user-guide/features/memory-providers
- Hermes memory: https://hermes-agent.nousresearch.com/docs/user-guide/features/memory
- Mem0 extraction comparison: https://docs.mem0.ai/core-concepts/memory-operations/add
- Neo4j concurrency qualification reference: https://neo4j.com/docs/operations-manual/current/database-internals/concurrent-data-access/

External capabilities must be rechecked against pinned versions during planning.

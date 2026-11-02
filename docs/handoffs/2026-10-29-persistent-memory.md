# Persistent memory implementation handoff — paused 2026-10-29

## Pause and resume authority

Justin explicitly requested: “Lets pause for now (as we're running out of usage). Write a handoff.” Work is paused by request, not complete. Do not resume implementation until he asks. Active C1 worker was interrupted and asked only to save its current state. Preserve all uncommitted files. No formal goal-tool goal exists.

When resumed, continue the already approved design and 17-task implementation plan without asking for approval again between tasks. Budget configuration was explicitly deferred (“Lets not worry about that rn”); do not repeat that question during local implementation or infer an unlimited/numeric paid-model budget.

## Workspace and source of truth

- Implementation worktree: `/Users/justinbeaudry/Projects/kb-memory-design`.
- Branch: `docs/persistent-memory-design`.
- Current committed HEAD and C1 review BASE: `f6bd6f5`.
- Original checkout: `/Users/justinbeaudry/Projects/kb`; preserve its unrelated untracked work, especially `.gstack/` and earlier evaluation/review plans.
- Approved spec: `docs/superpowers/specs/2026-10-29-persistent-memory-design.md`.
- Master plan: `docs/superpowers/plans/2026-10-29-persistent-memory.md`; linked foundation, capture, intelligence and operations plans contain tasks.
- Authoritative execution ledger: `.superpowers/sdd/2026-10-29-persistent-memory/progress.md`.
- That scratch directory also holds task briefs, detailed reports, test/coverage logs, review diffs, contracts and rulings. It is gitignored: do not delete/clean it.
- Read Superpowers SDD `references/resuming.md` before resuming. Skill root: `/Users/justinbeaudry/.codex/plugins/cache/openai-curated-remote/superpowers/7.0.1/skills/subagent-driven-development/`.

## Approved product boundaries

Private personal graph memory service on Railway. Authoritative Neo4j graph, Go API, private S3-compatible evidence storage; existing Bun CLI local schema2 preserved, explicit remote schema3. No Postgres/document DB/Redis/vector service by default. Letta is a reference, not a parity target.

Task/project scoped bounded retrieval, evidence-backed verification, immutable revisions and optimistic concurrency, automatic filtered capture and offline spool, async lessons/dreaming, Buzz owner decisions, Hermes conversations. Ordinary agent history and model conclusions remain submitted candidates. Repetition or a `user` role field cannot verify a fact. Trusted execution needs an independently qualified isolation boundary; otherwise use read-only source gateways or owner adjudication.

Limits: orientation4096B and evidence16384B complete wire, depth2, max50 examined expansion records, separate support closure cap1000. Capture chunk8MiB, proposed logical event32MiB, spool256MiB. Raw history90days, inactive task30days, pinned retained evidence, daily backups retained30days, RPO24h/RTO4h validation targets. No historical import unless selected. No paid app-model calls or new cloud resources enabled.

## Completed and reviewed

Tasks F1–F5 (numeric1–5) are complete in the ledger. Do not redispatch them.

- F1 graph schema/transaction qualification: `8354a2f`, cleanup `0c4cc6c`.
- F2 authenticated identity: `9c7e211`, Nostr owner-key fix `715643e`.
- F3 durable fenced jobs: `129333e`, real subprocess-contention fix `93c63cd`.
- F4 staged evidence/capture/checkpoints: `8187e2a`, cleanup/reconciliation fix `5aa6467`.
- F5 bounded retrieval/candidates/checkpoint restoration: `560954d`, scoped applicability/health fix `0a40a9b`.
- Parent contract docs through `f6bd6f5`.

Last completed Go proof at `0a40a9b`: vet, selected race suites and lint passed; fresh maintained coverage2082/2500=83.28%, generated-only exclusion. This evidence does not cover current uncommitted C1 changes. Full details are in task-5-report.md and fix logs. Existing TS baseline481tests/3380assertions predates C1.

## Active task C1 — incomplete, uncommitted, not reviewed

Worker identity: `/root/memory_c1_implement` (gpt-6.1-sol high, fresh context). Its restart handoff should be `.superpowers/sdd/2026-10-29-persistent-memory/task-6-paused.md`; read it before restarting commands. Requirements: task-6-brief.md; integration context: task-6-handoff.md. No task-6 final report/commit/review yet at pause.

Observed modified files: server OpenAPI/generated handler, server composition, identity handler/service, src/cli.ts, tests/capture-session.test.ts. New files include policy service/handler/tests, repository collector integration test, src/capture/, src/commands/capture.ts, src/repositories/, src/services/, capture tests and fixtures. These are worker-owned WIP; inspect `git status --short`, do not reset or overwrite.

Worker reported26 focused tests green, including actual directory-substitution and kill/restart lock/journal recovery. Actual HTTP policy checks returned authorized200, forbidden403 and missing-policy behavior. Full real collector lost-ack integration has NOT passed: two attempts hit cold S3 Put5s timeout; readiness precondition was added, then Docker stopped before qualification could run. Treat all as interim worker-reported evidence, not final acceptance.

Current work includes whole-event filtering before serialization/chunking, stable chunk/event IDs, bounded metadata-only loss ring, secure spool and OS credentials, upload retries, and authenticated GET policy. C1 explicitly owns adding project-scoped `policy:read` for collector/agent/verifier enrollment and using the real F4 policy port; O1 owns policy mutation. Current policy never defaults permissively.

Native boundary ruling: macOS `/dev/fd/<dirfd>/payload` failed real qualification. Narrow POSIX operations via Bun FFI/Bun.cc were approved to anchor openat/mkdirat/renameat/unlinkat/locking to directory FDs with no path fallback. Darwin arm64 variadic openat corrupted creation modes through direct FFI; worker added header-free fixed-arity C11 wrapper, then qualified0700/0600 modes/restart. Initially support only exercised macOS arm64; unsupported systems explicitly return storage_unavailable. Audit signatures/constants, buffers, FD cleanup, regular-file/UID/mode checks and compiler/runtime requirements. Bun1.4.3 native Bun.secrets was qualified from docs; no real credential reads during development.

Pause concurrency work: separate native dispatch flock. Flush holds lease; pause persists marker and cancels local work first, then succeeds only after dispatch drains. Busy must retain pause marker and disclose possible in-flight transmission. Parent requested separate-process flush/pause/restart proof, not only AbortController tests. Remains to be checked in final report/review.

## Local Docker interruption

Third synthetic collector fixture failed Neo4j startup with exit143 and Docker socket disappeared. No parent prune or broad cleanup occurred. Parent ran `open -a Docker` to start installed Docker Desktop; process inventory subsequently showed backend/Desktop running. Daemon readiness was NOT verified before user pause. Host had36GiB free. The earlier `docker desktop status` command ended exit1 (“not running”); no parent unified exec session remained from that check.

On resume check daemon and worker's saved owned fixture/process inventory before restarting anything. Do not stop unrelated containers/processes or prune storage. Do not resume expensive suites while paused.

## Next task order and handoffs

Finish C1, run appropriate TS and changed-Go gates, then independent task review. After clean review:

1. Task8 C3 remote CLI/MCP.
2. Task7 C2 Codex/Claude/generic adapters.
3. Task9 C4 selected import/export.
4. Tasks10–13 I1 verification/publication, I2 Buzz/recovery, I3 budgeted dreaming, I4 Hermes.
5. Tasks14–17 O1 lifecycle, O2 deployment/recovery, O3 evaluation, O4 private rollout/acceptance.

C3 precedes C2 because adapters must consume the actual remote client. task-8-brief.md is refreshed, task-8-handoff.md prepared; refresh it with final C1 credential/transport interfaces. C3 additionally owns bounded passages from existing large artifacts and explicit scoped working-evidence fetch. Preserve ID-only denial for task artifacts. Both retrieval/evidence capabilities, current client-owned eligible task reference, pre/post I/O authorization/generation/epoch validation, UTF8 byte spans and actual HTTP/CLI/MCP complete framing tests required. Do not recapture large artifacts to sidestep passage support.

C3 kb-mcp on harness devices uses scoped API credentials, never graph/object credentials. It may use a thin Go remote service/gateway to reach the same authoritative server services. Tool arguments cannot create a trusted Principal. Status should move behind authenticated StatusReader for HTTP/MCP reuse; future job/model status belongs I3.

## Important existing integration contracts

Read `docs/adr/memory-retrieval-contract.md` before I1/O1/C3 changes.

- OwnerState write lock acquired before mutable shared reads; explicit nonretrying transaction, services own transactions, no external object/model/Buzz IO or nested Within in callbacks.
- Current-grant checks and epoch fencing are mandatory. Enrolled AuthEpoch bug fixed/reviewed in F5.
- Owner identity is canonical BIP340/Nostr secp256k1 x-only64hex, not Ed25519/base64. Live bootstrap must await I2 interrupted-transfer recovery acceptance.
- Artifact generation, upload lease, abandoning/abandoned states fence retries/cleanup. SourceLocator exact, SourcePath canonical relative slash identity. Exclusions are exact/path-prefix, no globs.
- ObjectAvailable is last-confirmed integrity/presence: transient transport/read/Close errors preserve health; confirmed missing or digest/length mismatch fences false. Never restore stale health over a newer known-missing observation.
- Retrieval HMAC key KB_RETRIEVAL_HMAC_KEY is64hex/32bytes, required at startup. Working hypotheses may be evidence-free, but stay unverified/submitted/task-scoped.
- EntityIDs are ABOUT identities, not revision IDs. Query handles exact claim/revision IDs plus lexical matching.
- I1 creates new project verified targets and distinct generalized cross-project lessons. Immutable task revisions remain task-scoped supporting findings. Preserve applicability; validate generalized target text/entities/conditions for sharing. Hidden source metadata cannot leak. Requested scope is independent of principal's broader grants.
- O1 must reconcile retained evidence on policy changes, not permanently hide all history for harmless revisions or grandfather newly forbidden evidence. Deletion journal/restore barriers remain required.

Deferred minor test observations for final branch review: F3 rollback test omits original job state/finish-failure assertions; F4 reused-event-key test hits earlier digest/reference validation rather than actual conflict. See ledger; no open Critical/Important in completed tasks.

## Railway read-only preflight and external acceptance

Personal Applied Labs workspace b3164d80-3241-46a8-be72-5dff5dcb6132; no KB project found among17 accessible projects. Existing Buzz project is not permission to reuse its identity/credentials/channel. No cloud mutation, credential extraction, push, PR, deploy, app-model call or external message occurred.

Read task-15-provider-preflight.md for current official documentation research. Railway documents managed AES256 at rest, but bucket access is authenticated public HTTPS only (no private network endpoint); new bucket URLs virtual-hosted while F4 hardcodes path-style. No S3 SSE-config API/versioning/lifecycle/automatic bucket backup. Platform daily snapshots retained6days, not daily30day archive. Current Go config rejects nonloopback HTTP; qualify actual Railway origin transport rather than silently relax TLS. Nonroot volume ownership and encrypted backup/key recovery also need qualification.

O4 needs concrete selected project/region, spending config, KB-specific Buzz identity/channel, authorized model routes/daily cap, scopes/exclusions and live qualification. User deferred budgets now; complete local implementation first. O3 cannot claim usefulness without actual paired trials; the frozen prior pilot in original checkout is unexecuted and must remain untouched.

## Execution/review workflow

Fresh implementer per task, one implementation worker at a time. Use explicit model per SDD; prior implementers gpt-6.1-sol high, high-risk reviewers gpt-6-astra high. Workers/reviewers may not spawn agents. Review uses full task BASE..HEAD diff file and separate spec/quality verdicts. Read handling-reports.md for every DONE_WITH_CONCERNS and fix-loop.md each fix round; original implementer first3 rounds, cap5, scoped re-review. Read cannot-verify.md before closing cross-task observations. Final whole-branch /review, /cso, /devex-review and finish workflow still pending.

Go gate: vet, race tests, `go tool golangci-lint run --allow-serial-runners`; current Go1.27.3. TS gate: `bun run lint && bun run test`. Keep >=80% maintained coverage including roots/graph; generated-only exclusion. Use exact current evidence, avoid unnecessary reruns of unchanged expensive graph backup qualification.

Time tracking earlier unavailable due missing PLANE_API_KEY; no active timer. No memories were updated. Keep private vault/raw/sessions untouched; no automatic extraction/import.

## Final worker snapshot received after pause

Worker confirmed all implementation/testing stopped, no known active command. Read `task-6-paused.md` for exact diagnostic sessions and owned failed fixture IDs before any cleanup. Latest focused TS result supersedes the interim26 count: **29 pass,0 fail,134assertions** in `/tmp/kb-c1-green-metadata.log`. Full TS/Go gates and current coverage remain pending. Preserve `/tmp/kb-c1-*.log` evidence if cleaning temporary files later.

Worker self-review identified concrete unfinished fixes: persistent HTTP409 must not trigger thousands of immediate retries; preserve unauthorized/revoked/forbidden policy errors; route CLI status through service and expose/validate spool quota; reject duplicate event IDs with different content/scope; tighten persisted schema/quota validation; recheck durable pause after policy read before outbound content; assess redaction cost. Remove integration debug stderr after diagnosis. These are C1 work, not reviewed completion.

Credential reuse detail for C3: native service `com.beaudry.kb.capture`, account=validated explicit profile, default account `default`; bounded stdin enrollment, injected synthetic stores in tests, no real Keychain read/write performed. Read final C1 interface/report after fixes before consuming it.

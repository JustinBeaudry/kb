# Graph: Resume persistent-memory C3

## Inputs
- **TASK:** Resume the interrupted C3 remote CLI and stdio MCP implementation without disturbing existing work.
- **OUTPUT:** Reviewed local implementation and tests, integration documentation, and a current handoff. Worktree: `/Users/justinbeaudry/Projects/kb-memory-design`; branch `docs/persistent-memory-design`.
- **CHECKS:** C3 brief and global constraints; schema-2 local behavior; explicit authenticated schema-3 remote operation without local fallback; credential/origin safety; bounded HTTP/CLI/MCP framing and continuation; scoped working-evidence authorization; actual synthetic end-to-end tests; Go and TypeScript gates; independent review.
- **SOURCES:** `.superpowers/sdd/2026-10-29-persistent-memory/{progress.md,global-constraints.md,task-8-brief.md,task-8-handoff.md}`; `docs/adr/memory-retrieval-contract.md`; existing dirty worktree.
- **SCOPE:** Local C3 implementation, tests, generated API, integration documentation, and this plan. Preserve all pre-existing edits and untracked files. No cloud changes, production data, paid calls, pushes, or deployment.
- **LIMIT:** C3 only; up to three correction attempts per failed gate; leave later tasks for subsequent stages. Stop and report if a prerequisite cannot be satisfied.

## Nodes
| ID | Kind | Reads | Writes | Check | Depends on |
| --- | --- | --- | --- | --- | --- |
| R0 | model | Handoff, dirty tree, implementation | Baseline report | Identify exact interrupted state; no changes | none |
| G1 | model | Go retrieval, repository and C3 contract | Scoped service/repository tests and fixes | Prove/fix suspected passage continuation and citation selection issues | R0 |
| G2 | model | Go MCP handler and framing contract | Scoped MCP tests and fixes | Prove/fix suspected mutation-receipt and continuation issues | R0 |
| G3 | model | Go gateway and wire contract | Scoped gateway tests and fixes | Validate schema-v3 responses and reject malformed data | R0 |
| T1 | model | C3 brief, existing CLI and C1 credential boundary | TypeScript gateway/service/envelope/CLI, profile handling and tests | Explicit remote, safe origin binding, no fallback, complete-frame bounds | R0; integrate after Go interfaces stabilize |
| I1 | model | Completed G1-G3/T1 | End-to-end synthetic HTTP/CLI/MCP tests | Large UTF-8 passages, scope denial, revocation, continuation, actual framing | G1, G2, G3, T1 |
| D1 | model | Accepted implementation and qualification | Integration guide, query guide, README | Accurate setup and limitations; no unsupported MCP or hook claims | I1 |
| V1 | code | Resulting tree | Verification logs | TypeScript and Go gates, generated-code drift and diff checks | I1, D1 |
| R1 | model | Final diff and V1 evidence | Independent review | No unresolved critical/important findings | V1 |
| H1 | code/model | Accepted tree and evidence | checks.md, progress.md, existing progress ledger | Explicit evidence, limitations and next action | R1 |

## Parallel groups
- G1, G2, G3, and T1 can run in parallel only with strictly separate file ownership. Shared wire contracts are coordinated by the lead; no simultaneous edits to shared files.
- V1 gates may run in parallel where resource limits permit. I1 and review follow integration.

## Code nodes
Deterministic commands: focused tests, generation drift checks, `bun run lint && bun run test`, and server `go vet ./... && go test -race ./... && golangci-lint run`; record real output rather than reusing earlier evidence.

## Rules learned
- Existing `.superpowers/sdd/.../progress.md` is newer than the untracked October 29 handoff.
- Preserve local schema 2 and use scoped per-client credentials; MCP devices must not have graph/object credentials.
- A successful bounded response must include complete rendered framing, not just the HTTP payload.
- No paid model use or cloud provisioning is authorized by the deferred budget decision.

## Decisions log
- 2026-10-30: Resumption inspected existing worktree rather than the separate main checkout. C3 remains uncommitted and unfinished; preserve it.
- 2026-10-30: Independent read-only review identified five suspected Go issues. Treat them as hypotheses requiring reproducible tests, not verified fixes.
- 2026-10-30: Pause execution for approval of this scoped graph before modifying existing implementation.

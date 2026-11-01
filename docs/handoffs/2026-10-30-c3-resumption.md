# Handoff: persistent memory C3 resumption

**As of 2026-10-30 17:35 ET. Status: in progress, uncommitted, currently does not build.**

## Worktree and safety

- Work in `/Users/justinbeaudry/Projects/kb-memory-design`, branch `docs/persistent-memory-design`, HEAD `bd09d1b` (`fix(capture): persist policy freshness once per window`). The session's default directory `/Users/justinbeaudry/Projects/kb` is the separate `main` worktree.
- Preserve **all** modified and untracked files. Do not reset, clean, overwrite generated files indiscriminately, commit, push, deploy, or change cloud resources without an explicit decision. C3 changes include pre-existing uncommitted work and this session's additions.
- Read `.superpowers/sdd/2026-10-29-persistent-memory/progress.md`, `.superpowers/sdd/2026-10-29-persistent-memory/task-8-brief.md`, `docs/adr/memory-retrieval-contract.md`, and `docs/plans/2026-10-30-memory-c3-resumption/{graph,checks,progress}.md`. The `.superpowers` ledger is gitignored; preserve it. The older `docs/handoffs/2026-10-29-persistent-memory.md` is historical, not current.
- No cloud operations, paid model calls, live credential use, deployment, or release were authorized or performed in this resumption. No commits were made.

## Immediate blocker: interrupted corrections left the tree broken

Three delegates assigned final review corrections failed/interrupted while editing. Their partial changes remain; there are no active test processes. **The most recent full-suite successes predate these edits and are not evidence for the current tree.** Reproduce from the worktree:

```sh
cd /Users/justinbeaudry/Projects/kb-memory-design/server
go test ./internal/service ./internal/gateway ./internal/handler ./cmd/kb-mcp
cd ..
bun run lint
bun test tests/remote-*.test.ts
```

At handoff, Go compilation fails:

1. `server/internal/service/retrieval.service.go:568`: `passageResult` still accepts a source-revision string, but a partial change passes `domain.EvidenceRef`.
2. `retrieval.service.go:646`: `fetchAuthority.sourceRevision` no longer exists. The partial fix stores `ref domain.EvidenceRef` and `citationBinding` instead.
3. `server/internal/gateway/memoryapi.validation.go:248,313`: references to `service.MaxArtifactBytes` are missing the service import or need a dependency-safe shared constant. Verify import-cycle safety.

The Go service partial change is meant to preserve the selected citation's applicability, not just its source revision, and bind the selected claim/reference conditions across pre/post-I/O checks and cursors. New tests begin around `server/internal/service/retrieval_fetch.service_test.go:659`. Finish and review the implementation carefully; do not merely make it compile while losing source trust or applicability.

The Go gateway partial change adds strict passage continuity checks and `server/internal/gateway/memoryapi.fetch_continuity_test.go`. Finish it, check the relationship to TypeScript validation and actual signed server cursor formats, and test against real HTTP/MCP. Cursor payload inspection is not authentication: the authoritative service still checks the MAC and current authorization.

The latest TypeScript check: `bun run lint` passed; `bun test tests/remote-*.test.ts` had **22 pass, 2 fail**. `tests/remote-cli.test.ts`'s `actual CLI framing...` fixture is rejected after the new request-specific validation; investigate whether the fixture is invalid or the validator is too strict rather than weakening the contract. The real-bridge CLI test fails while compiling the broken Go source. Partial TypeScript changes are in `src/gateways/memory.gateway.ts`, `src/lib/remote-envelope.ts`, and remote tests. The purpose is to reject inappropriate statuses, foreign task checkpoints, or hidden evidence IDs from malformed/compromised upstream responses without rejecting valid F5 server output.

## What was implemented this session

- Explicit `kb remote` credential registration, status, orient, search, fetch and MCP launch; schema-3 framed responses, explicit project/task scope, no local fallback, closed error mapping, and response validation. Local schema 2 is intended to remain unchanged.
- Origin-bound remote credentials live in a separate OS secret service from capture (`com.beaudry.kb.remote` vs `com.beaudry.kb.capture`). The MCP launcher retrieves that credential and passes it to the separately built `kb-mcp` process via inherited FD 3, not argv or environment. Real OS credential stores and platform behavior remain unqualified.
- Go retrieval fixes include task ownership and eligible citation checks before object I/O, current authority and applicability checks around I/O, bounded candidate scanning that fails closed when inconclusive, authenticated passage cursors, and checkpoint continuation handling. Some of this is being corrected again by the interrupted applicability change above.
- Go HTTP response validation rejects malformed schema-3 responses. MCP uses stdio JSON-RPC, with complete success-frame budget checks, bounded IDs, and incremental rather than whole-chain read-ahead pagination. Continuation wrappers bind outer and inner budgets; server cursors retain authority over scope and authorization.
- `server/internal/repository/remote_c3_integration_test.go` uses local Neo4j and SeaweedFS, the authenticated Go HTTP stack, the production remote CLI **command with an injected in-memory credential store**, and a compiled MCP bridge. It exercises a >16 KiB UTF-8 source, continuation, framing, scoped denial and revocation. MCP initializes, lists tools and checks JSON-RPC IDs; source access denials include object-open checks. A separate TS test launches the actual CLI/compiled bridge against synthetic HTTP. These tests do not qualify an actual keychain, a production deployment, or a specific external MCP client.
- Updated `README.md`, `skills/kb/references/query.md`, and `docs/integrations/remote.md`. These explicitly describe the current limitations; recheck them after the remaining fixes.

## Unresolved design and acceptance issues

1. **MCP candidate and checkpoint writes are deliberately disabled.** Tool discovery advertises them, but preflight returns `unavailable` without mutation because the remote write API does not establish a revision size bound before committing. The original attempted compact receipt could commit without returning the revision required for updates. This is not C3 complete. A safe design needs an authoritative, versioned bounded-revision contract, compatible detection before mutation (including mixed-server versions), and a truthful `unknown_outcome`/reconciliation or idempotency strategy for lost HTTP responses after commit. Do not just add a local constant or claim exactly-once writes.
2. **UTF-8 artifact-edge bytes can be lost in fetch output.** Capture splits filtered JSON at fixed 8 MiB byte offsets; a character can straddle artifacts. Fetch narrows each artifact to valid UTF-8 and excludes split fragments. The stored bytes still exist, but concatenating exposed passages cannot recover them. A proposed compatible repair is an explicitly negotiated lossless representation of authorized edge fragments with offsets and base64, bound to budgets and continuations. It requires a versioned API/client design and tests; changing chunk boundaries alone would not repair existing objects and could affect lost-ack idempotency. Production data existence is unknown; local synthetic artifacts existed, but do not assume any cloud state.
3. **Pagination limitations:** each fetched passage rereads and hashes the entire object; large artifacts can incur substantial cumulative object I/O. A future MCP page can fail its frame budget because the authoritative cursor binds the effective service budget; report incomplete results rather than asserting guaranteed traversal. Very small MCP budgets can produce a compact protocol error larger than the requested budget; successful frames are bounded. Decide and document a contract for this explicit error exception.
4. Full C3 acceptance, independent review after fixes, and platform qualification remain open. Other work after C3 (C2 harness adapters, C4 import/export, I1–I4 intelligence, O1–O4 operations) was not completed. Railway, model budgets and production rollout remain unqualified.

## Verification history (not current-tree certification)

Before the interrupted final corrections, `bun run lint`, `bun run test` (564 passed), `go vet ./...`, and `go test -race ./...` passed; full Go race output was `/tmp/kb-c3-go-race.log`, and Bun output `/tmp/kb-c3-ts-final.log` at the time. Temporary paths may not persist. A focused actual local synthetic HTTP/CLI/MCP test also passed. `golangci-lint` was not found on the checked PATH; no successful full lint run is claimed. After the partial corrections, the Go build and two targeted Bun tests fail as described above. `git diff --check` passed at handoff, but this does not validate untracked files or behavior.

## Recommended next steps

1. Preserve the worktree and finish the three interrupted corrections. Run focused tests after each; verify citation applicability, request-scope rejection, Go cursor/offset checks, and the real CLI fixture.
2. Run full Bun lint/tests, Go vet and full race tests on the final tree; rerun actual synthetic integration and generated-code drift checks. Record `golangci-lint` as unavailable unless actually installed and run. Review both tracked **and untracked** files.
3. Resolve or explicitly scope the lossless artifact-edge representation and the versioned, recoverable MCP mutation contract; keep writes disabled until proven safe. Fix pagination/large-object work limits or obtain an explicit acceptance decision for the remaining limitation.
4. Conduct independent final review and update graph/checks/progress and integration docs. Do not mark C3 complete, commit, push, or deploy based on the earlier green results.

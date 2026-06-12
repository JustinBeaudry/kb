---
title: "fix: close the open issue backlog (#9, #11-#16)"
type: fix
status: active
date: 2026-06-12
---

# fix: close the open issue backlog (#9, #11–#16)

## Summary

Close all seven open issues: the six validated residual findings from the tree-navigation review (#11–#16 — node-ID collisions, silent empty map envelopes, dropped preamble wikilinks, a stat race, qmd timeout and test gaps) and the older `--vault-path` flag gap on `summarize`/`summaries` (#9). Every fix shape was already validated by per-finding review validators; this plan turns them into landed, tested code.

## Problem Frame

The 2026-06-12 tree-navigation PR (#17, merged as `6be7869`) shipped with six P2 residuals deliberately deferred rather than auto-applied, each filed as a tracked issue with a concrete suggested fix (recorded in `docs/residual-review-findings/feat-budget-bounded-tree-navigation.md`). Issue #9 predates that PR. "Get it all done" means draining this backlog.

## Assumptions

Pipeline-mode scoping decisions, recorded for review:

- **Scope is exactly the seven open issues.** No adjacent refactors (e.g., the known access-log cast residual or the doctor tmp-litter sweep stay deferred — they are recorded residuals, not open issues).
- **Issues #15 and #16 land as one unit** — both change `qmd.ts`'s hint path and share a new test file; splitting them would force two commits to the same ~30 lines.
- **#9 is fixed with citty-consistent args**, not by extending the hand-rolled flag parsing, since `mark-extracted.ts`/`sessions.ts` define the repo pattern.

---

## Requirements

- R1. `kb map` never emits two chunks with the same `node_id`, including when ordinal suffixes collide with natural `-2` slugs (closes #11).
- R2. `kb map <query>` never emits an empty, signal-less envelope: when budget fitting drops all candidate chunks, the output carries degradation/truncation signals and suggestions (closes #12).
- R3. Wikilinks appearing before the first heading, or on pages with no headings, participate in resolution, `page.wikilinks`, `unresolved_wikilinks`, and backlinks (closes #13).
- R4. A wiki file deleted between directory walk and per-file stat does not crash `kb map`/`kb get-node`; the file is treated as removed (closes #14).
- R5. A hung or slow `qmd` binary cannot block `kb map`: hint collection has a hard deadline and falls back to no hints (closes #15).
- R6. The qmd output-parsing/normalization path is a pure, unit-tested function; out-of-wiki paths can never become node IDs (closes #16).
- R7. `kb summarize` and `kb summaries` accept `--vault-path`/`-p` like every other vault command (closes #9).
- R8. Existing behavior is preserved: full suite green, no envelope contract changes; cached `tree.json` files from the current release trigger a clean one-time rebuild (never an error).

---

## Key Technical Decisions

- **Collision-free ID assignment stays inside `makeSectionIdAssigner`.** Track every emitted full ID, not just per-base counts; on collision, increment the ordinal until the composed ID is unused. Section IDs may shift for pages that previously produced duplicates — acceptable, since duplicate IDs were unusable anyway and IDs are only promised stable across rebuilds of identical content.
- **Degradation falls back to parent pages, not silence.** In `fitToBudget`, when the page-kind subset is empty but the input was not, derive the candidates' parent pages (via `parseNodeId`) and run tiers 2–3 over those; if the final fitted set is empty while input had matches, emit `truncated: true` + suggestions. The pre-fit `no_results` check stays as-is for genuinely zero-candidate queries.
- **Preamble links ride on a new optional `PageEntry` field, with a cache schema bump.** `buildPage` records raw targets not covered by any section (line before first heading, or whole body when heading-free) in `preamble_wikilinks?: string[]`; `linkTree` resolves them exactly like section links. `CACHE_SCHEMA_VERSION` bumps `"1"` → `"2"` so existing warm caches trigger a one-time clean rebuild (the stat fast path would otherwise keep old entries — and their dropped links — indefinitely for unchanged pages); `readCachedTree` already rebuilds on version mismatch, so no new code path is needed.
- **Stat races degrade, never throw.** ENOENT from `statSync` in the cache revalidation loop and in `buildPage` means "removed since walk": skip the entry, mark the cache changed. Other stat errors still propagate.
- **qmd hints get a 2.5s deadline and a pure parser.** `qmdSearchHints` races `proc.exited` against a timer and kills on expiry, returning null (hints are best-effort). Parsing moves to an exported pure `parseQmdOutput(output, topK)` so the wiki-scope security gate is directly testable without spawning anything.
- **#9 follows the citty pattern.** Add a `vaultPath` arg (`alias: ["p"]`) resolved as `args.vaultPath ?? resolveVaultPath(process.cwd())`, mirroring `src/commands/sessions.ts`; keep each command's existing positional/flag handling otherwise.

---

## Implementation Units

Units are independent except where noted; dependency-free units may land in any order.

### U1. Collision-free section node IDs

**Goal:** Two sections can never share a node ID (closes #11).

**Requirements:** R1, R8.

**Dependencies:** None.

**Files:**
- Modify: `src/lib/map/node-id.ts` (`makeSectionIdAssigner`)
- Modify: `tests/map-builder.test.ts`

**Approach:** Keep a set of emitted full IDs alongside the per-base counter. Compose the candidate ID as today; while it is already emitted, bump the ordinal. Apply identically to the `section-<n>` empty-slug fallback.

**Patterns to follow:** existing assigner shape and its pinning tests in `tests/map-builder.test.ts` (duplicate-`Setup` test).

**Test scenarios:**
- Happy: headings "Setup", "Setup", "Setup 2" → three distinct IDs (`#setup`, `#setup-2`, `#setup-2-2` or equivalent deterministic disambiguation) — pin the exact rule.
- Happy: existing plain-duplicate test (`setup`, `setup-2`, `setup-3`) still passes unchanged.
- Edge: natural slug arrives **before** the colliding ordinal ("Setup 2" then "Setup", "Setup") → still unique.
- Edge: empty-slug positional fallback colliding with a literal `section-1` heading → unique.
- Determinism: same page builds the same IDs twice.

**Verification:** `bun test tests/map-builder.test.ts` green; a fixture vault with the collision pattern shows unique `node_id`s in `kb map` output.

### U2. No silent empty envelopes from budget degradation

**Goal:** Budget fitting always leaves the agent a signal (closes #12).

**Requirements:** R2, R8.

**Dependencies:** None.

**Files:**
- Modify: `src/commands/map.ts` (`fitToBudget`)
- Modify: `tests/map-command.test.ts`

**Approach:** When `chunks.filter(node_kind === "page")` is empty but input chunks exist, derive unique parent pages from the section candidates via `parseNodeId` + `pagesById` and use their page summaries as the tier-2/3 set. If the final fitted chunk list is empty while the input was non-empty, emit the tier-3 policy with `truncated: true` and suggestions instead of a bare empty envelope.

**Patterns to follow:** existing tier handling and `wireFor` composition in `src/commands/map.ts`.

**Test scenarios:**
- Happy: vault where a query matches ~35 section headings, `--budget 1024` → non-empty envelope (parent-page summaries) within budget, `map_tier` 2 or 3.
- Edge: budget so tight even one parent-page chunk overflows → empty chunks BUT `truncated: true` and non-empty `suggestions`.
- Regression: existing tier-1/2/3 and zero-result tests unchanged.
- Integration: output parses under the v2 validator; bytes ≤ budget in all cases.

**Verification:** `bun test tests/map-command.test.ts` green; the reproduction from issue #12 now returns a usable envelope.

### U3. Preamble and heading-free wikilinks join the graph

**Goal:** No silently dropped wikilinks (closes #13).

**Requirements:** R3, R8.

**Dependencies:** None.

**Files:**
- Modify: `src/lib/map/types.ts` (`PageEntry.preamble_wikilinks?: string[]`)
- Modify: `src/lib/map/builder.ts` (`buildPage` capture; `linkTree` resolution)
- Modify: `src/lib/map/cache.ts` (`CACHE_SCHEMA_VERSION` `"1"` → `"2"`)
- Modify: `tests/map-builder.test.ts`, `tests/map-cache.test.ts`

**Approach:** In `buildPage`, after section conversion, collect raw targets from `parseWikilinks(body)` whose line precedes the first heading (or all of them when there are no sections), store on the new optional field. In `linkTree`, resolve `page.preamble_wikilinks ?? []` through the same `resolveTarget` pipeline feeding `page.wikilinks`/`unresolved_wikilinks`/backlinks. Keep the field absent-tolerant in code (`?? []`), AND bump `CACHE_SCHEMA_VERSION` to `"2"` so pre-upgrade caches rebuild once and unchanged pages gain their preamble links immediately.

**Test scenarios:**
- Happy: heading-free page "`intro [[target]]`" → `page.wikilinks` includes the resolved target; target page's backlinks include the source.
- Happy: page with "preamble [[a]]\n# Title\nbody [[b]]" → both links resolved; no duplicates when the same target appears in both regions.
- Edge: unresolved preamble target lands in `unresolved_wikilinks`.
- Integration (cache): a hand-written `tree.json` with `schema_version: "1"` triggers a full clean rebuild (existing unknown-version test pattern); the rebuilt tree carries the preamble links.

**Verification:** `bun test tests/map-builder.test.ts tests/map-cache.test.ts` green.

### U4. ENOENT-tolerant stat during scan

**Goal:** Mid-scan file deletion degrades instead of crashing (closes #14).

**Requirements:** R4, R8.

**Dependencies:** None.

**Files:**
- Modify: `src/lib/map/cache.ts` (revalidation loop statSync)
- Modify: `src/lib/map/builder.ts` (`buildPage` statSync)
- Modify: `tests/map-cache.test.ts`

**Approach:** Wrap both `statSync` sites; on `ENOENT` treat the file as removed (cache loop: skip + `changed = true`; `buildPage` callers: skip the page — have `buildPage` return null on ENOENT and filter at both call sites, or catch in the loops). Non-ENOENT errors rethrow.

**Test scenarios:**
- Happy: `buildPage`-level — building against a path that disappears (call the exported function on a nonexistent file) does not throw and yields no page.
- Integration: warm cache where a cached page's file is deleted after `listWikiFiles` would have seen it — simulate by writing a cache referencing a file that no longer exists and asserting `loadOrBuildTree` returns a tree without it rather than throwing.
- Regression: added/removed-page detection tests unchanged.

**Verification:** `bun test tests/map-cache.test.ts` green.

### U5. qmd hint hardening: deadline + pure tested parser

**Goal:** Optional qmd can neither hang `kb map` nor smuggle out-of-wiki IDs untested (closes #15 and #16).

**Requirements:** R5, R6, R8.

**Dependencies:** None.

**Files:**
- Modify: `src/lib/qmd.ts` (extract `parseQmdOutput(output, topK)`; add deadline to `qmdSearchHints`)
- Create: `tests/qmd.test.ts`

**Approach:** `parseQmdOutput` is a pure exported function containing the current token-extraction/normalization/`isValidNodeId`/topK logic. `qmdSearchHints` spawns, then races `proc.exited` against a ~2.5s timer; on expiry, `proc.kill()` and return null. Failures remain silent (null).

**Test scenarios (parser, pure):**
- `... wiki/foo.md ...` → `["wiki/foo.md"]`.
- Bare `foo.md` and `./foo.md` → `wiki/foo.md` (prefix injected, `./` stripped).
- `raw/secret.md`, `../escape.md`, absolute paths → rejected (grammar gate).
- More than topK matches → capped at topK, deduped.
- Empty/garbage output → `[]`.

**Test scenarios (deadline, integration):**
- A fake `qmd` executable (temp dir shim prepended to PATH in the spawn env) that sleeps longer than the deadline → `qmdSearchHints` returns null within ~3s.
- A fake `qmd` that prints a valid line and exits → hints parsed normally.
- qmd absent from PATH → null immediately (existing behavior, pin it).

**Verification:** `bun test tests/qmd.test.ts` green; `kb map <query>` latency unaffected when qmd is absent.

### U6. --vault-path for summarize and summaries

**Goal:** Flag parity with the rest of the CLI (closes #9).

**Requirements:** R7, R8.

**Dependencies:** None.

**Files:**
- Modify: `src/commands/summarize.ts`, `src/commands/summaries.ts`
- Modify: `tests/summarize.test.ts` (and the summaries test file if separate; audit `tests/` for coverage of both commands)

**Approach:** Add a citty `vaultPath` string arg with `alias: ["p"]` to both commands and resolve as `args.vaultPath ?? resolveVaultPath(process.cwd())`, mirroring `src/commands/sessions.ts`. Both commands ALSO hand-parse `process.argv` (summarize's `parseFlags` assigns any unrecognized token to `session`; summaries takes the first two raw tokens as `[action, session]`), so citty consuming the flag is not enough — the raw token stream still contains it. Committed mechanism: the hand-parsers (`commandArgs`/`parseFlags`) explicitly skip `--vault-path` and `-p` plus their following value token; citty owns the flag's semantics. Do not migrate the existing positionals to citty in this unit.

**Patterns to follow:** `src/commands/sessions.ts` and `src/commands/mark-extracted.ts` arg blocks.

**Test scenarios:**
- Happy: `kb summarize <manifest> --vault-path <dir>` operates on the given vault from an unrelated cwd (use a manifest fixture; the summarizer subprocess can be stubbed via `KB_SUMMARIZE_COMMAND` as existing tests do).
- Happy: `kb summaries pin <name> -p <dir>` resolves the same way.
- Edge (parsing): the session/action positionals parse correctly with the flag BEFORE them (`kb summarize --vault-path <dir> <manifest>`) and AFTER them (`kb summarize <manifest> -p <dir>`) — the flag value must never be mistaken for the session name.
- Regression: both commands without the flag still resolve via `KB_VAULT`/cwd (existing tests unchanged).

**Verification:** `bun test` green for both command test files; `kb summarize --help` shows the flag.

---

## Scope Boundaries

- Not touching the access-log `as unknown as` cast residual or the doctor tmp-litter sweep — known residuals, not open issues.
- Not changing the envelope schema, node-ID grammar, or cache schema version (U3's optional field is additive and absent-tolerant).
- Not adding qmd vault-registration gating (`isVaultRegistered`) to `kb map` — noted in #15 as optional; deferred.

### Deferred to Follow-Up Work

- Generic constraint on `appendMinimalJsonl` to remove the access-log double cast (recorded residual).
- Doctor sweep for stale `.kb/index/*.tmp-*` litter (recorded residual).

---

## System-Wide Impact

- All changes are inside the map subsystem, qmd gateway, and two session commands; no envelope/wire contract changes (R8).
- U1 may change section IDs on pages that previously produced colliding IDs — those IDs were ambiguous before, and IDs are not promised stable across content/algorithm changes.
- U3's new optional `PageEntry` field is forward-compatible: old caches load (missing → empty), new caches written with the field are simply richer.
- Each unit should close its issue via `Closes #N` in the PR body.

---

## Sources & Research

- Issues #9, #11–#16 (each carries the validated suggested fix and severity/confidence metadata from ce-code-review run `20260612-094233-25ab026d`; #9 from run `20260611-225511-2ba3638b`).
- `docs/residual-review-findings/feat-budget-bounded-tree-navigation.md` — durable residual record.
- Load-bearing code: `src/lib/map/node-id.ts` (assigner), `src/commands/map.ts` (`fitToBudget`), `src/lib/map/builder.ts` (`buildPage`/`linkTree`/`resolveTarget`), `src/lib/map/cache.ts` (revalidation loop), `src/lib/qmd.ts` (`qmdSearchHints`), `src/commands/{summarize,summaries}.ts` (cwd-only vault resolution at line 8 of each).
- Test conventions: `tests/map-builder.test.ts`, `tests/map-command.test.ts`, `tests/map-cache.test.ts` fixtures from PR #17; `KB_SUMMARIZE_COMMAND` stubbing in `tests/summarize.test.ts`.

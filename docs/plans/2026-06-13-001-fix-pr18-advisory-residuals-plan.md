---
title: "fix: close PR #18 advisory residual findings"
type: fix
status: completed
date: 2026-06-13
---

# fix: close PR #18 advisory residual findings

## Summary

Close the three advisory residual findings from the PR #18 code review that were not auto-applied — two narrow correctness gaps in the map subsystem and one event-loop hygiene gap in the qmd hint deadline. All three are small refinements to code already on the open `fix/close-open-issue-backlog` branch (PR #18).

## Problem Frame

PR #18 shipped fixes for issues #9 and #11–#16. The multi-agent review classified three further findings as advisory (non-blocking): the budget-degradation fallback in `kb map` only covers the section-only case, `buildPage` records a phantom empty page when a file is deleted at read time rather than stat time, and the qmd deadline timer is not cleared when the completion promise rejects. These are real but low-severity; closing them tightens the code already under review rather than deferring them to rot as known residuals.

## Requirements

- R1. `kb map <query>` represents the parent pages of dropped section candidates under a tight budget even when the candidate set already contains at least one page-kind chunk (not just the section-only case).
- R2. A wiki file deleted between `statSync` and the file read in `buildPage` yields no page entry (treated as removed), not a phantom page with empty sections.
- R3. The qmd hint deadline timer never delays process exit, including when the stdout-read/exit completion promise rejects before the deadline fires.
- R4. Existing behavior is preserved: full suite green, no envelope/cache contract changes.

## Key Technical Decisions

- **Augment, don't gate.** In `fitToBudget`, replace the `pages.length === 0` gate with an unconditional pass that adds parent-page summaries for any section candidate whose page is not already represented in the page set (seed a seen-set from the existing page chunks' IDs, then append missing parents). This makes the page-level tier complete for mixed candidate sets, not just section-only ones.
- **Distinguish gone from unreadable.** When `readWikiFileNoFollow` returns null in `buildPage`, re-check existence (`existsSync`): a now-missing file returns `null` (removed, consistent with the stat-time ENOENT branch); a still-present-but-unreadable/oversized file keeps the current behavior (record the page with no sections + stderr warning). `buildPage` already returns `PageEntry | null` and both call sites filter null, so no signature change.
- **Clear the timer unconditionally.** Guard the qmd deadline timer so it is always cleared regardless of how the race settles — a `try/finally` around the race (or unref'ing the timer) so a rejected completion promise cannot leave a pending 2.5s timer holding the event loop open. `try/finally` is preferred over `.unref()` because it also stops the timer from firing a stray resolve.

## Implementation Units

All three units are independent and may land in any order.

### U1. Parent-page derivation for mixed candidate sets

**Goal:** `kb map` never drops section candidates without representing their parent pages under budget pressure, even when page-kind chunks are already present (R1).

**Files:**
- Modify: `src/commands/map.ts` (`fitToBudget`)
- Modify: `tests/map-command.test.ts`

**Approach:** Replace the `if (pages.length === 0)` gate with logic that always derives missing parent pages: build a set of page IDs already present in `pages`, iterate the section candidates, and append a `pageSummary` for each distinct parent page not already in the set (via `parseNodeId` + `pagesById`, mirroring the existing section-only branch). The tier-2/tier-3 fitting downstream is unchanged.

**Patterns to follow:** the existing section-only derivation block in `fitToBudget`; `pagesById` from `src/lib/map/traverse.ts` and `parseNodeId` from `src/lib/map/node-id.ts` (the exact imports `src/commands/map.ts` already uses).

**Test scenarios:**
- Happy path (mixed): a query returning one page-kind chunk plus many section chunks on *other* pages, under a budget that overflows tier-1 → tier-2 envelope includes parent-page summaries for the section chunks' pages, deduped against the already-present page; `map_tier` is 2.
- Edge (already-represented): section chunks whose parent page is the same as the present page-kind chunk → no duplicate page chunk emitted.
- Regression: the existing section-only fallback test and the exact tier-2 test still pass unchanged.
- Integration: output parses under the v2 envelope validator; bytes ≤ budget.

**Verification:** `bun test tests/map-command.test.ts` green; a mixed-candidate fixture under tight budget returns parent pages with no duplicates.

### U2. Distinguish deleted-at-read from unreadable in buildPage

**Goal:** A file removed between stat and read produces no page entry rather than a phantom empty page (R2).

**Files:**
- Modify: `src/lib/map/builder.ts` (`buildPage`)
- Modify: `tests/map-builder.test.ts`

**Approach:** In the `content === null` branch after `readWikiFileNoFollow`, re-check `existsSync(filePath)`. If the file is gone, return `null` (matching the stat-time ENOENT branch — caller filters null). If it still exists, keep the current behavior: record the page with empty sections and the stderr warning (oversize/unreadable). Order the existence check before composing the warning so the message stays accurate.

**Patterns to follow:** the existing stat-time ENOENT branch in `buildPage` that returns `null`; the null-filtering at both `buildPage` call sites in `builder.ts` and `cache.ts`.

**Test scenarios:**
- Happy path (deleted at read): `statSync` and `readWikiFileNoFollow` run synchronously back-to-back in `buildPage`, so a real in-process unlink between them is not achievable — use `mock.module` to stub `readWikiFileNoFollow` to return null while pointing `buildPage` at a path that does not exist on disk (so the new `existsSync` re-check returns false) → `buildPage` returns null; the page is absent from the tree.
- Edge (genuinely unreadable/oversized, still present): a present file that returns null from the reader (e.g., over `MAX_FILE_BYTES`) → page recorded with empty sections and the existing warning, NOT dropped.
- Regression: the existing oversize test and the ghost-file (ENOENT-at-stat) test still pass.

**Verification:** `bun test tests/map-builder.test.ts` green; oversize pages still appear with empty sections, read-time-deleted pages do not appear.

### U3. Guard the qmd deadline timer

**Goal:** The qmd hint deadline timer can never delay process exit, including on completion-promise rejection (R3).

**Files:**
- Modify: `src/lib/qmd.ts` (`qmdSearchHints`)
- Modify: `tests/qmd.test.ts`

**Approach:** Wrap the `Promise.race` in `try/finally` (or equivalent) so `clearTimeout(timer)` runs on every exit path — including when the chained completion promise rejects and throws past the current `clearTimeout` call into the outer `catch`. Keep the existing SIGKILL + reader-cancel + unref timeout handling intact.

**Patterns to follow:** the existing timeout/kill handling already in `qmdSearchHints`; the subprocess-isolated `runHints` harness in `tests/qmd.test.ts` that verifies natural process exit.

**Test scenarios:**
- Error path (completion rejects before deadline): a fake qmd whose stdout read or exit path triggers a rejection (e.g., killed externally / closed pipe) → `qmdSearchHints` resolves/returns without the child process leaving a pending timer; the `runHints` child exits naturally well under the deadline-plus-margin window.
- Regression: the existing hang/backpressure/non-zero/absent/SIGTERM-trap tests still pass and still verify natural exit.

**Verification:** `bun test tests/qmd.test.ts` green; the rejection-path test confirms the CLI process exits promptly.

## Scope Boundaries

- No envelope schema, node-ID grammar, or cache schema changes.
- Not touching the `--vault-path` flag work, the node-ID collision fix, or any other already-shipped PR #18 unit.

### Deferred to Follow-Up Work

- The remaining PR #18 advisory observations that are not correctness/hygiene gaps (v1/v2 cache-version thrash when alternating binaries; `// SAFETY:` comments on the `ErrnoException` casts that match a pre-existing repo idiom) stay deferred — they were judged non-actionable in review and remain so.

## System-Wide Impact

All three changes are internal to the map subsystem and the qmd gateway; no wire, cache, or CLI contract changes. Work lands on the existing `fix/close-open-issue-backlog` branch (PR #18), tightening code already under review rather than opening new surface.

## Sources & Research

- PR #18 code-review run artifact `20260612-165426-bbb41fd4` (advisory findings), plus the Codex PR thread on `src/lib/qmd.ts` already resolved separately.
- Load-bearing code: `src/commands/map.ts` (`fitToBudget`, the `pages.length === 0` gate), `src/lib/map/builder.ts` (`buildPage` null-content branch and stat-time ENOENT branch), `src/lib/qmd.ts` (`qmdSearchHints` deadline race), `src/lib/map/traverse.ts` (`parseNodeId`, `pagesById`).
- Test conventions: `tests/map-command.test.ts`, `tests/map-builder.test.ts`, `tests/qmd.test.ts` (subprocess-isolated `runHints`).

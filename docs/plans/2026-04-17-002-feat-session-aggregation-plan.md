---
title: Cross-session aggregation & continuous-extraction inbox
type: feat
status: stale
stale_date: 2026-04-28
stale_reason: Abandoned. No evidence of implementation — no aggregate command, pattern detector, or continuous-extraction inbox exists in the codebase. Superseded by other priorities.
date: 2026-04-17
origin: docs/ideation/2026-04-17-open-ideation.md
---

# Cross-session aggregation & continuous-extraction inbox

## Overview

Cairn currently writes one summary file per session but never aggregates across them. The 2KB inject budget fits 2-3 newest summaries; older patterns fall out of context even when they describe recurring work. This plan adds a deterministic cross-session pattern detector that produces one synthesized page (`session-patterns.md`) at the vault root, and a continuous-extraction inbox that queues unprocessed extraction candidates so the user is nudged instead of having to remember `/cairn:extract`.

Opt-in, deterministic-first, Haiku-enhanced later.

## Problem Frame

1. **Flat session history** — every session is an island. Recurring themes, lingering open threads, and re-litigated decisions aren't visible across sessions.
2. **Inject budget waste** — 2-3 newest session summaries consume most of the 2KB budget with redundant "what happened today" content. One aggregated pattern page is higher-signal per byte.
3. **Extraction drift** — Stop hook already produces `## Extraction Candidates` and sets `extracted: false`, but extraction is manual. Candidates age out silently; `autoExtractNudge` flag exists but reads no queue.

Source: `docs/ideation/2026-04-17-open-ideation.md` idea #2.

## Requirements Trace

- R1. Produce `session-patterns.md` at vault root summarizing recurring topics, file hotspots, persistent open threads, and recent decisions from the last N sessions.
- R2. Detection is deterministic (frontmatter analysis) in v1. Haiku pass is deferred.
- R3. Opt-in via `.cairn/state.json` flag. Default off.
- R4. Inject hook prefers fresh patterns page over oldest-N session summaries inside existing 2KB budget — no budget growth.
- R5. Stop hook appends extraction candidates to `.cairn/extract-inbox.jsonl` when `extracted: false` and candidates exist.
- R6. SessionStart nudge (when `autoExtractNudge: true`) surfaces inbox count in injected context.
- R7. `/cairn:extract` consumes and drains inbox entries as sessions are processed.
- R8. No change to session-summary file format or ingest/discuss-before-filing semantics.

## Scope Boundaries

- No task manager. `open_threads` tracking is scoped to threads the Stop hook already captured.
- No Haiku summarization pass in v1. Deterministic heuristics only.
- No auto-filing of patterns or candidates. All filing routes through existing `/cairn:extract` discuss-before-filing.
- No change to 2KB inject budget.
- No weekly scheduler. Regeneration triggers: explicit `cairn aggregate`, `/cairn:refine`, or staleness check on SessionStart.

### Deferred to Separate Tasks

- Haiku-pass pattern synthesis (qualitative narrative over deterministic signals): follow-up plan.
- Cross-session `decisions` frontmatter schema hardening (current `decisions: []` shape is adequate for v1).
- Multi-vault per-project aggregation (depends on plan #4 landing).

## Context & Research

### Relevant Code and Patterns

- `hooks/session-summary` — writes session file, reads frontmatter, produces `Extraction Candidates` section. Append-to-inbox logic bolts on at line 153 after session file write.
- `hooks/inject` — bash hook with `append_if_fits` budget primitive. Patterns page injects between step 2 (`index.md`) and step 3 (recent sessions).
- `src/commands/doctor.ts` — shape for a new CLI subcommand (citty, status lines, vault resolution via `resolveVaultPath`).
- `src/lib/vault.ts` — `resolveVaultPath` for CLI. No state.json mutation helper yet — add one here.
- `src/lib/constants.ts` — `VAULT_FILES`, `DEFAULT_BUDGET`, window constants go here.
- `skills/extract/SKILL.md` — `autoExtractNudge` flag already documented; inbox consumer logic extends existing workflow.
- `skills/refine/SKILL.md` — refine pass is the natural trigger for regeneration.
- `templates/CAIRN.md` — vault schema; document `session-patterns.md` as a new vault-root file alongside `context.md` / `log.md`.

### Institutional Learnings

No `docs/solutions/` corpus yet. Ideation doc §5 (findings substrate) is the most relevant prior thinking — patterns page is a narrower, shippable slice of the same "persist derived signals" direction.

### External References

None required. Problem is self-contained to vault + hook conventions.

## Key Technical Decisions

- **Vault-root `session-patterns.md`, not `wiki/_session-patterns.md`** — it is a derived, regenerable artifact (same class as `context.md`, `log.md`, `index.md`), not a wiki knowledge page. Keeping it out of `wiki/` means lint treats it correctly (not an orphan), refine doesn't try to merge it, and the file is clearly regenerable. Frontmatter marks it `generated: true` so any future tool can distinguish.
- **Deterministic v1, Haiku v2** — frontmatter (`tags`, `files_changed`, `decisions`, `open_threads`) is already structured. Counting overlaps is free, interpretable, and testable. Haiku narrative can layer on once deterministic signal is trusted.
- **Window = N sessions, not N days** — count-based window avoids cold-start (first week produces a page), handles irregular usage, and matches existing `.cairn/state.json` style. Default N=20, override via `aggregateWindow`.
- **Inject substitution, not growth** — when patterns page exists and is fresh, inject it after `index.md` and cap session tail to 1 newest instead of all-that-fit. Preserves 2KB contract.
- **Inbox is `.jsonl`, not `.json`** — append-only matches Stop hook's single-write posture. Extract skill drains on read. One record per session.
- **No scheduler, three triggers** — explicit `cairn aggregate`, `/cairn:refine`, and SessionStart staleness check (if patterns page older than N/2 sessions or N/2 days, regenerate on next `cairn aggregate` invocation). Scheduler is a separate concern.
- **Opt-in flag at state level, not env** — `.cairn/state.json.aggregatePatterns: true` keeps configuration vault-scoped (survives shell restarts, travels with vault).

## Open Questions

### Resolved During Planning

- Page location: vault root `session-patterns.md` (see Key Technical Decisions).
- Detection method v1: deterministic frontmatter overlap (see decisions).
- Trigger: explicit CLI + refine hook; no scheduler (see decisions).
- Opt-in mechanism: `.cairn/state.json` flag (see decisions).

### Deferred to Implementation

- Exact tie-break order when two tags have equal session counts (alphabetical fine; confirm during unit 2).
- Minimum-threshold for reporting a pattern (e.g., tag must appear in ≥2 sessions, file in ≥3). Tune once real data is visible.
- Whether to include sessions with `status: incomplete` in the window (lean yes, but verify against real summary shapes during unit 2).
- Inbox record shape beyond `{ session_file, candidate_count, timestamp }` — expand if extract skill needs more during unit 5.

## Implementation Units

- [ ] **Unit 1: State schema + CLI scaffold**

**Goal:** Extend `.cairn/state.json` with aggregation flags and scaffold `cairn aggregate` subcommand (no detection logic yet).

**Requirements:** R3

**Dependencies:** None

**Files:**
- Modify: `src/lib/vault.ts` (add `readState` / `writeState` helpers)
- Modify: `src/lib/constants.ts` (add `DEFAULT_AGGREGATE_WINDOW = 20`, `PATTERNS_FILE = "session-patterns.md"`)
- Create: `src/commands/aggregate.ts`
- Modify: `src/cli.ts` (register `aggregate` subcommand)
- Test: `src/lib/vault.test.ts` (new)
- Test: `src/commands/aggregate.test.ts` (new)

**Approach:**
- State schema additions: `aggregatePatterns: boolean` (default `false`), `aggregateWindow: number` (default 20), `patternsLastGeneratedAt: string | null`.
- `readState(vaultPath)` returns parsed state with defaults filled; `writeState(vaultPath, partial)` merges and writes atomically.
- `cairn aggregate` args: `--enable`, `--disable`, `--window <n>`, and bare invocation (run pass). Scaffolding returns "not yet implemented" for bare invocation; flag mutation works end-to-end in this unit.

**Patterns to follow:**
- `src/commands/doctor.ts` — citty subcommand shape, `resolveVaultPath`, status/line output.
- `src/lib/vault.ts` `scaffoldVault` — atomic write pattern for state.json.

**Test scenarios:**
- Happy path: `readState` on fresh vault returns defaults (aggregatePatterns false, window 20, patternsLastGeneratedAt null).
- Happy path: `writeState` with partial preserves unrelated fields from existing state.
- Happy path: `cairn aggregate --enable` flips `aggregatePatterns` to true and exits 0.
- Happy path: `cairn aggregate --window 50` sets window to 50 and persists.
- Edge case: `cairn aggregate --window 0` rejected with error message.
- Edge case: `cairn aggregate` on vault without `.cairn/state.json` exits non-zero with actionable message ("run `cairn init`").
- Error path: `writeState` with unwritable state file propagates I/O error, does not leave partial write.

**Verification:** `bun test src/lib/vault.test.ts src/commands/aggregate.test.ts` green. `cairn aggregate --enable` flips flag in a real vault and `cat .cairn/state.json` shows the change.

- [ ] **Unit 2: Deterministic pattern detector**

**Goal:** Pure function that reads the last N session files and returns a structured `PatternReport`.

**Requirements:** R1, R2

**Dependencies:** Unit 1

**Files:**
- Create: `src/lib/patterns.ts`
- Test: `src/lib/patterns.test.ts`

**Approach:**
- Input: `vaultPath`, `window` (N). Output: `PatternReport` with four fields:
  - `recurringTags: Array<{ tag: string; count: number; sessions: string[] }>` (threshold: count ≥ 2)
  - `fileHotspots: Array<{ path: string; count: number; sessions: string[] }>` (threshold: count ≥ 3)
  - `persistentOpenThreads: Array<{ thread: string; firstSeen: string; lastSeen: string; sessionCount: number }>` (threshold: appears in ≥ 2 sessions)
  - `recentDecisions: Array<{ choice: string; reason: string; session: string }>` (no overlap scan; surface all decisions from window, most recent first)
- Parse frontmatter with a minimal YAML parser — either pull from existing deps or write a narrow parser (only reads the fields we need, doesn't round-trip). Session summary frontmatter shape is stable (see `hooks/session-summary`).
- Sort session files by filename (ISO timestamp — lexically sortable), take newest N.
- Tie-breaking: higher count wins; equal counts → alphabetical.

**Execution note:** Test-first. Build a small fixture of synthetic session summaries covering happy path and each edge case; write the detector to satisfy them.

**Technical design:**

> *Directional guidance for review, not implementation specification.*

```
loadWindow(vaultPath, N) -> SessionFrontmatter[]
  read sessions/, sort by filename desc, take N, parse frontmatter

tallyOverlap(sessions, field) -> Map<key, {count, sessions[]}>
  flatten field arrays, count by key, retain session file for each hit

PatternReport {
  recurringTags:      tallyOverlap(s, "tags")       filter count>=2 sort desc
  fileHotspots:       tallyOverlap(s, "files_changed.path") filter count>=3 sort desc
  persistentOpenThreads: tally open_threads[].text  filter sessionCount>=2
  recentDecisions:    flatten decisions[] sort by session desc, cap 10
}
```

**Patterns to follow:**
- `src/commands/doctor.ts` `walkForMarkdown` — directory walking idiom.
- Keep parser narrow; do not pull a heavy YAML dep for frontmatter that is always shape-stable.

**Test scenarios:**
- Happy path: 3 sessions with tag overlap → returns tags with count 2+, sessions listed.
- Happy path: 5 sessions touching same file → file hotspot reported with count and session list.
- Happy path: same open_thread text in 3 sessions → persistentOpenThreads entry with first/last seen.
- Happy path: recentDecisions returns latest-first, capped at 10.
- Edge case: empty `sessions/` → all four arrays empty, no error.
- Edge case: fewer than N sessions available → operates on what exists.
- Edge case: session file with malformed YAML frontmatter → skipped, others processed; warning logged.
- Edge case: session with empty frontmatter arrays (`tags: []`) → contributes nothing, no error.
- Edge case: tag appearing exactly once in window → excluded (below threshold).
- Edge case: tie in tag counts → deterministic alphabetical order.
- Error path: unreadable session file → skipped with warning, detector continues.

**Verification:** `bun test src/lib/patterns.test.ts` green with ≥8 scenarios covering happy/edge/error.

- [ ] **Unit 3: Patterns page writer + `cairn aggregate` pass**

**Goal:** Render `PatternReport` to `session-patterns.md`, update `patternsLastGeneratedAt`, integrate into bare `cairn aggregate` invocation.

**Requirements:** R1, R3

**Dependencies:** Unit 1, Unit 2

**Files:**
- Modify: `src/lib/patterns.ts` (add `renderPatterns(report): string`)
- Modify: `src/commands/aggregate.ts` (wire detector + renderer + state update)
- Modify: `src/lib/constants.ts` (export patterns page header/disclaimer strings if needed)
- Test: `src/lib/patterns.render.test.ts` (new)
- Test: `src/commands/aggregate.test.ts` (extend)

**Approach:**
- Rendered page starts with frontmatter:
  ```yaml
  ---
  generated: true
  generated_at: <ISO>
  window: <N>
  source_sessions: [<ISO timestamps>]
  ---
  ```
- Body sections: `## Recurring Topics`, `## File Hotspots`, `## Persistent Open Threads`, `## Recent Decisions`. Each section omits itself when the underlying array is empty (no "No results" noise).
- Top of body: one-line disclaimer `_Auto-generated from last N sessions. Regenerate with \`cairn aggregate\`._`
- Bare `cairn aggregate` flow: gate on `aggregatePatterns === true`; if disabled, exit 0 with message "Aggregation disabled. Enable with `cairn aggregate --enable`." Otherwise: run detector → render → write `<vault>/session-patterns.md` → update `patternsLastGeneratedAt` → print summary (counts per section).

**Patterns to follow:**
- `templates/CAIRN.md` frontmatter conventions for vault-root files.
- `hooks/session-summary` frontmatter style (quoted ISO timestamps).

**Test scenarios:**
- Happy path: non-empty `PatternReport` renders all four sections with correct counts.
- Happy path: frontmatter includes `generated: true`, ISO `generated_at`, `window`, and `source_sessions` array.
- Happy path: `cairn aggregate` writes file to `<vault>/session-patterns.md` and updates state timestamp.
- Edge case: empty sections are omitted from output (no empty `## File Hotspots` heading).
- Edge case: `cairn aggregate` with `aggregatePatterns: false` exits 0 without writing file.
- Edge case: vault has zero sessions → writes page with all sections omitted and disclaimer only; state timestamp still updated.
- Error path: write failure on `session-patterns.md` surfaces error, state timestamp NOT updated (no false advertisement of freshness).

**Verification:** `bun test` green. Run against real vault: `cairn aggregate --enable && cairn aggregate`, inspect `session-patterns.md`.

- [ ] **Unit 4: Inject hook substitution**

**Goal:** When `session-patterns.md` exists and is fresh, inject it after `index.md` and reduce session tail to newest 1.

**Requirements:** R4

**Dependencies:** Unit 3

**Files:**
- Modify: `hooks/inject`

**Approach:**
- After step 2 (index.md), before step 3 (recent sessions), add step 2b:
  - If `<vault>/session-patterns.md` exists AND its mtime is newer than the oldest session in the window (proxy for "fresh"), read and `append_if_fits` under a `### Session Patterns` header.
  - If injected successfully, set a local flag `patterns_injected=true`.
- Step 3 change: if `patterns_injected=true`, break after 1 session file instead of filling remaining budget. Otherwise behavior unchanged.
- Staleness proxy: if `session-patterns.md` is older than the newest session file, still inject but prepend `_(stale — run \`cairn aggregate\` to refresh)_` to its content. Agent decides whether to trust.

**Execution note:** Characterization-first. Before changing `hooks/inject`, add a test harness (bash or node-based) that exercises the hook against fixture vaults and snapshots `additionalContext` output. Then modify.

**Patterns to follow:**
- Existing `append_if_fits` primitive and step ordering in `hooks/inject`.

**Test scenarios:**
- Happy path: vault with fresh `session-patterns.md` and 5 session files → injected context contains patterns page and exactly 1 session summary.
- Happy path: vault without `session-patterns.md` → behavior unchanged, recent sessions fill remaining budget.
- Edge case: `session-patterns.md` older than newest session file → injected with staleness marker.
- Edge case: `session-patterns.md` too large for remaining budget after context+index → not injected; falls back to current session-tail behavior.
- Edge case: vault with patterns page and zero sessions → patterns page injected, session loop no-ops.
- Integration: full injected `additionalContext` stays within `CAIRN_BUDGET` (2048 bytes default).
- Error path: unreadable `session-patterns.md` → treated as absent, hook does not fail.

**Verification:** Hook test harness green. Manual: run hook against a real vault, verify output JSON respects budget and contains patterns section.

- [ ] **Unit 5: Continuous extraction inbox**

**Goal:** Stop hook appends to `.cairn/extract-inbox.jsonl`; extract skill consumes; SessionStart nudge reports count.

**Requirements:** R5, R6, R7

**Dependencies:** Unit 1 (state schema in place)

**Files:**
- Modify: `hooks/session-summary` (append-to-inbox after writing session file)
- Modify: `hooks/inject` (read inbox count when `autoExtractNudge: true`, surface in context)
- Modify: `skills/extract/SKILL.md` (document inbox drain workflow)
- Modify: `templates/CAIRN.md` (document `.cairn/extract-inbox.jsonl` purpose)

**Approach:**
- Inbox record shape (one JSON per line):
  ```json
  {"session_file":"2026-04-17T12-00-00.md","candidates":3,"queued_at":"2026-04-17T12:00:01Z"}
  ```
- Stop hook additions after line 153:
  - Count `## Extraction Candidates` list items. If count > 0 AND `extracted: false` in the just-written summary, append record to `<vault>/.cairn/extract-inbox.jsonl` (create if missing).
- Inject hook additions: after existing steps, if `state.json.autoExtractNudge === true` AND inbox file exists AND line count > 0, `append_if_fits` a `### Extraction Inbox` section: `N unprocessed session(s) with extraction candidates. Run \`/cairn:extract\`.`
- Extract skill changes: at workflow start, read inbox; as each session is processed (whether candidates are filed or not), remove that session's inbox record (rewrite file minus the line). After workflow, inbox reflects still-unprocessed sessions.

**Execution note:** Keep inbox mutations resilient. Use atomic rewrite (write temp + rename) for drain step in skill prose.

**Patterns to follow:**
- Stop hook's single-shot append posture (line 159 `>>` to log.md).
- `skills/extract/SKILL.md` existing workflow structure.

**Test scenarios:**
- Happy path: session with 2 extraction candidates → inbox gains 1 record with `candidates: 2`.
- Happy path: `/cairn:extract` processes a session → corresponding inbox line removed; other lines untouched.
- Happy path: inject hook with `autoExtractNudge: true` and 3 inbox lines → context contains "3 unprocessed session(s)".
- Edge case: session with zero extraction candidates → no inbox append.
- Edge case: inbox file missing → inject hook skips nudge silently; Stop hook creates on first append.
- Edge case: `autoExtractNudge: false` → inject hook does not read inbox, does not surface count.
- Edge case: inbox file malformed (partial line) → inject hook counts lines defensively; extract skill rewrites cleanly on next drain.
- Integration: session → Stop hook → inbox → SessionStart nudge → `/cairn:extract` → inbox decremented. End-to-end chain.
- Error path: unwritable `.cairn/` → Stop hook logs and exits 0; does not fail session.

**Verification:** Run a Claude Code session that triggers Stop hook on a test vault, verify `.cairn/extract-inbox.jsonl` gains a record. Next session SessionStart output contains nudge. Run `/cairn:extract`, verify inbox drains.

- [ ] **Unit 6: Skill + refine integration**

**Goal:** Expose aggregation to the agent via skill/command, and trigger regeneration as part of `/cairn:refine`.

**Requirements:** R1, R3, R8

**Dependencies:** Unit 3

**Files:**
- Create: `skills/aggregate/SKILL.md`
- Create: `commands/aggregate.md`
- Modify: `skills/refine/SKILL.md` (add step: regenerate patterns page as part of pass)
- Modify: `plugin.json` (register aggregate skill/command)

**Approach:**
- Skill shape mirrors `extract/SKILL.md`: toggle args (`on` / `off`), bare invocation runs `cairn aggregate`, uses `CAIRN_VAULT` resolution.
- Command wrapper (`commands/aggregate.md`) delegates to skill — matches existing pattern in `commands/extract.md`, `commands/refine.md`.
- Refine skill addition: after step 8 ("Research gaps"), insert step 8.5 "Regenerate session patterns — if `aggregatePatterns` is enabled in state.json, invoke `cairn aggregate` and mention the result in log entry". Kept behind the flag — disabled users see no change.
- Update refine log-entry template to mention patterns regeneration when it ran.

**Patterns to follow:**
- `skills/extract/SKILL.md` toggle behavior and Finding Your Vault section.
- `commands/extract.md` → `commands/refine.md` wrapper style.

**Test scenarios:**
- Test expectation: none — this unit is prose/config only. No behavior not already covered by units 1–5.
- Manual verification: run `/cairn:aggregate` in a Claude Code session with aggregation enabled, confirm patterns page regenerates.
- Manual verification: run `/cairn:refine` with `aggregatePatterns: true`, confirm patterns page regenerates as part of pass; with flag false, refine behavior unchanged.

**Verification:** Manual smoke test of both skills against a real vault. `plugin.json` parses cleanly.

## System-Wide Impact

- **Interaction graph:** Stop hook → inbox file → inject hook (nudge) → extract skill (drain). New write path in Stop hook; new read paths in inject hook and extract skill.
- **Error propagation:** All new paths degrade gracefully. Inbox write failure does not fail session end. Patterns page read failure does not break inject. Missing state.json fields fall back to defaults. This matches `hooks/inject`'s existing "graceful degradation" contract (line 12).
- **State lifecycle risks:** Inbox drain must be atomic (temp + rename) to avoid partial-line corruption on crash mid-extract. Patterns page write must be whole-file (not append) to avoid partial regeneration.
- **API surface parity:** New `cairn aggregate` CLI + `/cairn:aggregate` command + `aggregate` skill all expose the same behavior (parallel to existing init/doctor/uninstall + extract/refine trios).
- **Integration coverage:** Unit 5's end-to-end chain (Stop → inbox → inject → extract) needs an integration scenario, not just unit mocks.
- **Unchanged invariants:**
  - Session summary file format is unchanged.
  - 2KB inject budget is unchanged.
  - Discuss-before-filing for ingest/extract is unchanged.
  - `wiki/` remains for knowledge pages only; `session-patterns.md` lives at vault root.
  - PostCompact hook behavior is unchanged.

## Risks & Dependencies

| Risk | Mitigation |
|------|------------|
| Deterministic heuristic produces noisy "patterns" from shallow tag reuse | Thresholds (≥2 for tags, ≥3 for files); tune after first real run. Disable via `aggregatePatterns: false` if unusable. |
| Inbox and session summaries drift (inbox has record for deleted session file) | Extract skill tolerates missing session files: drops the record, moves on. Not a hard error. |
| Stop hook already runs under 120s timeout; adding inbox write must stay fast | Append-only single-line write is negligible. Count extraction candidates via a cheap `grep -c`-style pass. |
| YAML frontmatter parsing in TypeScript without a heavy dep | Shape is stable and narrow (4 array fields). Write a minimal parser (~30 LOC) over pulling `js-yaml`. Revisit if another unit needs full YAML. |
| Patterns page becomes a "summary of summaries" that no one reads | Ship as opt-in. If usage signal is absent after v1, deprecate instead of investing in Haiku v2. |
| Hook bash tests are fragile | Keep bash test harness minimal — fixture vaults + snapshot of injected JSON. Acceptable coverage for glue; deep logic lives in TypeScript units. |

## Documentation / Operational Notes

- Update `README.md` with `cairn aggregate` usage and `aggregatePatterns` flag.
- Update `templates/CAIRN.md` to list `session-patterns.md` and `.cairn/extract-inbox.jsonl` in the Vault Structure table.
- `cairn doctor` should grow a line reporting `aggregatePatterns: on/off` and patterns page freshness. Nice-to-have, not blocking — fold into unit 3 if trivial, otherwise defer.
- Rollout: default-off means zero behavior change for existing users. Announce in CHANGELOG; no migration required.

## Sources & References

- **Origin document:** [docs/ideation/2026-04-17-open-ideation.md](../ideation/2026-04-17-open-ideation.md) — idea #2
- Related code: `hooks/session-summary`, `hooks/inject`, `src/commands/doctor.ts`, `skills/extract/SKILL.md`
- Related plan: `docs/plans/2026-04-17-001-fix-remove-init-slash-command-plan.md` (recent plan artifact used as filename-convention reference)
- Upstream inspiration: Karpathy's LLM Wiki pattern (vault as compounding artifact)

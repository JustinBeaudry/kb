---
title: "feat: LLM tree navigation and Entire session offload"
type: feat
status: superseded
superseded_by: docs/plans/2026-06-12-001-feat-budget-bounded-tree-navigation-plan.md
date: 2026-05-08
origin: docs/brainstorms/2026-05-08-llm-tree-navigation-and-entire-session-offload-requirements.md
---

# feat: LLM tree navigation and Entire session offload

## Overview

`kb` repositions from "Claude Code memory plugin" to "agent-neutral curated knowledge CLI." Two shifts ship in this plan:

1. **Tree-navigation retrieval becomes the default smart path.** A compact, cached structural map (PageIndex-style) of curated `wiki/**` lets a host LLM select node IDs and fetch exact evidence. Adapter mode requires no separate LLM API key — the host model navigates.
2. **Sessions move to Entire.** `kb` stops scaffolding `sessions/**`, removes the Stop hook and the capture/summarize/read-session command surface, and provides explicit (non-destructive) migration UX for legacy vaults.

The plan is phased: A (sessions offload + repositioning), B (tree navigation core), C (package boundary + adapter docs). Phases A and B are independent and can land sequentially or in parallel branches; C depends on A and B being merged.

## Problem Frame

`kb` currently mixes two unrelated responsibilities — curated markdown retrieval and Claude session capture — and its retrieval is keyword grep over `wiki/**`. The brainstorm (see origin: `docs/brainstorms/2026-05-08-llm-tree-navigation-and-entire-session-offload-requirements.md`) settles two product directions:

- Sessions are not durable knowledge; Entire owns session/history search. `kb`'s job is curated knowledge, agent-neutral.
- Substring grep loses on technical-document retrieval (cross-references, repeated vocabulary, multi-hop context). PageIndex-style structural navigation, with the host LLM as navigator, is the durable shape.

This plan operationalizes both without breaking existing user vaults and without inflating the surface beyond what a 2,600-LOC codebase can carry.

## Requirements Trace

- R1, R2 — Reposition `kb` as agent-neutral; treat host integrations as adapters. Covered in Units A5, C1, C2.
- R3 — New installs don't scaffold `sessions/**`. Unit A1.
- R4 — Existing user `sessions/**` not silently deleted. Unit A4.
- R5 — Entire owns session/history search. Unit A2 (remove kb session subsystem); Unit A5 (docs).
- R6 — Compact cached structural map. Units B1, B2, B3.
- R7 — LLM tree navigation as default smart retrieval. Units B5, B6.
- R8 — Adapter mode requires no LLM API key in `kb` core. Captured in design (commands are deterministic); Unit C2 docs.
- R9 — Cheap local pre-filter. Unit B7.
- R10 — Natural document units (pages, headings, sections, neighbors), not fixed chunks. Unit B6.
- R11 — Cross-reference following. Unit B6.
- R12 — Envelope extension. Unit B4.
- R13 — Evidence-first core. Captured as design rule across B5/B6.
- R14 — `raw/**` remains ask-gated; `sessions/**` removed from trust model. Units A1 (constants), A3 (inject), A5 (docs).
- R15 — Minimized access logs. Unit B5/B6 verification (preserve hashed-query convention).
- R16 — SessionStart/PostCompact stay LLM-free, pointer-only in lazy. Unit A3 preserves; new pointer text Unit B5.
- R17 — Cached, hash-invalidated map. Unit B3.
- R18 — Bounded prompt size; subtree filtering. Unit B5.
- R19 — Vector DB not mandatory. Captured in design; Unit B7 keeps `qmd` optional.
- R20 — Zero-LLM fast path for exact title/alias. Unit B7.
- R21 — Docs (README, templates, skills, commands, hooks) updated. Unit A5.
- R22 — `doctor` detects legacy session setup. Unit A4.
- R23 — Package-boundary-ready (no physical split). Unit C1.
- R24 — Adapter docs teach the same protocol. Unit C2.

## Scope Boundaries

- Not building session search, summarization, or extraction inside `kb`.
- Not deleting existing user session files automatically.
- Not making vector search the default retrieval architecture.
- Not requiring a physical monorepo split.
- Not adding PDF/OCR ingestion in this plan.
- Not adding a standalone CLI LLM wrapper for tree navigation. Adapter-hosted navigation is the only path that ships in this slice.
- Not cloning OpenKB's full ingestion/watch/chat lifecycle.

### Deferred to Separate Tasks

- **Physical monorepo split into `@beaudry/kb-core` + per-host adapter packages** — gated on a second real adapter (Codex or Cursor) actually shipping. Follow-up plan when that exists.
- **Standalone `kb chat` / `kb answer` synthesis command** — only if user adoption shows demand for non-adapter use; not in this plan.
- **`qmd` deeper integration as a ranked candidate source** — Unit B7 wires it as a hint at most; richer ranking is a follow-on.
- **Codex and Cursor adapter packages** — Unit C2 ships docs only; actual adapter code is per-host follow-on plans.

## Context & Research

### Relevant Code and Patterns

- `src/lib/envelope.ts` — length-prefixed JSON wire format. Schema v1 fields are stable; `policy` is an open dict, but `Curation` is a closed union. Bumping to v2 is the explicit signal that we are extending the contract. Tests in `tests/envelope.test.ts` assert v1 today and must be updated alongside.
- `src/lib/hash.ts:sha256File` — streaming sha256 already exists. Reuse for content-hash keys in the tree cache.
- `src/lib/lockfile.ts:withMigrationLock` — already exported, currently unused. Wire to `kb migrate-sessions` and to atomic tree-cache writes.
- `src/lib/lockfile.ts:withExclusiveLock` — used by `withLogLock` and per-session locks; pattern for cache file coordination.
- `src/lib/access-log.ts` — already records `{query_hash, query_len}`, never plaintext. New commands `map`, `get-node`, `migrate-sessions` extend the `AccessLogCommand` union and reuse the hashed-query pattern.
- `src/lib/frontmatter.ts` — thin YAML wrapper. Use as the single frontmatter parser for the map builder.
- `src/lib/path-safety.ts`, `src/commands/recall.ts:18-92` — TOCTOU-hardened reads (`O_NOFOLLOW`, `realpathSync` containment, `assertGenuineScopeDir`). Tree-nav fetch must use the same primitives.
- `src/lib/inject/pointer.ts` — already pointer-only / no-LLM. R16 is preserved by *not changing* its execution shape; Units A3 and B5 only edit the static header text.
- `src/lib/vault.ts:48-110` — `scaffoldVault` driven by `VAULT_DIRS` and `VAULT_FILES` constants. Single source of truth for what `kb init` creates.
- `src/lib/entire.ts` — already a narrow gateway (4 functions, parses minimal text only). Aligned with R5; preserve as-is.
- `src/cli.ts` — citty-based, lazy dynamic imports per command. New commands follow the same `defineCommand({ meta, args, run })` shape.
- Test convention: `tests/*.test.ts`, integration-style, spawning `bun src/cli.ts <cmd>` and parsing envelopes from stdout (`tests/recall.test.ts:18-90`). All new tests follow this pattern.

### Institutional Learnings

- `docs/solutions/workflow-issues/match-review-ceremony-to-codebase-scale-2026-04-25.md` — for sub-5K-LOC projects, do not orchestrate sweeping all-axes plans. Slice into independent landings. This plan obeys that by phasing A → B → C.
- `docs/plans/2026-04-21-001-feat-vault-trust-boundary-lazy-retrieval-plan.md` (shipped) — established envelope contract, lazy pointer mode, access-log shape. This plan extends, never replaces.
- `docs/plans/2026-04-19-001-refactor-session-capture-manifest-plan.md` — proven recipe for hash-keyed cache invalidation: `sha256File`, content-hash recorded inside the cached artifact, mismatch → regenerate, atomic temp-then-rename writes, exclusive lockfile. Tree cache reuses this verbatim.
- `docs/brainstorms/2026-04-19-session-capture-manifest-requirements.md` — the structural argument for offloading sessions from `kb` is already made: sessions duplicate content already durably stored elsewhere (transcripts, Entire checkpoints, git). This plan is the next step in that direction.
- `docs/brainstorms/2026-04-26-rename-cairn-to-kb-requirements.md` — established the project's "clean break, no compat layer" posture for code; sessions are the deliberate exception (R4 protects user data).

### External References

The brainstorm cites these directly; this plan adopts their conceptual frame:

- PageIndex compact tree-of-contents with stable node IDs: https://pageindex.ai/research/pageindex-intro
- Why static vector similarity fails on technical manuals: https://pageindex.ai/blog/technical-manuals
- OpenKB compiled-knowledge + tree projection pattern: https://github.com/VectifyAI/OpenKB

No further external research is needed — the codebase conventions and the brainstorm's grounding are sufficient.

## Key Technical Decisions

- **Bump envelope `schema_version` to `"2"`.** Adding optional structural fields (`node_id`, `heading_path`, `node_kind`) on chunks plus a new `Curation` value (`"heading-section"`) is a contract change. A version bump is cheaper and clearer than silently widening v1. Old envelope tests update in lockstep.
- **Node ID scheme: `<rel-path>` for whole pages, `<rel-path>#<heading-slug>` for sections.** Heading slug is GitHub-style: lowercase, dashes, alphanumerics. Stable across edits unless heading text changes; collisions disambiguate by ordinal suffix (`#installation-2`).
- **One tree, projected per query.** A single graph in cache (`pages → sections → wikilinks → backlinks`); `kb map` projects a tree (or filtered subtree) for the LLM prompt. This avoids maintaining multiple tree shapes while keeping the LLM prompt budget bounded.
- **Cache at `.kb/index/tree.json`, keyed by content hash.** Each page entry records its `content_hash`; cache rebuild compares mtime + hash, regenerates only changed entries. Atomic temp-then-rename. `withMigrationLock` (renamed conceptually, or new `withCacheLock`) coordinates concurrent `kb` invocations.
- **Adapter mode is the only navigation path in v1.** The brainstorm's R8 forbids requiring a separate LLM API key in `kb` core. The host LLM does selection. `kb` ships only deterministic `map` / `get-node` / `recall` commands. A standalone synthesizer is deferred.
- **Sessions are removed, not migrated into wiki.** `kb migrate-sessions --report` (default) and `--archive` (move to `<vault>/.archive/sessions-<timestamp>/`). No automatic content promotion. Promotion into `wiki/**` (with `entire://` provenance) is a separate, explicit user action, not an upgrade behavior.
- **Package boundaries via directory layout, not physical split.** `src/core/` (pure retrieval primitives + tree nav), `src/adapters/claude/` (inject hooks + Claude-specific glue). No `package.json` workspaces in this plan.
- **`qmd` stays optional, never required.** If installed, `kb` may use it as a candidate generator hint in B7. Absence does not disable any command. R19.
- **Phase A first.** Removal of session subsystem is mechanically simple, unblocks the new product framing in docs immediately, and lets Phase B build on a clean substrate.

## Open Questions

### Resolved During Planning

- **Schema bump or extend v1?** Bump to v2. Adding optional chunk fields and a new `curation` value crosses the validator's closed union; a bump is the right signal.
- **Node ID format?** `<rel-path>[#<heading-slug>]`. Readable, stable, debuggable. Hash-only IDs rejected as opaque.
- **One tree or many?** One graph, projected as tree per query. Simpler caching, simpler invalidation.
- **Migration UX?** All three layers from the brainstorm: doctor detects + reports, explicit `kb migrate-sessions` command, default mode is `--report` (non-destructive).
- **Physical monorepo split now?** No. R23 explicitly defers it; learnings doc warns against premature ceremony on small codebases. Directory boundaries only.
- **Phase ordering?** A → B → C. A is mostly subtractive and updates docs to the new framing; B builds new functionality; C tidies layout.
- **Pointer text touched twice?** Yes, deliberately. A3 removes "Do not read sessions/" (since `sessions/` is gone from the trust model). B5 adds `kb map` mention once that command exists.

### Deferred to Implementation

- **Heading-slug collision policy.** Treat first occurrence as canonical; suffix later occurrences `#<slug>-2`, `-3`, etc. Exact handling lives in the slugger; tests pin the rule.
- **Map filtering algorithm details for query-driven `kb map <query>`.** Order of signals (title → alias → tag → heading → wikilink neighborhood → backlink → lexical fallback) is fixed in B7; weighting and tie-breaking can be tuned during implementation against the success-criteria query.
- **Exact size budgets.** `KB_MAP_BUDGET` env var; default to be calibrated during B5 against vaults of ~200 pages. Subtree fallback algorithm pinned at implementation.
- **Cache file format details.** JSON, but exact key layout (`{ pages_by_id, edges_by_source, ... }`) finalized when B2 lands. Validate stability via a `tree-cache.test.ts`.
- **Doctor wording for legacy session detection.** Final user-facing text is implementer's call; constraint is non-alarming and points to `kb migrate-sessions`.
- **Whether `read-raw` deny-rules need revision.** `.claude/settings.json` currently denies `sessions/**`; those rules can stay as belt-and-suspenders for legacy vaults or be removed when sessions leave the model. Decide during A1.

## Output Structure

After this plan lands, the source tree looks like:

```
src/
  cli.ts
  core/
    lib/
      envelope.ts                    # extended to v2
      frontmatter.ts
      markdown.ts                    # NEW (B1) — headings, sections, wikilinks
      map/
        builder.ts                   # NEW (B2)
        cache.ts                     # NEW (B3)
        candidates.ts                # NEW (B7)
        node-id.ts                   # NEW (B2)
        types.ts                     # NEW (B1/B2)
      hash.ts
      access-log.ts                  # extended for new commands
      vault.ts                       # sessions removed from VAULT_DIRS
      constants.ts                   # sessions removed
      path-safety.ts
      lockfile.ts
      sensitive-read.ts
      entire.ts                      # unchanged
      qmd.ts                         # unchanged, optional
    commands/
      recall.ts
      get.ts
      list-topics.ts
      read-raw.ts
      init.ts
      doctor.ts                      # legacy-session detection added
      uninstall.ts
      map.ts                         # NEW (B5)
      get-node.ts                    # NEW (B6)
      migrate-sessions.ts            # NEW (A4)
  adapters/
    claude/
      inject/
        pointer.ts                   # text updated A3, B5
        eager.ts                     # session block removed
        modes.ts
        log.ts
      hooks/
        inject.ts                    # entry script
.kb/
  index/
    tree.json                        # NEW cache (B3)
hooks/
  hooks.json                         # Stop hook removed
  inject
  run-hook.cmd
  # session-summary deleted
templates/
  KB.md                              # rewritten (A5)
README.md                            # rewritten top sections (A5)
skills/
  kb/SKILL.md                        # session sections stripped (A5)
  refine/SKILL.md                    # session refs stripped (A5)
  # extract/ deleted (A2)
commands/
  ingest.md, query.md, lint.md, refine.md   # session sections stripped
  # extract.md deleted (A2)
docs/
  adapter-protocol.md                # NEW (C2)
```

This is a scope declaration; if implementation finds a better layout (e.g., `src/core/lib/map/` collapsing to fewer files), the implementer may adjust.

## High-Level Technical Design

> *This illustrates the intended approach and is directional guidance for review, not implementation specification. The implementing agent should treat it as context, not code to reproduce.*

### Tree map shape (sketch)

```
TreeCache {
  schema_version: "1"          # cache file schema, distinct from envelope
  built_at:       ISO8601
  vault_root:     string
  pages: [
    {
      id:           "wiki/foo.md"
      path:         "wiki/foo.md"
      title:        "Foo"           # frontmatter.title || H1 || filename
      type:         string?         # frontmatter.type
      tags:         [string]        # frontmatter.tags
      aliases:      [string]        # frontmatter.aliases
      content_hash: "sha256:..."    # of file body
      mtime_ms:     number
      sections: [
        {
          id:          "wiki/foo.md#installation"
          heading:     "Installation"
          level:       2
          line_range:  [10, 42]     # 1-indexed inclusive
          children:    [Section...] # nested by heading depth
          wikilinks:   [string]     # target IDs (resolved page IDs)
        }
      ]
      backlinks: [string]           # page IDs that wikilink to this page
    }
  ]
  by_alias: { [alias]: pageId }     # exact-alias fast path (R20)
  by_tag:   { [tag]:   [pageId] }
}
```

### Navigation flow (adapter mode, deterministic from `kb`'s side)

```
Host LLM (Claude/Codex/Cursor)        kb (deterministic)
  | user question + chat ctx              |
  |--- kb map [query] ------------------>|  candidate-gen if query, project tree
  |<-- envelope: tree map / subtree -----|  policy.tree_root, chunks=node summaries
  | LLM selects node IDs                  |
  |--- kb get-node <id> [--neighbors] -->|  load section, follow wikilinks if asked
  |<-- envelope: section content --------|  chunks with heading_path, line_range
  | LLM may iterate (R11)                 |
  | LLM answers with citations            |
```

### Envelope v2 (additive)

```
EnvelopeChunk = {
  source:        string
  line_range:    [number, number]
  curation:      "curated" | "raw-excerpt" | "heading-section"   # v2: new value
                                                                # session-excerpt removed
  text:          string
  node_id?:      string                # v2: optional, present for tree-nav results
  heading_path?: [string]              # v2: optional, e.g. ["Foo","Installation"]
  node_kind?:    "page" | "section"    # v2: optional
}

EnvelopePolicy = {
  trust?:        "curated" | "raw"
  source_scope?: "wiki" | "raw"        # "sessions" value removed
  no_results?:   boolean
  suggestions?:  [string]
  tree_root?:    string                # v2: present on kb map
  nav_trace?:    [string]              # v2: nodes visited to reach this evidence
  ...open
}
```

### Node ID grammar

```
NodeID  ::= PagePart ('#' SectionPart)?
PagePart    ::= relative path under wiki/, e.g. "wiki/foo.md"
SectionPart ::= heading-slug (kebab-case alnum), with ordinal suffix on collisions
              ('-' decimal)?
```

## Implementation Units

Phases A and B are independent and can be merged in either order or in parallel branches. Phase C depends on A and B both being merged.

### Phase A — Sessions Offload + Repositioning

- [ ] **Unit A1: Strip session scaffolding from `kb init` and constants**

**Goal:** New installs no longer create `sessions/`, `sessions/summaries/`, `sessions/.trash/`. Source-of-truth constant is updated; existing user vaults untouched.

**Requirements:** R3, R14.

**Dependencies:** None.

**Files:**
- Modify: `src/lib/constants.ts` (drop sessions entries from `VAULT_DIRS`)
- Modify: `src/lib/vault.ts` (verify scaffolding loop still works with reduced list)
- Modify: `tests/init.test.ts` (remove `sessions/` existence assertion; add negative assertion)
- Modify: `.claude/settings.json` (decide: keep `sessions/**` deny rules as legacy belt-and-suspenders, or remove — recommend keep for now since legacy vaults still have the dir)

**Approach:**
- `VAULT_DIRS` becomes `["wiki", "raw"] as const`. The `sessions`, `sessions/summaries`, `sessions/.trash` entries are removed.
- `scaffoldVault` change is automatic via the constant.
- `tests/init.test.ts:19` flips polarity: assert `existsSync(join(vaultDir, "sessions"))` is `false`.
- Confirm no other consumer depends on `VAULT_DIRS` containing session paths: `src/commands/capture-session.ts:6,236` is removed in A2, so the order is A1 → A2 (or both in same PR).

**Execution note:** Test-first. Rewrite `tests/init.test.ts` to assert the new contract; run, see it fail; then change `constants.ts`.

**Patterns to follow:**
- `src/lib/constants.ts` (current shape stays).
- `tests/init.test.ts` integration-test pattern.

**Test scenarios:**
- Happy path: `kb init` in empty dir creates `wiki/`, `raw/`, `KB.md`, `index.md`, `log.md`, `context.md`, `.kb/state.json`, `.kb/config.json` — and does not create any `sessions*` dir.
- Edge case: `kb init` against a directory that already has a legacy `sessions/` does not delete or modify it (preservation of user data, R4).
- Edge case: `kb init` is idempotent — running twice does not error or recreate dirs.

**Verification:**
- Fresh `kb init` in a temp dir produces no `sessions*` entries on disk.
- Running `kb init` against a vault with pre-existing `sessions/` leaves that dir's contents byte-identical.

---

- [ ] **Unit A2: Remove session capture/summarize/read commands and Stop hook**

**Goal:** Delete the entire `kb` session subsystem: capture command, Stop hook, summarize/summaries commands, read-session command, supporting libs, the extract slash command and skill.

**Requirements:** R5, R21.

**Dependencies:** None (independent of A1; convenient to land together).

**Files:**
- Delete: `src/commands/capture-session.ts`
- Delete: `src/commands/summarize.ts`
- Delete: `src/commands/summaries.ts`
- Delete: `src/commands/read-session.ts`
- Delete: `src/lib/manifest.ts`
- Delete: `src/lib/summarizer.ts`
- Delete: `src/lib/excerpt.ts` (verify it has no non-session callers)
- Delete: `src/lib/git.ts` (verify only used by manifest.ts)
- Delete: `hooks/session-summary`
- Delete: `commands/extract.md`
- Delete: `skills/extract/SKILL.md` (and the `skills/extract/` directory)
- Modify: `hooks/hooks.json` (remove the `Stop` event registration)
- Modify: `src/cli.ts` (remove `capture-session`, `summarize`, `summaries`, `read-session` subcommand registrations)
- Modify: `src/lib/access-log.ts` (remove `read-session` from `AccessLogCommand` union; keep other commands)
- Modify: `src/lib/sensitive-read.ts` (verify it's still used by `read-raw.ts`; if it had session-specific paths, prune them)
- Delete: `tests/capture-session.test.ts`, `tests/session-summary.test.ts`, `tests/read-session.test.ts`, `tests/summarize.test.ts`, `tests/manifest.test.ts` (and any other session-only tests; audit `tests/` for `session` references)
- Modify: `tests/cli.test.ts` if it asserts the deleted subcommands are registered
- Modify: `package.json` if `KB_SUMMARIZE_*` env vars are referenced anywhere user-facing

**Approach:**
- Delete in dependency order from leaf consumers up: tests → commands → cli registration → libs → hooks.
- Verify `src/lib/entire.ts` still compiles and is preserved unchanged. Its callers (`capture-session.ts`, `doctor.ts`) lose `capture-session.ts`, but `doctor.ts` keeps `isEntireOnPath`. The other entire.ts functions (`getHeadCheckpointId`, `explainCheckpoint*`) lose their callers in this PR and become dead code; **keep them** — they are the foothold for future Entire-promotion flows that the brainstorm preserves under R5.
- Audit for orphaned env vars (`KB_SUMMARIZE_COMMAND`, `KB_SUMMARIZE_CHUNK_BYTES`, `KB_TRANSCRIPT_STABLE_MS`); remove docs references in A5.
- Keep `read-raw` (R14: raw stays ask-gated).

**Execution note:** Run `bun run lint` after each delete batch to catch broken imports immediately.

**Patterns to follow:**
- Reverse the registration pattern in `src/cli.ts` (lazy dynamic imports — just remove the entries).

**Test scenarios:**
- Happy path: `bun run lint` is clean after deletions.
- Happy path: `bun test` passes with the session test files removed.
- Happy path: `kb --help` (or invoking unknown commands) no longer lists session-related commands.
- Edge case: `bun src/cli.ts capture-session` exits non-zero with "unknown command" rather than running.
- Edge case: A vault with legacy `sessions/<id>.md` files is not modified by any surviving command.

**Verification:**
- `grep -ri "capture-session\|summarize\|summaries\|read-session" src/` returns zero matches in source (test fixtures may still reference legacy artifacts and are exempt).
- `bun test` passes.
- `bun run lint` passes.

---

- [ ] **Unit A3: Remove session block from eager inject; update pointer header**

**Goal:** `eager.ts` no longer expands session content into agent context. `pointer.ts` header text drops the "Do not read sessions/" hint. SessionStart/PostCompact stay LLM-free.

**Requirements:** R14, R16, R21.

**Dependencies:** A2 (so the inject paths are not the last consumer of the session subsystem when this lands).

**Files:**
- Modify: `src/lib/inject/eager.ts` (delete the `Recent Sessions` enumeration block, lines ~46-71; keep the `wiki/**` and `raw/**` portions)
- Modify: `src/lib/inject/pointer.ts` (rewrite header string; drop `sessions/` reference)
- Modify: `tests/inject-modes.test.ts` (remove session-content-in-eager assertions; update the pointer header expectation)

**Approach:**
- New pointer header text:
  ```
  ## KB Vault
  Curated knowledge available. Use `kb list-topics` or `kb recall <query>` to retrieve.
  ```
  (Single line of guidance; no `sessions/` mention. The mention of `kb map` is added in B5 once that command exists.)
- `eager.ts` retains the wiki working-set and index-summary blocks; only the recent-sessions section is stripped. Verify total payload still fits the eager budget without over-filling now that one source is gone.
- `tests/inject-modes.test.ts` asserts: pointer mode does not contain `"sessions"`, eager mode does not contain `"### Recent Sessions"`, eager-mode total bytes are at or below the existing budget.

**Execution note:** Test-first.

**Patterns to follow:**
- `src/lib/inject/pointer.ts:36-39` (header string is a single template literal — keep that shape).
- Existing inject-modes test conventions (median-bytes assertion).

**Test scenarios:**
- Happy path (pointer): generated payload contains "Curated knowledge available" and does not contain "sessions" (case-insensitive).
- Happy path (eager): generated payload contains wiki content and does not contain "Recent Sessions" header.
- Edge case (legacy vault): eager inject against a vault with pre-existing `sessions/<id>.md` files produces output with no session content.
- Edge case (empty vault): pointer payload still emits the header and gracefully handles missing `index.md`.
- Performance: pointer payload byte length stays under `POINTER_BUDGET = 500`.
- Integration: SessionStart hook script `hooks/inject` invocation does not spawn any subprocess that calls an LLM (verifiable by ensuring no `claude`, `entire`, or model-API calls are made — a `Bun.spawn` audit in test).

**Verification:**
- `bun src/cli.ts <inject-trigger>` (or running `hooks/inject` directly) emits a JSON `additionalContext` with no session strings.
- `tests/inject-modes.test.ts` passes.

---

- [ ] **Unit A4: Add `kb migrate-sessions` command and doctor legacy-session detection**

**Goal:** Provide explicit, non-destructive migration UX for legacy vaults. `doctor` detects and reports legacy session state; `kb migrate-sessions` offers `--report` (default) and `--archive` modes.

**Requirements:** R4, R22.

**Dependencies:** A1, A2 (so the new command lives in a vault that no longer expects session structure).

**Files:**
- Create: `src/commands/migrate-sessions.ts`
- Modify: `src/cli.ts` (register `migrate-sessions`)
- Modify: `src/commands/doctor.ts` (replace `collectSessionHealth` with a simpler legacy-detection block; emit a single advisory line pointing to `kb migrate-sessions`)
- Modify: `src/lib/access-log.ts` (`AccessLogCommand` union: add `"migrate-sessions"`)
- Create: `tests/migrate-sessions.test.ts`
- Modify: `tests/doctor.test.ts` (assert advisory text appears when legacy `sessions/` exists; absent otherwise)

**Approach:**
- `kb migrate-sessions` modes:
  - `--report` (default): list discovered legacy entities (manifest count, summary count, trashed count, malformed count, legacy-no-manifest count). Plain text human output. Exit 0.
  - `--archive`: under `withMigrationLock`, atomically move `<vault>/sessions/` to `<vault>/.archive/sessions-<ISO8601>/`. Idempotent: subsequent `--archive` runs report "no legacy sessions found." Exit 0.
  - `--vault-path` flag follows the existing convention.
- No `--remove` mode in this slice; users who want hard-delete can `rm` the archived dir themselves. Keeps the command's blast radius minimal.
- `doctor`: drop the verbose `collectSessionHealth` block. Replace with a single check: if `<vault>/sessions/` exists with any contents, print a one-line advisory: `"Legacy sessions/ detected. kb no longer manages sessions; run \`kb migrate-sessions --report\` to inspect or \`--archive\` to move them aside."` Otherwise silent on sessions.
- Use `withMigrationLock` from `src/lib/lockfile.ts:122` (already exported, finally with a caller).
- Move via `fs.rename` for atomicity within same filesystem; fall back to recursive copy + delete only if rename fails with `EXDEV`.

**Execution note:** Test-first. Define the `--report` and `--archive` outputs in tests before writing the command.

**Patterns to follow:**
- Citty `defineCommand` shape from `src/commands/doctor.ts` (plain-text output) and `src/commands/init.ts`.
- `withMigrationLock` usage.
- `assertSafeFilename` and path-safety primitives for the archive target path.

**Test scenarios:**
- Happy path (`--report`): vault with `sessions/<id>.md`, `sessions/summaries/<id>.md`, `sessions/.trash/<id>.md` produces a count summary and exits 0; no files are moved.
- Happy path (`--archive`): same vault → after run, `<vault>/sessions/` no longer exists, `<vault>/.archive/sessions-<ISO>/` contains the original tree byte-identical.
- Idempotency: running `--archive` twice; second run reports "no legacy sessions" and exits 0 without creating an empty archive.
- Edge case: empty vault (no `sessions/` dir at all) — `--report` prints "no legacy sessions found"; `--archive` is a no-op exit 0.
- Edge case: vault where `sessions/` is a file, not a directory — refuse with error; do not modify.
- Edge case: `--archive` while a concurrent `kb migrate-sessions --archive` holds the migration lock — second invocation either retries to acquire or exits with `LockBusyError` after retry budget.
- Error path: `sessions/` is on a different filesystem from `.archive/` → falls back to copy+delete; verify both source removed and target created.
- Integration (doctor): vault with legacy `sessions/` → `kb doctor` output contains the advisory string. Vault without `sessions/` → advisory is absent.
- Logging: `migrate-sessions --archive` emits an access-log entry with `command: "migrate-sessions"`, no plaintext paths.

**Verification:**
- `bun test tests/migrate-sessions.test.ts` passes.
- Manual: against a real legacy vault, `kb migrate-sessions --archive` moves the dir and `kb doctor` reports nothing about sessions afterward.

---

- [ ] **Unit A5: Update docs surface — remove session language, reposition as agent-neutral**

**Goal:** README, templates/KB.md, skills/**, commands/** no longer describe `kb` as a Claude session memory plugin. Docs teach the curated-knowledge story; session capture/extract guidance is removed; legacy-vault users are pointed to `kb migrate-sessions`.

**Requirements:** R1, R2 (partial — full adapter framing in C2), R5, R21.

**Dependencies:** A1, A2, A3, A4 (so docs match shipped behavior).

**Files:**
- Rewrite: `README.md` — top sections (lines 1-80 currently), vault-structure block (lines 99-111), context-injection priority (lines 113-122), model-choice section (lines 144-148). Remove all session/extract/summarize references. Update kb description from "persistent memory plugin for Claude Code" to something like "agent-neutral curated knowledge CLI."
- Rewrite: `templates/KB.md` — vault-structure table (lines 7-17), trust-boundary table (lines 23-28), session sections (lines ~270-280), extract workflow (entire section). Keep `qmd` section but reframe as optional candidate hint, not the upgrade path. The new doc teaches `kb list-topics` / `kb recall` / `kb get` only (B5/B6 commands referenced in C2 docs once they exist).
- Modify: `skills/kb/SKILL.md` — strip session language (lines 25-29, 67, 82, 86, 124).
- Modify: `skills/refine/SKILL.md` — remove `kb summarize` references (lines 69-73).
- Modify: `commands/query.md` — remove "Query sessions by files touched" section (lines 24-29).
- Modify: `commands/ingest.md`, `commands/lint.md`, `commands/refine.md` — light pass for any session references.
- Modify: `src/cli.ts` `meta.description` — change from "A persistent memory plugin for Claude Code" to "Agent-neutral curated knowledge CLI."
- Modify: `tests/docs-contract.test.ts` — flip assertions: `kb read-session` must NOT appear in docs; new strings (e.g., "agent-neutral", or whatever is finalized) DO appear.
- Modify: `tests/skill-contract.test.ts` — remove session-related assertions.
- Modify: `package.json` `description` field if changed.
- Modify: `.claude-plugin/plugin.json` description field if needed.

**Approach:**
- One pass per surface, in order: README → templates/KB.md → skills/**/SKILL.md → commands/**.md → tests.
- Cross-pollinate: the same one-paragraph product description text is reused across README header, `cli.ts` description, package.json, and plugin.json so they stay in sync.
- After all docs are updated, run a final grep: `grep -ri "session\|extract\|summarize\|capture" README.md templates/ skills/ commands/` and audit the remaining hits — anything not legitimately about migration/Entire is a leak.
- The new `templates/KB.md` *will* reference future commands (`kb map`, `kb get-node`) only after Phase B lands. In Phase A, docs use the existing `kb list-topics` / `kb recall` / `kb get` commands and explicitly call out that smarter retrieval is in progress (or simply omit). C2 will rewrite again.

**Execution note:** Test-first for the docs-contract and skill-contract tests; update them to assert the new reality, then make the docs match.

**Patterns to follow:**
- Existing `templates/KB.md` structure (frontmatter, sections, tables).
- Existing assertion style in `tests/docs-contract.test.ts` (string-presence checks).

**Test scenarios:**
- `tests/docs-contract.test.ts`: assert `templates/KB.md` does NOT contain `kb read-session`, `kb capture-session`, `kb summarize`, "Recent Sessions", "extract from sessions". Assert it DOES contain `kb recall`, `kb list-topics`, `kb migrate-sessions`.
- `tests/skill-contract.test.ts`: assert no SKILL.md mentions `read-session` or `summarize`.
- Spot check (manual): every link in README and templates/KB.md resolves; no broken section anchors.
- Cross-surface consistency: the one-paragraph product description appears identically in README, `src/cli.ts` meta.description, and `package.json` description (compare strings via test).

**Verification:**
- `grep -ri "kb capture-session\|kb summarize\|kb summaries\|kb read-session\|sessions/summaries\|sessions/.trash" README.md templates/ skills/ commands/` returns zero matches.
- All docs contract tests pass.
- `kb --help` shows the new description.

### Phase B — LLM Tree Navigation Core

- [ ] **Unit B1: Markdown structure parser**

**Goal:** Extract headings, sections (heading → next heading-of-same-or-higher-level), and wikilinks from a markdown body using small regex-based primitives. No external markdown AST library.

**Requirements:** R6 (data inputs), R10 (section boundaries), R11 (wikilink discovery).

**Dependencies:** None.

**Files:**
- Create: `src/core/lib/markdown.ts` (or `src/lib/markdown.ts` initially, moved into `src/core/` in Phase C)
- Create: `src/core/lib/map/types.ts` (shared types for parsed sections/wikilinks)
- Create: `tests/markdown.test.ts`

**Approach:**
- Public API:
  ```
  parseHeadings(body: string): Heading[]
  parseSections(body: string): Section[]      # built from headings, with line ranges
  parseWikilinks(body: string): Wikilink[]    # [[target|display]]
  slugify(heading: string): string
  ```
- `Heading`: `{ text, level, line }` (1-indexed line).
- `Section`: `{ heading, level, line_range: [start, end], wikilinks: string[], children: Section[] }` — nested by level.
- `Wikilink`: `{ target, display?, line }`. Target is the raw inside `[[...]]` minus any `|display` suffix.
- Heading regex: `/^(#{1,6})\s+(.+?)\s*$/gm` (level from `#` count). Mirror existing `pointer.ts:6` and `list-topics.ts:28` patterns.
- Wikilink regex: `/\[\[([^\]\|]+)(?:\|([^\]]+))?\]\]/g`. Trim target. Reject targets containing `..` or absolute paths.
- Slug rule: lowercase, replace whitespace with `-`, drop characters outside `[a-z0-9-]`, collapse repeats. Keep ASCII-only for stability (Unicode normalization is out of scope).
- Section line ranges: heading line → line before next heading of same-or-shallower level → end of file.
- Pure functions, no I/O.

**Execution note:** Test-first.

**Patterns to follow:**
- `src/lib/inject/pointer.ts:6,8-16` (heading regex shape).
- `src/lib/frontmatter.ts` (small, pure, no deps).

**Test scenarios:**
- Happy path: body with H1/H2/H2/H3 returns 4 headings with correct levels and lines.
- Happy path: section nesting — H2 with two H3 children produces a tree where H2.children = [H3, H3] and the last H3's line_range ends at body end.
- Edge case: body starts at H3 (no H1/H2) — top-level sections start at H3.
- Edge case: ATX headings inside fenced code blocks — *acceptable* to pick up as headings in v1 (note as known limitation; document in unit test). Real-world impact: low.
- Edge case: empty body — returns empty arrays.
- Edge case: heading text with markdown formatting (`## **Bold**`) — slug is `bold`.
- Edge case: duplicate heading text — `slugify` returns the same slug; **disambiguation is the caller's job (B2)**, not the parser's.
- Wikilinks: `[[foo]]` → target `foo`; `[[foo|Foo Display]]` → target `foo`, display `Foo Display`.
- Wikilink edge: nested brackets `[[foo[bar]]]` — accept as `foo[bar` (don't try to be clever).
- Wikilink security: `[[../etc/passwd]]` — target captured verbatim (resolution and rejection happens later in B2/B6, not in the parser).
- Slug: collisions of header text return identical slugs; ordinal suffixing is the caller's responsibility.

**Verification:**
- `tests/markdown.test.ts` passes.
- `bun run lint` passes.

---

- [ ] **Unit B2: Map builder + node ID scheme**

**Goal:** Walk `wiki/**`, parse frontmatter + structure, produce the in-memory tree-cache value (the "graph" used by all downstream consumers).

**Requirements:** R6, R20 (foothold for fast paths via `by_alias`).

**Dependencies:** B1.

**Files:**
- Create: `src/core/lib/map/node-id.ts` (slug + ordinal + node-id grammar)
- Create: `src/core/lib/map/builder.ts`
- Modify/create: `src/core/lib/map/types.ts` (extend with `TreeCache`, `PageEntry`, `SectionEntry` shapes)
- Create: `tests/map-builder.test.ts`

**Approach:**
- `buildTree(vaultPath: string): Promise<TreeCache>` — walks `<vault>/wiki/**` (using same TOCTOU-safe primitives as `recall.ts`: `O_NOFOLLOW`, `assertGenuineScopeDir`, size cap), reads each `.md` file, splits frontmatter (`src/lib/frontmatter.ts`), parses headings/wikilinks/sections (B1), assigns node IDs.
- Node ID grammar:
  - Page ID: `<rel-path>` e.g. `wiki/foo.md`.
  - Section ID: `<rel-path>#<slug>[-<ordinal>]`. Ordinals only used on collisions within a page.
- Wikilink resolution: `[[foo]]` resolves first to `wiki/foo.md` if exists, else to the `by_alias[foo]` lookup if exists, else recorded as unresolved. Unresolved wikilinks are still preserved for the LLM to see (they may indicate intent to create a page).
- Backlinks: second pass after wikilinks are resolved.
- Page title resolution priority: `frontmatter.title` → first H1 in body → filename without `.md`.
- Page-level fields: `tags` (frontmatter array), `aliases` (frontmatter array), `type` (frontmatter scalar).
- Build is deterministic given the same vault: page order is stable (sorted by path), section order matches body order, `by_alias` and `by_tag` keys are sorted.

**Execution note:** Test-first.

**Patterns to follow:**
- `src/commands/recall.ts:18-92` for safe vault walking primitives (symlink rejection, `O_NOFOLLOW`, containment checks).
- `src/lib/hash.ts:sha256File` for content hashing.
- `src/lib/frontmatter.ts` for frontmatter parsing.

**Test scenarios:**
- Happy path: small fixture vault with 3 wiki pages, 6 sections total, 2 wikilinks → builder returns a `TreeCache` with `pages.length === 3`, expected node IDs, resolved wikilinks, computed backlinks.
- Happy path: alias defined in frontmatter (`aliases: [Foo, Foo-Bar]`) → `by_alias["Foo"] === "wiki/foo.md"`.
- Edge case: page with no frontmatter → page entry uses filename-derived title, empty tags, empty aliases.
- Edge case: page where two H2 sections share the same slug (`## Setup` then `## Setup`) → first gets `wiki/x.md#setup`, second gets `wiki/x.md#setup-2`.
- Edge case: malformed frontmatter (invalid YAML) → page is skipped or recorded with `{ malformed: true, error: "..." }`; build does not throw. Decide policy: log to access-log? (No — to stderr only; access-log is for retrieval).
- Edge case: symlink inside `wiki/` → rejected; not included in the tree.
- Edge case: file larger than `MAX_FILE_BYTES` (256 KB) → skipped with a warning; tree records its existence but no sections.
- Edge case: empty `wiki/` directory → returns a `TreeCache` with `pages: []` and empty indexes.
- Wikilink resolution: `[[foo]]` resolves to `wiki/foo.md` if present; `[[Bar Page]]` resolves via `by_alias["Bar Page"]`; `[[nonexistent]]` recorded as unresolved with target string preserved.
- Backlinks: page A wikilinks to page B; B's `backlinks` includes A.
- Determinism: build the same vault twice; output JSON-equal.
- Performance smoke: 200-page fixture vault builds in under (TBD; calibrate during implementation, target < 2s).

**Verification:**
- `tests/map-builder.test.ts` passes.
- Output `TreeCache` validates against type definitions.

---

- [ ] **Unit B3: Map cache with hash-based invalidation**

**Goal:** Persist the tree-cache to `<vault>/.kb/index/tree.json`; rebuild incrementally based on file content hashes; atomic writes; exclusive coordination across concurrent `kb` invocations.

**Requirements:** R17.

**Dependencies:** B2.

**Files:**
- Create: `src/core/lib/map/cache.ts`
- Modify: `src/lib/lockfile.ts` (add `withCacheLock` if `withMigrationLock` is reserved for migrate; alternatively reuse existing lock with a different lock-file path)
- Create: `tests/map-cache.test.ts`

**Approach:**
- API:
  ```
  loadOrBuildTree(vaultPath: string): Promise<TreeCache>
  invalidateTree(vaultPath: string): Promise<void>     # used by tests and possibly doctor
  ```
- `loadOrBuildTree` reads `.kb/index/tree.json` if present; for each page in the cache, compares `content_hash` against `sha256File(pagePath)`; rebuilds only changed pages (delegating to B2 helpers); also detects new pages on disk and removed pages from cache.
- Cache file schema includes its own `schema_version` field (independent from envelope schema); v1 of the cache file uses content-hash for invalidation. `mtime_ms` is captured for diagnostics but not used as a freshness signal (R17 says "hashes or mtimes" — hash-only is more reliable).
- Atomic write: write to `tree.json.tmp`, then `fs.rename` to `tree.json`. Hold `withExclusiveLock(<vault>/.kb/index/tree.lock)` while writing.
- On cache-version mismatch (someone bumped the cache schema), rebuild from scratch.
- On JSON parse error, rebuild from scratch.
- Build telemetry to access-log: `command: "map-rebuild"`, `pages_returned: <count>`, `bytes_returned: <size>` — keeps the same redaction posture (R15).

**Execution note:** Test-first.

**Patterns to follow:**
- `src/lib/lockfile.ts` (`withExclusiveLock`).
- Atomic temp-then-rename pattern as established in the session-capture refactor plan.
- `src/lib/hash.ts:sha256File`.

**Test scenarios:**
- Happy path (cold): no `tree.json` → builds from scratch, writes file, returns `TreeCache`.
- Happy path (warm, no changes): existing `tree.json` matches all on-disk hashes → returned without rebuild; file mtime unchanged.
- Happy path (one page edited): cached `content_hash` for `wiki/foo.md` doesn't match disk → only `wiki/foo.md` is reparsed; other pages reused from cache; backlinks recomputed (since wikilinks may have changed).
- Edge case: page deleted on disk → removed from cache; backlinks pointing to it cleared.
- Edge case: page added on disk → parsed and added.
- Edge case: corrupted `tree.json` (invalid JSON) → rebuild from scratch.
- Edge case: cache file with unknown `schema_version` → rebuild from scratch.
- Concurrency: two `kb` processes both call `loadOrBuildTree` against an empty cache → exactly one writes the file (the other reads the result of the first, or rebuilds and overwrites — either is acceptable as long as both return correct trees and the file is never half-written).
- Concurrency: a third process tries to write while the lock is held → retries up to budget; either acquires after the first releases or throws `LockBusyError`.
- Atomicity: kill the process mid-write → on next invocation, `tree.json.tmp` may exist but `tree.json` is either absent or fully valid (never half-written).
- Logging: rebuild emits exactly one access-log line with hashed query empty, pages_returned and bytes_returned populated.

**Verification:**
- `tests/map-cache.test.ts` passes.
- Manual smoke: edit a wiki page, run a command that triggers rebuild, observe only that page reparses.

---

- [ ] **Unit B4: Envelope v2 — extend schema with optional structural fields**

**Goal:** Bump `schema_version` to `"2"`. Add optional chunk fields (`node_id`, `heading_path`, `node_kind`). Replace `Curation` value `"session-excerpt"` with `"heading-section"`. Add policy fields (`tree_root`, `nav_trace`). Validator accepts the new shape; tests updated.

**Requirements:** R12.

**Dependencies:** None (independent code change; can land before B5/B6, which depend on it).

**Files:**
- Modify: `src/lib/envelope.ts`
- Modify: `tests/envelope.test.ts`
- Modify: every existing call site that builds envelopes (`src/commands/recall.ts`, `get.ts`, `list-topics.ts`, `read-raw.ts`) — they continue to emit `schema_version: "2"` by virtue of calling `buildEnvelope`, but verify their output still parses.

**Approach:**
- New `Curation` union: `"curated" | "raw-excerpt" | "heading-section"` (drops `"session-excerpt"` because A2 deleted the only producer).
- New optional `EnvelopeChunk` fields: `node_id?: string`, `heading_path?: string[]`, `node_kind?: "page" | "section"`. Validator allows them when present; rejects wrong types.
- New optional `EnvelopePolicy` fields: `tree_root?: string`, `nav_trace?: string[]`. Policy is already an open dict, so this is doc-only on the type side.
- `EnvelopePolicy.source_scope` union: drop `"sessions"` value.
- `buildEnvelope` emits `schema_version: "2"`.
- `parseEnvelope` rejects v1 with `EnvelopeVersionError` (no back-compat read; envelopes are stdout-only and not persisted).
- Update `tests/envelope.test.ts` to assert v2.

**Execution note:** Test-first. Update `tests/envelope.test.ts` to assert v2 contract; run, watch it fail; then update `envelope.ts`.

**Patterns to follow:**
- Existing `assertValidShape` extension style.

**Test scenarios:**
- Happy path: build + parse roundtrip with `schema_version: "2"` succeeds.
- Happy path: chunk with optional `node_id`, `heading_path`, `node_kind` parses cleanly.
- Happy path: chunk with `curation: "heading-section"` parses cleanly.
- Happy path: policy with `tree_root` and `nav_trace` parses cleanly.
- Edge case: chunk without optional fields parses (back-compat with simple chunks from `recall`/`get`).
- Edge case: chunk with `node_id` of wrong type (number) → validator rejects.
- Edge case: chunk with `curation: "session-excerpt"` → validator rejects (closed union no longer includes it).
- Error path: parse v1 envelope → throws `EnvelopeVersionError`.
- Error path: parse envelope with `schema_version: "3"` → throws `EnvelopeVersionError`.
- Integration: `bun src/cli.ts recall <query>` against a fixture → output parses with v2 validator.

**Verification:**
- `bun test tests/envelope.test.ts` passes.
- `grep -r '"session-excerpt"' src/ tests/` returns zero matches.

---

- [ ] **Unit B5: `kb map` command**

**Goal:** Ship the `kb map` command. Without args, returns a compact projection of the tree. With a query, runs candidate generation (B7) and returns a filtered subtree. Output is an envelope (v2) where each chunk is a node summary (page or section) suitable for the LLM to read and select from. Bounded by `KB_MAP_BUDGET`.

**Requirements:** R6, R7, R18.

**Dependencies:** B3, B4, B7.

**Files:**
- Create: `src/commands/map.ts`
- Modify: `src/cli.ts` (register `map`)
- Modify: `src/lib/access-log.ts` (`AccessLogCommand` union: add `"map"`)
- Modify: `src/lib/inject/pointer.ts` (header text adds: "Run `kb map` for a structural overview before searching.")
- Create: `tests/map-command.test.ts`
- Modify: `tests/inject-modes.test.ts` (assert pointer header includes the `kb map` suggestion)

**Approach:**
- API: `kb map [query] [--vault-path|-p <path>] [--budget <bytes>]`.
- Without query: project the full tree as flat node summaries, depth-first by page then by section. Each chunk: `source = page-id`, `text = "<heading-path>: <first 200 chars of section/page intro>"`, `node_id`, `heading_path`, `node_kind`. Apply `KB_MAP_BUDGET` (default 16 KB initially; calibrate against 200-page vaults).
- With query: invoke B7 candidate generation; emit only the candidate node summaries; budget applies to the filtered set (so even very large vaults stay within budget for query mode).
- If full tree exceeds budget without a query: emit a top-level summary (page list with titles only, no sections) and add `policy.suggestions: ["Try: kb map <query>", "Or: kb map --budget <bigger>"]`.
- Envelope policy: `trust: "curated"`, `source_scope: "wiki"`, `tree_root: "wiki/"`.
- Access-log: `command: "map"`, `query_hash` from query (or empty string if no query), `query_len` from query length.
- The command first calls `loadOrBuildTree` (B3) which is idempotent and cheap on warm cache.

**Execution note:** Test-first.

**Patterns to follow:**
- `src/commands/recall.ts` for envelope build + `appendAccessLog` + path safety.
- `src/commands/list-topics.ts` for simple chunk emission.

**Test scenarios:**
- Happy path (no query): small vault → returns envelope with N+M chunks (pages + sections), `policy.tree_root === "wiki/"`, every chunk has `node_id` and `heading_path`.
- Happy path (with query): query "auth" against a vault containing `wiki/auth.md` → first-priority candidates are auth-titled pages and their sections.
- Happy path (exact alias): query matches a known alias exactly → `policy.suggestions` includes the resolved page; or alternatively, that alias is the first chunk.
- Edge case (zero results): query matching nothing → `no_results: true` policy, empty chunks, `suggestions: ["Try: kb recall <query>"]` (lexical fallback hint).
- Edge case (budget exceeded, full tree mode): vault that overflows budget → degraded view (page-titles only) + suggestion to query.
- Edge case (empty wiki): empty vault → `no_results: true`.
- Logging: every invocation produces exactly one access-log line; query is hashed never plaintext (`tests/logging.test.ts`-style assertion).
- Integration with cache (B3): two consecutive `kb map` calls — second is faster and does not rewrite `tree.json` (mtime unchanged on second call, no rebuild log line).
- Integration with envelope (B4): output parses cleanly with `parseEnvelope` v2 validator.

**Verification:**
- `bun test tests/map-command.test.ts` passes.
- Manual smoke: `bun src/cli.ts map "PageIndex"` against a fixture vault containing the brainstorm's success-criteria pages returns the expected cluster (PageIndex page, technical-manuals page, vector-RAG page).

---

- [ ] **Unit B6: `kb get-node <id>` command with cross-reference following**

**Goal:** Fetch exact section content (or whole page) by node ID. Optionally include neighbor sections or referenced pages, supporting multi-hop navigation without restarting the search.

**Requirements:** R10, R11.

**Dependencies:** B3, B4.

**Files:**
- Create: `src/commands/get-node.ts`
- Modify: `src/cli.ts` (register `get-node`)
- Modify: `src/lib/access-log.ts` (`AccessLogCommand` union: add `"get-node"`)
- Create: `tests/get-node.test.ts`

**Approach:**
- API: `kb get-node <id> [--neighbors] [--follow-wikilinks <max>] [--vault-path|-p <path>]`.
- Resolve `<id>` against `loadOrBuildTree` cache. If page-only ID (`wiki/foo.md`): return whole page as one chunk (curation `"curated"`). If section ID (`wiki/foo.md#install`): return that section's `line_range` as one chunk (curation `"heading-section"`).
- `--neighbors`: also include the previous and next siblings at the same heading level. Emitted as additional chunks with their own `node_id`s.
- `--follow-wikilinks N`: also include the first sentence (or first 200 bytes) of each wikilinked page from the requested section, capped at N (default 0). Each follow chunk has its own `node_id` and `heading_path`. Cap `N` at e.g. 5.
- File reads use the same TOCTOU-safe primitives as `recall.ts` (resolved against cache's known paths; no user-controlled path traversal).
- Envelope policy: `trust: "curated"`, `source_scope: "wiki"`, `nav_trace: [<requested-id>, ...followed-ids]`.
- Access-log: `command: "get-node"`, `query_hash` from id, `query_len` from id.length.
- Error: unknown node ID → exit 1 with `error: unknown node: <id>` to stderr; no envelope written. (Or: empty envelope with `no_results: true` — pick one and document; recommend stderr error to align with bash exit-code conventions and matching `get.ts` behavior.)
- Reject IDs that don't match the grammar; reject path traversal (`..`).

**Execution note:** Test-first.

**Patterns to follow:**
- `src/commands/get.ts` for whole-page fetch.
- `src/commands/recall.ts` for safe reads.

**Test scenarios:**
- Happy path (page ID): `kb get-node wiki/foo.md` returns the full file as one chunk.
- Happy path (section ID): `kb get-node "wiki/foo.md#installation"` returns only that section, with correct `line_range`.
- Happy path (`--neighbors`): two extra chunks (prev sibling + next sibling) at the same heading level.
- Happy path (`--follow-wikilinks 2`): if section contains `[[bar]]` and `[[baz]]`, returned envelope has 1 main chunk + 2 follow chunks; `policy.nav_trace` lists all three IDs in order.
- Edge case (no neighbors): section is the only one at its level → `--neighbors` returns just the main chunk.
- Edge case (deep nesting): H4 inside H3 inside H2 — section ID resolves only to the H4's range, not its parent's.
- Edge case (whole-page ID has H1): returns whole file (entire body), regardless of internal headings.
- Edge case (`--follow-wikilinks` to unresolved wikilink): silently skipped; nav_trace doesn't include it.
- Edge case (`--follow-wikilinks` cycle): `foo` links to `bar`, `bar` links to `foo` — cap at N prevents infinite expansion; nav_trace shows the cap was hit.
- Error path (unknown node ID): exit code 1, stderr message, no envelope on stdout.
- Error path (malformed node ID, e.g. `wiki/../etc/passwd`): exit code 1, stderr message; no file read attempted.
- Error path (node ID points to file that exists in cache but not on disk — cache stale): rebuild attempt; if still missing, error.
- Logging: one access-log line per invocation; query hashed never plaintext.
- Integration with envelope (B4): output parses cleanly with v2 validator; chunks include `node_id`, `heading_path`, `node_kind`.

**Verification:**
- `bun test tests/get-node.test.ts` passes.
- Manual smoke: `kb map "PageIndex"` → pick a node ID from the result → `kb get-node <id> --neighbors --follow-wikilinks 1` returns the expected adjacent context.

---

- [ ] **Unit B7: Candidate generation pre-filter**

**Goal:** Cheap local pre-filter that reduces the LLM's candidate space when `kb map <query>` is given a query. Order: exact title match → exact alias match → tag match → heading match → wikilink neighborhood → backlink set → lexical (existing recall-style substring) fallback. `qmd` results merged in if available, never required.

**Requirements:** R9, R19, R20.

**Dependencies:** B2 (uses TreeCache); B3 (loads it).

**Files:**
- Create: `src/core/lib/map/candidates.ts`
- Modify: `src/commands/map.ts` (calls into candidates when query is present — circular dep with B5 means B5 imports B7's API, so B7 lands first or in same PR)
- Create: `tests/candidates.test.ts`
- Optional: extend `src/lib/qmd.ts` with a thin "if installed, return top-K candidate page IDs" helper

**Approach:**
- API:
  ```
  selectCandidates(tree: TreeCache, query: string, limit: number): CandidateSet
  ```
- `CandidateSet`: `{ exact: NodeId[], tagged: NodeId[], heading: NodeId[], neighborhood: NodeId[], lexical: NodeId[], qmd?: NodeId[] }` — preserved as separate buckets so `map` can decide ordering. Each bucket internally deduped.
- Exact title/alias match (R20 fast path): if query exactly equals a page title (case-insensitive) or appears in `by_alias`, that page is the top result. `kb map <exact-alias>` should be a near-instant zero-ranked result.
- Tag match: query parses as a single token → check `by_tag`.
- Heading match: substring (case-insensitive) in any section heading.
- Wikilink neighborhood: from any matched page, include pages it wikilinks to (one hop).
- Backlink set: pages that wikilink TO any matched page.
- Lexical fallback: substring in body — borrowed from existing `recall.ts` walking, but read from cache (sections' `text` content if cached, else re-read file).
- `qmd` integration: if `isQmdAvailable()` returns true, also call `qmd search` and parse top-K page IDs; tag them in `qmd` bucket. Do not re-rank.
- Total candidate count capped at `limit` (default 30, expressed as a constant); buckets fill in priority order.

**Execution note:** Test-first.

**Patterns to follow:**
- `src/lib/qmd.ts` for narrow gateway boundary (don't deepen).
- `src/commands/recall.ts` substring scan (reuse via shared helper or duplicate the regex; small enough).

**Test scenarios:**
- Happy path (exact title): query "Authentication" with a page titled "Authentication" → first candidate is that page.
- Happy path (exact alias): query equals an alias → that page is first.
- Happy path (tag): query is a single tag → all pages with that tag returned.
- Happy path (heading): query matches an H2 heading — that section's node ID returned.
- Happy path (wikilink neighborhood): query matches page A, A wikilinks to B and C → B and C are in neighborhood bucket, ordered after exact/tag/heading.
- Happy path (backlinks): query matches page A, B and C wikilink to A → B and C in backlink bucket.
- Happy path (lexical fallback): query is substring not appearing in titles/aliases/tags/headings, only in body → returned in lexical bucket only.
- Edge case (query matches nothing): all buckets empty.
- Edge case (limit hit): vault with 100 matches, limit 30 → exactly 30 returned, distributed by priority.
- `qmd` available: stubbed `qmd` returns 3 IDs → those appear in `qmd` bucket; non-qmd path still runs and is independent.
- `qmd` unavailable: command works identically without it; no error logged.
- `qmd` errors out: silently fall back; no error to user; log to stderr at debug level only.
- Determinism: same query against same tree returns same `CandidateSet` (for testability).

**Verification:**
- `bun test tests/candidates.test.ts` passes.
- Integration smoke: `kb map "PageIndex"` against the brainstorm's success-criteria fixture surfaces the expected cluster.

### Phase C — Adapter Posture + Package Boundary Prep

- [ ] **Unit C1: Move-only refactor — `src/core/` and `src/adapters/claude/`**

**Goal:** Reorganize the source tree to expose package boundaries without splitting packages. No logic changes; only file moves and import-path updates.

**Requirements:** R23.

**Dependencies:** A and B merged (so we move a stable surface).

**Files:**
- Move: most of `src/lib/*.ts` (envelope, frontmatter, vault, hash, lockfile, log-writer, access-log, path-safety, sensitive-read, entire, qmd, markdown, map/*) → `src/core/lib/`
- Move: `src/lib/inject/*` → `src/adapters/claude/inject/`
- Move: `src/hooks/inject.ts` → `src/adapters/claude/hooks/inject.ts`
- Move: `src/commands/*` → `src/core/commands/`
- Update imports in `src/cli.ts`, `bin/kb.mjs` (no relative path change since `bin` imports `src/cli.ts` and that path is unchanged), and every moved file
- Update `tsconfig.json` if path aliases help (optional; not required)
- Update `tests/*` imports
- Update `hooks/run-hook.cmd` and `hooks/inject` (the bash wrappers) to call the new path: `bun src/adapters/claude/hooks/inject.ts`

**Approach:**
- Execute as one mechanical commit per platform's `git mv` to preserve blame. Single PR, large diff but all moves.
- After moves, run `bun run lint && bun test` — should pass with no source changes beyond import paths.
- Document the layering rule in a top-level `docs/architecture.md`: `core/` knows nothing about hosts; `adapters/<host>/` may import from `core/` but never the reverse; commands live in `core/commands/` because the CLI is host-neutral.
- No `package.json` workspaces. No new build step. The directory layout *is* the boundary.

**Execution note:** Land in its own PR. Don't bundle with logic changes.

**Patterns to follow:**
- Existing one-directional `commands/` → `lib/` import shape (verified in research).

**Test scenarios:**
- All existing tests pass with no behavioral change. (Test scenarios are the union of A and B unit tests, run after the move.)
- `grep -r "src/lib/inject" src/core/` returns zero matches (core never imports adapter code).
- `grep -r "../../core" src/adapters/` returns matches only from adapters into core (verifying the direction).
- `kb` smoke test: `bun src/cli.ts list-topics` works against a fixture vault.
- Hook wrapper test: `bash hooks/inject` runs and emits valid JSON.

**Verification:**
- `bun run lint && bun test` is green.
- Hook wrappers work end-to-end (manual smoke).

---

- [ ] **Unit C2: Adapter authoring guide + Claude adapter rewrites**

**Goal:** Document the retrieval protocol so a Codex or Cursor adapter author can implement the same shape. Update Claude-side slash commands and skills to use `kb map` / `kb get-node` flow as the default.

**Requirements:** R1, R2, R8, R24.

**Dependencies:** A5 (docs reset), B5 (`kb map` exists), B6 (`kb get-node` exists), C1 (boundaries are visible).

**Files:**
- Create: `docs/adapter-protocol.md` — the canonical adapter authoring guide
- Modify: `templates/KB.md` — add a "Smart retrieval (default)" section pointing to `kb map` / `kb get-node`; relegate `kb recall` to "lexical fallback"
- Modify: `skills/kb/SKILL.md` — rewrite the query workflow to: get the map, select node IDs, fetch evidence
- Modify: `commands/query.md` — same rewrite
- Modify: `README.md` — add an "Adapter mode" section and the canonical product-pitch paragraph
- Modify: `tests/docs-contract.test.ts` — assert the new strings present (e.g., `kb map`, `kb get-node`, "node ID")
- Modify: `src/lib/inject/pointer.ts` — header text mentions `kb map` (final form, supersedes A3 and B5)

**Approach:**
- `docs/adapter-protocol.md` table of contents:
  1. Mental model: adapter as host-side glue between user prompt and `kb` deterministic commands.
  2. Required behaviors (the loop): map → select → fetch → cite.
  3. Command contracts: `kb map`, `kb get-node`, `kb recall`, `kb list-topics`, `kb get` — input args, output envelope shape, error behavior.
  4. Envelope v2 reference (link to `src/core/lib/envelope.ts`).
  5. Trust posture: only `wiki/**` is curated; `raw/**` requires explicit user approval (`--approve`); `sessions/**` is not a `kb` surface (legacy vault note).
  6. Performance constraints: SessionStart/PostCompact must remain pointer-only; tree cache lives at `<vault>/.kb/index/tree.json`.
  7. Worked example (Claude): how `skills/kb/SKILL.md` instructs the host model.
  8. Worked example (Codex stub): same loop, different host conventions. Stub-quality is fine; this is documentation, not code.
- Final pointer header text:
  ```
  ## KB Vault
  Curated knowledge available. Run `kb map [query]` for a structural overview; fetch sections with `kb get-node <id>`. Lexical fallback: `kb recall <query>`.
  ```
  Verify byte-length stays under `POINTER_BUDGET = 500`.
- Skill/command rewrites instruct the host model to: (a) call `kb map [query]`, (b) read the chunks, (c) select promising `node_id`s, (d) call `kb get-node <id>` (with `--neighbors` or `--follow-wikilinks` when warranted), (e) cite using `source` and `line_range` from each chunk.

**Execution note:** Docs-contract tests update first; then docs.

**Patterns to follow:**
- Existing skill structure (`skills/kb/SKILL.md`).
- Existing command-doc structure (`commands/query.md`).

**Test scenarios:**
- `tests/docs-contract.test.ts`: `templates/KB.md` contains `kb map`, `kb get-node`, `node ID`. README contains "Adapter mode".
- Pointer header test (`tests/inject-modes.test.ts`): contains `kb map`, byte-length under budget, no `sessions/` reference.
- Manual: read `docs/adapter-protocol.md`; a Codex-fluent dev with no `kb` background can implement an adapter without reading source code.
- Skill smoke (in Claude Code): invoke `/kb:query` against a fixture vault — the skill walks the map → fetch loop and cites correctly.

**Verification:**
- All docs-contract assertions pass.
- Manual review of `docs/adapter-protocol.md` against a hypothetical Codex implementation succeeds (peer review check).

## System-Wide Impact

- **Interaction graph:** SessionStart/PostCompact hooks → `pointer.ts` (text only changes); Stop hook → removed entirely (A2). New entry points: `kb map`, `kb get-node`, `kb migrate-sessions`. The tree-cache rebuild is triggered implicitly by any `map`/`get-node` call but is no-op when warm.
- **Error propagation:** Cache build errors (malformed frontmatter, file too large) are logged to stderr and skipped per-file; they never cause the calling command to fail. Cache lock contention surfaces as `LockBusyError` to stderr after retry budget; commands exit non-zero. Unknown node IDs in `get-node` exit 1 with stderr message, no envelope.
- **State lifecycle risks:** Tree cache is regenerated atomically; partial writes impossible due to temp-then-rename + lockfile. Concurrent `kb` invocations share the cache via lock-file coordination. Migration archive (`migrate-sessions --archive`) is also lock-coordinated — no double-archive risk.
- **API surface parity:** Envelope v2 is a breaking change to consumers that parse the envelope and require a specific schema_version. The only known consumer is `tests/envelope.test.ts` (in-tree). External adapter code consuming envelopes must pin `schema_version: "2"`. Documented in C2.
- **Integration coverage:** Integration tests asserting cross-layer behavior — `tests/init.test.ts` (A1), `tests/inject-modes.test.ts` (A3, C2), `tests/migrate-sessions.test.ts` (A4), `tests/map-command.test.ts` (B5), `tests/get-node.test.ts` (B6), `tests/docs-contract.test.ts` (A5, C2) — cover the boundaries unit tests miss.
- **Unchanged invariants:**
  - `src/lib/envelope.ts` *length-prefixed wire format* (the prefix-newline-body framing) is preserved.
  - `raw/**` ask-gating via `read-raw` is unchanged.
  - SessionStart and PostCompact remain LLM-free in lazy mode (R16); the only changes there are the static header text.
  - `src/lib/entire.ts` surface unchanged; it's a foothold, not a new dependency.
  - `src/lib/access-log.ts` redaction posture (hashed query, never plaintext) is preserved across all new commands.
  - `src/lib/path-safety.ts` and TOCTOU primitives are unchanged; tree-nav code uses them rather than introducing new file-read patterns.

## Risks & Dependencies

| Risk | Mitigation |
|------|------------|
| Schema-v2 envelope breaks downstream consumers we don't know about (e.g., a community wrapper) | Envelopes are stdout-only and not persisted; bump is signaled clearly; document v1→v2 migration in `docs/adapter-protocol.md` and in the next release note. Project posture is "clean break, no compat" per `docs/brainstorms/2026-04-26-rename-cairn-to-kb-requirements.md`; this is consistent. |
| Tree cache becomes large for big vaults (10K pages) and slow to load | Cache is keyed by content hash so unchanged pages are O(1) skip. Initial calibration in B3 against a 200-page fixture; if 10K-page perf is unacceptable, add lazy partial-load (only by_alias + by_tag) — but not in this slice. |
| Heading-slug collisions cause unstable node IDs across vault edits | Ordinal disambiguation rule is deterministic and tested in B1. Edits that change heading text *do* invalidate the section's ID — this is acceptable; node IDs are not promised across content edits, only across rebuilds of the same content. |
| Wikilink resolution silently misses page renames | Backlinks recomputed every cache rebuild from current state. Stale links are recorded as unresolved with the original target preserved, so the LLM sees the intent. |
| `kb migrate-sessions --archive` data loss on cross-filesystem moves | Fall-back path (copy + delete) is tested for `EXDEV`; failures during copy abort before delete. The default is `--report`, requiring explicit `--archive` to mutate. |
| Eager inject content shrinkage (after A3 removes session block) leaves users feeling something is "missing" | Document in release notes; the new tree-nav flow is a richer replacement; legacy users who relied on auto-injected session summaries are pointed to Entire. |
| Pointer header revisions across A3, B5/C2 land out of order | Each unit test pins the expected text; if landed out of order, tests catch it. |
| Phase C move-only refactor breaks blame/history | Use `git mv`; verify with `git log --follow` post-merge. |
| `qmd` integration adds a test-flake surface (external binary) | `qmd` calls are stubbed by default in tests; integration test uses a real-`qmd`-or-skip pattern (already established in repo). |
| Adapter docs (C2) drift from actual command behavior over time | docs-contract tests assert key strings; future plans should update tests in lockstep. |
| Slug stability for non-ASCII headings (e.g., emoji, CJK) | Documented limitation; v1 keeps to ASCII-only. Pages with non-ASCII headings still get a slug (via stripping), but stability across vaults with the same content is best-effort. |

## Documentation / Operational Notes

- Release notes for the slice that lands Phase A: "Sessions are no longer a `kb` surface. Existing vaults are untouched; run `kb migrate-sessions --report` to inspect, `--archive` to move them aside. Use Entire for session/history search."
- Release notes for the slice that lands Phase B: "New: `kb map` and `kb get-node` for structural retrieval. The host LLM in your adapter (Claude Code, Codex, Cursor) navigates by node ID. `kb recall` remains as a lexical fallback. Envelope schema bumped to v2 — adapter authors should pin `schema_version: '2'`."
- Release notes for Phase C: "`kb` source layout reorganized into `src/core/` (host-neutral) and `src/adapters/claude/` (Claude-specific glue). Adapter authors: see `docs/adapter-protocol.md`."
- Update `kb doctor` advisories as part of A4. No external monitoring or rollout flags needed (this is a CLI tool; users opt in by upgrading).
- No CI changes needed (no CI exists today). Future: a CI pipeline running `bun run lint && bun test` would catch most regressions in this plan.

## Sources & References

- **Origin document:** [docs/brainstorms/2026-05-08-llm-tree-navigation-and-entire-session-offload-requirements.md](../brainstorms/2026-05-08-llm-tree-navigation-and-entire-session-offload-requirements.md)
- Prior plan (shipped): [docs/plans/2026-04-21-001-feat-vault-trust-boundary-lazy-retrieval-plan.md](2026-04-21-001-feat-vault-trust-boundary-lazy-retrieval-plan.md)
- Prior plan (shipped): [docs/plans/2026-04-19-001-refactor-session-capture-manifest-plan.md](2026-04-19-001-refactor-session-capture-manifest-plan.md)
- Prior brainstorm: [docs/brainstorms/2026-04-19-session-capture-manifest-requirements.md](../brainstorms/2026-04-19-session-capture-manifest-requirements.md)
- Prior brainstorm: [docs/brainstorms/2026-04-26-rename-cairn-to-kb-requirements.md](../brainstorms/2026-04-26-rename-cairn-to-kb-requirements.md)
- Institutional learning: [docs/solutions/workflow-issues/match-review-ceremony-to-codebase-scale-2026-04-25.md](../solutions/workflow-issues/match-review-ceremony-to-codebase-scale-2026-04-25.md)
- External: PageIndex research overview — https://pageindex.ai/research/pageindex-intro
- External: PageIndex on technical-manual retrieval — https://pageindex.ai/blog/technical-manuals
- External: OpenKB compiled knowledge + tree projection — https://github.com/VectifyAI/OpenKB
- Load-bearing source files: `src/lib/envelope.ts`, `src/lib/lockfile.ts`, `src/lib/hash.ts`, `src/lib/access-log.ts`, `src/lib/frontmatter.ts`, `src/lib/path-safety.ts`, `src/lib/inject/pointer.ts`, `src/lib/inject/eager.ts`, `src/lib/vault.ts`, `src/lib/constants.ts`, `src/lib/entire.ts`, `src/cli.ts`, `src/commands/recall.ts`, `src/commands/get.ts`, `src/commands/list-topics.ts`, `src/commands/doctor.ts`, `hooks/hooks.json`, `templates/KB.md`, `README.md`, `skills/kb/SKILL.md`.

---
date: 2026-04-17
topic: open-ideation
focus: none (open-ended improvement scan)
---

# Ideation: Cairn open improvement scan

## Codebase Context

Cairn is a Claude Code plugin implementing Karpathy's LLM Wiki pattern — a persistent markdown knowledge vault maintained by the agent across sessions.

**Shape.** TypeScript CLI on Bun (~900 LOC total across `src/`). Three hooks: `inject` (SessionStart, budgeted context injection), `session-summary` (Stop, structured summary writer), and a PostCompact re-injector. Three skills: `cairn` (ingest/query/lint), `extract` (sessions → wiki), `refine` (structural vault improvements). `templates/CAIRN.md` is the vault schema. Optional `qmd` MCP for hybrid search. Optional Entire integration for richer session capture.

**Recent work.** v0.3.0+ shipped typed pages, cascade ingest, categorized index, working-set `context.md`, backlinks, extract + refine skills, qmd integration, Entire integration. Just today: removed shadowing `/init` slash command and applied review findings (research-gap surfacing in lint/refine, query output forms, extract wording clarity, backlinks rationale).

**Pain points surfaced during ideation.**
- User vault `CAIRN.md` drifts silently after plugin template updates (confirmed: user's own `~/cairn/CAIRN.md` predates current template).
- Compaction handling is reactive (PostCompact only); no preflight warning.
- Sessions pile up as flat summaries; 2KB inject budget fits only 2-3 newest; no cross-session pattern aggregation.
- `.cairn` per-project override exists in `src/lib/vault.ts` but inject hook ignores cwd.
- `open_threads: []` frontmatter written by Stop hook is never consumed.
- Lint/refine compute overlapping metrics each run with no persistence.
- `/init` shadowing bug was invisible until user hit it — no hook self-test caught it.

**Leverage points.** Pure markdown ⇒ vast tooling ecosystem (git, grep, Obsidian, Dataview). Hook-based ⇒ extensible to any Claude Code behavior. Skill-first ⇒ new workflows via `SKILL.md` only. `.cairn/` control plane already exists for state.

**Past learnings.** No `docs/solutions/` corpus yet — only plan/spec documents under `docs/superpowers/`. The design spec `docs/superpowers/specs/2026-04-15-karpathy-alignment-design.md` reads as a self-retrospective on v1 vault design and directly informed these ideation frames.

## Ranked Ideas

### 1. Schema migration & template drift reconciliation
**Description:** Add a `schema_version` field to `templates/CAIRN.md` frontmatter and persist the installed version in `.cairn/state.json`. `cairn doctor` detects drift; new `cairn sync-templates` shows a 3-way diff (template-old / template-new / user-edited) and applies an approved merge. Optional: SessionStart hook nudges user when drift is detected.
**Rationale:** User's own `~/cairn/CAIRN.md` is already stale vs current repo template — this is a live gap, not hypothetical. Every future schema change depends on a working migration path. Without it, plugin behavior and vault behavior silently diverge.
**Downsides:** 3-way merge UX is fiddly in a CLI. Needs consent flow for users who customized the template. Requires backup-before-apply escape hatch.
**Confidence:** 90%
**Complexity:** Medium
**Status:** Unexplored

### 2. Cross-session aggregation & pattern detector
**Description:** Rolling analysis of the last N session summaries produces a weekly/topic `wiki/_session-patterns.md` overview page — recurring themes, unresolved threads appearing in multiple sessions, decisions being re-litigated. Pair with continuous extraction: Stop hook queues extract candidates into an inbox rather than requiring `/cairn:extract` to be run manually.
**Rationale:** The 2KB inject budget fits only 2-3 newest session summaries, so older patterns fall out of context. One synthesized overview compounds into a far better inject target than N flat summaries. Matches Karpathy's cascade principle applied at the session layer — every session makes the aggregate richer.
**Downsides:** Risks producing low-value "summaries of summaries." Needs either a cheap deterministic pattern heuristic (file path overlap, tag overlap) or a small Haiku pass. User needs opt-in control to avoid noise.
**Confidence:** 80%
**Complexity:** Medium
**Status:** Unexplored

### 3. Reliability infra: preflight compaction + hook self-test
**Description:** Two plumbing fixes bundled. (a) Preflight compaction: watch token watermark during session and write a checkpoint into `context.md` *before* compaction fires, instead of only recovering after (current PostCompact). (b) `cairn doctor --test-hooks`: dry-run each hook against synthetic transcript/input, surface silent breakage. This would have caught the `/init` shadowing bug before release.
**Rationale:** Compaction amnesia is the main adoption driver for Cairn — shifting from reactive recovery to proactive preservation is the natural next step. Hook self-test catches the exact class of bug that just shipped to production undetected.
**Downsides:** Preflight depends on a token-watermark signal Claude Code may not expose reliably; may require a heuristic. Self-test adds maintenance surface (synthetic fixtures must stay aligned with real hook inputs).
**Confidence:** 75%
**Complexity:** Medium overall (self-test low; preflight depends on available Claude Code signals)
**Status:** Unexplored

### 4. Per-project / multi-vault as first-class
**Description:** `.cairn` file resolution already exists in `src/lib/vault.ts` but the inject hook ignores cwd — fix that first. Then add `cairn init --project` to scaffold `.cairn/wiki/` committed inside the project repo. Cross-vault wikilink syntax (`[[global:topic]]`, `[[project:other-project/topic]]`). `cairn export <page>` and `cairn import <url|path>` move pages between vaults preserving `forked_from` lineage frontmatter. Team vaults fall out for free — it's just git.
**Rationale:** Current bolt-on `.cairn` override admits the global-vault model doesn't fit. Making repo-scoped vaults first-class means project knowledge follows the repo to a new laptop, into CI, into teammates' machines, and is inspectable in PR diffs. Half the plumbing already exists unused.
**Downsides:** Resolution order edge cases (which vault wins when global and project disagree?). Namespace collisions between global and project pages. Migration path for existing single-vault users.
**Confidence:** 80%
**Complexity:** High
**Status:** Unexplored

### 5. Persistent findings substrate + ingest-time semantic checks
**Description:** Four related features as one substrate:
(a) `.cairn/stats.jsonl` — append-only health metrics every lint/refine run (page count, link density, orphans, staleness, type distribution, backlink coverage).
(b) `wiki/_contradictions.md` — ledger of lint-detected contradictions with stable IDs and status (open / resolved / accepted-as-tension).
(c) `.cairn/lint-findings.jsonl` — shared work queue consumed by refine (priority targets) and ingest (auto-propose implicit-concept pages).
(d) Ingest-time semantic check: before filing a new candidate, qmd-search the vault for near-duplicates and conflicting claims; present reconciliation options inside the discuss-before-filing step.
**Rationale:** Highest-leverage compounding win across the candidate set. Current lint / refine / ingest each do overlapping analysis and throw away the results. Persisting findings turns every operation into delta work — orphans jumping this week becomes a trend, not a stat. Ingest-time contradiction check shifts detection left; the vault becomes self-healing.
**Downsides:** Four small features bundled — risk of partial landing. Semantic check requires qmd or a minimal embedding index. Ledger UX needs thought (how do users resolve entries?).
**Confidence:** 85%
**Complexity:** High overall, but splittable — stats.jsonl alone is low complexity
**Status:** Unexplored

### 6. Open-threads protocol
**Description:** `open_threads: []` is already written into every session summary's frontmatter by the Stop hook but nothing reads it. Promote to a real artifact: either a dedicated `threads.md` file at vault root, or auto-inject the N oldest-unresolved threads into `context.md`. On SessionStart the agent surfaces them: "You left X hanging 3 days ago — continue, close, or defer?"
**Rationale:** Dead metadata sitting right there. Smallest-footprint, highest-signal win in the set. Turns Cairn into a lightweight cross-session continuity tracker (not a general task manager — scoped to unfinished work the agent itself flagged).
**Downsides:** Needs a UI convention for closing threads without deleting history. Risk of noise if threads accumulate without a resolution workflow. May duplicate external task tools if scope creeps.
**Confidence:** 85%
**Complexity:** Low
**Status:** Unexplored

### 7. Auto-curated `context.md` from access patterns
**Description:** Track which vault pages are referenced, edited, or searched in the last N days via `.cairn/access.jsonl` (written by the inject hook and by skills when they read/modify pages). On SessionStart, generate the working set automatically — ranked by recency + edit frequency. User pins/excludes override the auto-rank. Feeds the stats journal in #5.
**Rationale:** Manual working-set curation is the classic "great week 1, abandoned by week 3" pattern. Access-based ranking matches where attention actually moves and requires zero maintenance. Composes cleanly with idea #5 (shared `.cairn/` infra) and #4 (project-scoped access logs).
**Downsides:** Cold-start problem — first 2 weeks produce no signal. Over-indexes recency relative to importance. Needs a pin/exclude UI to let users override for evergreen-but-infrequent references.
**Confidence:** 75%
**Complexity:** Low-Medium
**Status:** Unexplored

## Rejection Summary

| # | Idea | Reason Rejected |
|---|------|-----------------|
| 1 | Vault-as-RSS / static site publisher | Obsidian Publish and Quartz already solve this; not Cairn's job |
| 2 | qmd auto-install on first search | Small polish, not ideation-worthy on its own |
| 3 | Session-summary retry queue | Folded under idea #3 (hook self-test covers this surface) |
| 4 | CAIRN.md as living usage-adaptive document | Depends on access log (#7) + stats infra (#5) landing first; revisit later |
| 5 | Trust-mode autofile for typed candidates | Contradicts discuss-before-filing core principle; risky reversal without strong dedup data |
| 6 | Negative-knowledge extraction (failed-attempt type) | Unclear whether it's a new type or just a tag; too speculative |
| 7 | Interactive slash commands for ingest/query | Already exist in `commands/` |
| 8 | Vault is conversation — drop `sessions/` | Structural churn, breaks Entire provenance model |
| 9 | Passive signal-stream ingest (shell / browser / git tailing) | Scope explosion plus privacy risk |
| 10 | Vault daemon (long-running process) | Architectural swing without proven pain |
| 11 | Graph DB backing (SQLite / DuckDB) | Contradicts markdown-first principle; too heavy |
| 12 | Cairn as multi-client memory substrate (Codex, Gemini) | Premature generalization; no current pain signal |
| 13 | Typed page skeleton generator (`cairn new <type>`) | Agent-level concern solvable via prompt, not a CLI feature |
| 14 | Agent pages (self-refreshing via recipe DSL) | Premature DSL design |
| 15 | Git edit-history distilled into frontmatter | Marginal ranking gain for the write cost |
| 16 | Confidence frontmatter + skeptical-recall firewall | Self-estimated confidence is unreliable; source-count alone is a weaker but narrower idea |
| 17 | Role-specific `context.md` compiler (branch / pwd-aware) | Interesting, partly subsumed by #7 if the access log also captures cwd signal |
| 18 | URL ingest CLI + paste-detection hook | Useful but small; not in top-7 leverage |
| 19 | Adaptive + observable 2KB budget | Polish on the current hook; can ride alongside idea #3 |
| 20 | Search-first reframe (query as primary loop, cache → auto-file) | Bold reframe worth revisiting *after* #5 lands — the query cache in that idea requires the findings substrate to exist first |

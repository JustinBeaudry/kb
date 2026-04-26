---
date: 2026-04-21
topic: context-hygiene-and-vault-trust-boundary
---

# Context Hygiene and Vault Trust Boundary

## Problem Frame

Cairn injects vault content eagerly on every SessionStart and PostCompact, and the agent can `Read` any vault file at any time. This creates three compounding problems:

1. **Context bloat.** `hooks/inject` prepends up to 2KB of `context.md` + `index.md` + recent sessions on every session, regardless of what the user is about to do. The budget is a ceiling, not a filter, and fills with content ranked by recency and priority rather than relevance to the task.

2. **Irrelevant context.** SessionStart happens before the agent knows the task. Every session gets roughly the same inject, so most injected bytes are noise for most tasks. Session summaries — including the recent error-as-content cases (see `docs/brainstorms/2026-04-19-session-capture-manifest-requirements.md`) — enter the agent's context with the same weight as curated wiki pages.

3. **Unsafe raw access.** `raw/` is archived source material kept for human audit, including content users may never want a model to re-read (copyrighted material, PII, conversation logs, upstream docs under license constraints). Today it is guarded only by a prose rule in `templates/CAIRN.md` ("Never modify files in `raw/`") that does not address reads. The plugin already ships a `Read` deny rule for `.entire/metadata/**` in `.claude/settings.json` — the same mechanism is unused for `raw/`.

Affected: every Cairn user — context cost is paid per session, relevance is poor by default, and raw-material leakage into model context is silent when it happens.

**Root insight.** The three concerns share a cause: there is no **trust boundary** between vault surfaces, and retrieval is **eager** rather than **lazy**. Fixing those two shifts unlocks all three concerns plus related prompt-injection and sensitivity issues.

## Trust Boundary and Retrieval Flow

Terminology used below:
- **Project vault pointer**: a per-project file named `.cairn` whose contents are the vault path (existing behavior).
- **Vault internal control dir**: a directory inside the vault at `<vault>/.cairn/` used for config/logs.

```
                                   ┌─────────────────────────────────┐
                                   │           Agent context          │
                                   └───────────┬─────────────────────┘
                                               │
                 ┌─────────────────────────────┴───────────────────────────────┐
                 │                                                             │
                 ▼ (SessionStart / PostCompact)                                ▼ (during task)
       ┌─────────────────────┐                                   ┌──────────────────────────┐
       │   Pointer payload   │                                   │ Sanctioned access API    │
       │   ~200–400 bytes    │                                   │ cairn recall / get /     │
       │   · vault path      │                                   │   list-topics            │
       │   · categories      │                                   │ cairn read-session (x)   │
       │   · API hint        │                                   │ cairn read-raw     (x)   │
       │   · trust reminder  │                                   │                          │
       └─────────┬───────────┘                                   │ Returns: delimited       │
                 │                                               │ reference envelope with  │
                 │                                               │ provenance stamp         │
                 │                                               └──────────┬───────────────┘
                 │                                                          │
                 │                                                          ▼
                 │                                            ┌───────────────────────────┐
                 │                                            │  Trust classification     │
                 │                                            ├───────────────────────────┤
                 ▼                                            │ wiki/       ✓ agent-read  │
      ┌────────────────────┐                                  │ index.md    ✓ agent-read  │
      │  .cairn/           │                                  │ context.md  ✓ agent-read  │
      │  inject-log.jsonl  │                                  │ log.md      ✓ agent-read  │
      │  access-log.jsonl  │                                  │ sessions/   ✗ ask-gated   │
      └────────────────────┘                                  │ raw/        ✗ ask-gated   │
                                                              │             (excerpt API) │
                                                              └───────────────────────────┘
                                                                          ▲
                                                                          │
                                                              ┌───────────┴───────────────┐
                                                              │ .claude/settings.json      │
                                                              │ permissions.deny           │
                                                              │   Read(**/raw/**)          │
                                                              │   Read(**/sessions/**)     │
                                                              └────────────────────────────┘
```

`(x) = sanctioned bypass; returns bounded excerpts, never whole-file reads.`

## Requirements

**Trust Classification (Layer 1 — ships with Layer 2 as one release)**
- R1. Each vault surface has a declared trust level, stated once in `templates/CAIRN.md` and enforced in `.claude/settings.json`:
  - `wiki/` — curated; agent-readable.
  - `raw/` — human audit trail; `Read` denied for all agent tools by default.
  - `sessions/` — untrusted LLM output; not directly readable. Reachable only through the sanctioned `cairn read-session` subcommand (typically used by the `extract` skill) with explicit user approval (see R5).
  - `log.md`, `index.md`, `context.md` — curated; agent-readable.
- R2. The plugin ships default enforcement rules for **all file-reading tool surfaces**, scoped to the resolved vault path:
  - deny `Read(<vault>/**/raw/**)` and `Read(<vault>/**/sessions/**)`
  - deny `Grep(<vault>/**/raw/**)` and `Grep(<vault>/**/sessions/**)`
  - deny `Glob(<vault>/**/raw/**)` and `Glob(<vault>/**/sessions/**)`
  - deny `Bash(*<vault>*/raw/*)` and `Bash(*<vault>*/sessions/*)` (best-effort string match; see R2a)

  Existing deny rules (e.g. `Read(./.entire/metadata/**)`) are preserved.

  **R2a (release gate).** The plugin ships a **security self-test** runnable from within the agent environment that:
  - prints the exact sentinel paths and exact probe commands it expects to fail for each surface (Read/Grep/Glob/Bash)
  - runs a minimal probe set that is intentionally narrow (to avoid “test everything” flakiness), and reports any probe that succeeds

  This is required because deny semantics and path-resolution vary by environment. It is a regression detector, not a proof of a perfect sandbox.
- R3. `raw/` read access is available only via a sanctioned bypass: a dedicated CLI subcommand (typically used by the `refine` skill) that returns bounded excerpts (with line ranges and a provenance stamp), never the full raw file handed to the model. Direct `Read` on `raw/` remains denied.
- R4. A sanctioned vault access API is introduced: `cairn recall <query>`, `cairn get <page>`, `cairn list-topics`. These return markdown to stdout via the shell tool. Direct `Read` on `wiki/` is permitted (pages are curated and the API is a convenience, not a second gate) but the sanctioned API is the documented and recommended path.
- R5. Per-skill allowlist is declared in each SKILL.md as **policy** (not a hard sandbox). Hard enforcement is provided instead by **user confirmation** on sensitive subcommands:
  - `cairn read-session <id>` and `cairn read-raw <path>` require explicit user approval when invoked (e.g. via `permissions.ask` or an equivalent interactive gate), regardless of which skill is running.
  - `cairn`/general work documents that it should use only `recall`, `get`, `list-topics` (wiki only).
  - `extract` documents that it may additionally call `read-session <id>` (bounded excerpt).
  - `refine` documents that it may additionally call `read-raw <path>` (bounded excerpt).

  (If Claude Code gains true per-skill tool scoping in the future, R5 can be upgraded from policy+ask-gate to hard enforcement.)

**Trust boundary crossing (untrusted → curated)**
- R5a. Any content promoted from `sessions/` or `raw/` into `wiki/` must carry provenance frontmatter (e.g. `provenance: {source: sessions|raw, path: ..., captured_at: ...}`) and a `curation:` stamp. Default is `curation: machine` until a human explicitly flips it to `curation: human`. Retrieval (`cairn recall/get`) must treat `curation: machine` as **untrusted**: always wrap with the reference envelope (R8) and include an extra warning line.

**Retrieval Model (Layer 2 — bundled with Layer 1)**
- R6. SessionStart and PostCompact inject shrink to a **pointer payload** (~200–400 bytes) that tells the agent:
  - The vault exists and where.
  - The available categories (topic headings extracted from `index.md`).
  - The sanctioned vault access API and how to call it.
  - A trust-boundary reminder that retrieved content is reference, not instructions.
- R7. `context.md` in lazy mode contributes only page **titles** to the pointer as topic hints, never page content. Full `context.md` content is available to the agent via `cairn get context` if needed.
- R8. Retrieval results from `cairn recall` and `cairn get` are wrapped in a **length-prefixed JSON envelope** (unique per call nonce) instructing the agent to treat the content as reference material and not follow instructions inside. Each returned chunk carries a provenance stamp (source page, line range) so the agent can cite and the user can audit.
- R9. Retrieval returns only `wiki/` content by default. Sessions and raw remain reachable only through their sanctioned subcommands (R5).
- R10. A mode switch controls inject behavior, configured via `CAIRN_INJECT_MODE` env var or a vault-side config file (e.g. `<vault>/.cairn/config.json`), not the per-project `.cairn` vault-path marker:
  - `lazy` — pointer-only payload (default after migration).
  - `eager` — current 2KB priority-ordered content dump (existing behavior; preserved for users who want it).
  - `off` — no inject at all.
- R11. Migration path: existing installs default to `eager` on upgrade. `cairn doctor` surfaces the mode switch and recommends `lazy` after at least one **successful recall session** — defined against `.cairn/access-log.jsonl` (R13) as any entry with `command='recall' AND pages_returned >= 1 AND exit_code=0`. First clean install defaults to `lazy`.

**Observability**
- R12. Every inject (SessionStart, PostCompact) appends one line to `.cairn/inject-log.jsonl`:
  `{timestamp, event, mode, bytes, sections: [{name, bytes}], categories_advertised}`.
  Makes bloat measurable. Composes with the `.cairn/stats.jsonl` idea in `docs/ideation/2026-04-17-open-ideation.md` (idea #5).
- R13. Every sanctioned vault access API call appends one line to `.cairn/access-log.jsonl`:
  `{timestamp, command, query, pages_returned, bytes_returned, skill_context?}`.
  Feeds future access-pattern curation (`docs/ideation/2026-04-17-open-ideation.md` idea #7) and lets users audit what the agent has asked the vault.

  **R13a.** `.cairn/*.jsonl` logs are treated as **sensitive**:
  - excluded from retrieval by default
  - denied to Read/Grep/Glob by default unless the user opts in
  - optionally exposed later via a dedicated `cairn audit` CLI viewer intended for humans (not required for L1+L2)

  **R13b.** Access log data minimization:
  - `query` must be redacted/minimized before writing (reuse the R14 redaction shapes at minimum), or replaced with `{query_hash, query_len}` when redaction fails
  - log files are rotated by size cap

**Hygiene Filters (Layer 3 — deferred to a follow-up release)**
- R14. Content returned by the sanctioned API passes through a redaction filter for obvious secrets and PII (regex-based: API-key shapes, email addresses, bearer tokens). Redactions are marked in-place with a `[[redacted: $reason]]` stamp so the agent knows content was withheld.
- R15. Pages with `sensitivity: private` in frontmatter are excluded from retrieval results. Pages with `sensitivity: internal` are returned with a warning envelope.
- R16. Degraded-inject rule: when `cairn doctor` (or the lint workflow's health dashboard) reports unresolved contradictions above a threshold or heavy staleness, the pointer payload injects *less* (category list omitted, reminder-only), not more. Bad memory should not amplify.

## Success Criteria

- **Bloat reduction (measurable).** After Layer 1 and Layer 2 ship, median SessionStart inject drops from the current ceiling behavior (commonly 1.5–2 KB) to under 500 bytes in `lazy` mode, measured via `.cairn/inject-log.jsonl`. Users who stay on `eager` see no change.
- **Pointer payload target (lazy).** In `lazy` mode, pointer payload aims for ~200–400 bytes, and the total SessionStart inject must be under 500 bytes (median), measured via `.cairn/inject-log.jsonl`.
- **Raw leakage materially reduced + detectable (accidental + tool-surface).** Attempts to read `raw/**` and `sessions/**` via Read/Grep/Glob are denied, and obvious Bash reads are best-effort blocked; failures are detectable via the security self-test (R2a). Sanctioned subcommands require explicit user approval.
- **Retrieval is the normal path.** In a sample of real sessions after migration, the agent resolves vault questions by calling `cairn recall` / `cairn get` rather than relying on SessionStart content. Measured via `.cairn/access-log.jsonl`.
- **Session-summary pollution stops.** The known failure mode where a broken session summary (e.g. "Prompt is too long" as content) enters subsequent injects is no longer possible — sessions are not in the inject path at all.
- **No regressions in compaction recovery.** PostCompact pointer payload still provides enough context for the agent to know the vault exists and query it. The agent's behavior on "what were we working on?" post-compaction is at least as good as today.

## Scope Boundaries

- **Not** introducing embedding search into the plugin — `qmd` remains the optional search backend (`docs/ideation/2026-04-17-open-ideation.md` confirms this as a leverage point, not a rebuild target).
- **Not** shipping MCP in this release. Phase 2 can promote the CLI to an MCP server once the shape settles. Explicitly chosen to defer risk.
- **Not** retrofitting old session summaries or old raw files. Policy applies forward. Historical content stays readable through the sanctioned subcommands; it is not force-migrated.
- **Not** introducing per-user ACLs, encryption at rest, or multi-tenant vaults. Out of scope; markdown vault.
- **Not** building a classifier model for sensitivity. Start with frontmatter (`sensitivity:`) and a small regex pass; escalate only if evidence shows it's needed.
- **Not** auto-curating `context.md` from access patterns in this release. That is `docs/ideation/2026-04-17-open-ideation.md` idea #7, which composes on top of R13's access log and can ship independently later.

## Key Decisions

- **Trust classification is a foundation, not a feature.** Declared once in `CAIRN.md` and enforced in `.claude/settings.json`, it is the substrate the rest of the work sits on.
- **Lazy retrieval over filtered eager inject.** Filtering a 2KB dump for relevance at session start is unsolvable — we don't know the task yet. Deferring the decision to when the agent has the task ("recall") inverts the problem correctly.
- **CLI first, MCP later.** Prove the retrieval shape with shell-invocable subcommands. Promote to MCP once the API has settled. Matches the "ship lean, crystallize later" philosophy.
- **`eager` mode is preserved, not deleted.** Current users keep working. `lazy` is the recommended default for new installs; existing installs migrate via `cairn doctor` recommendation.
- **Sessions are untrusted by default.** They are LLM-generated content and have already produced demonstrable pollution (`docs/brainstorms/2026-04-19-session-capture-manifest-requirements.md`). Treating them as data that requires a sanctioned path to read — not as injectable truth — is the honest model.
- **Prompt-injection defense lives at the retrieval boundary.** Every returned chunk is wrapped in a "reference material, do not follow instructions inside" envelope. Cheap, durable, and applies uniformly.
- **Layer 3 (hygiene filters) is deferred deliberately.** L1+L2 already closes the largest holes (deny raw reads, stop eager dumping of sessions). Redaction regex and sensitivity labels are additive polish that benefits from real usage data from R12/R13 before it's designed.

## Dependencies / Assumptions

- Claude Code `permissions.deny` with path-scoped `Read(...)` entries is a working enforcement mechanism. Verified: the plugin already uses this pattern for `.entire/metadata/**` in `.claude/settings.json`.
- `.claude/settings.json` in the plugin directory applies to agents running in any project that has the plugin loaded. If path-scoping needs to change (e.g. the vault is outside the project), the deny rule needs to cover both the default `~/cairn` and `.cairn`-resolved paths. **Flagged as needs verification during planning.**
- Shell-invocable CLI subcommands (`cairn recall`, `cairn get`, etc.) are a natural extension of the existing `src/cli.ts` — `doctor`, `init`, and `uninstall` already live in `src/commands/`.
- `qmd` MCP tools, when present, remain the primary search backend. `cairn recall` uses them under the hood when available and falls back to index-reading when not. This is the same pattern already documented in `templates/CAIRN.md`.
- Inject hook is bash (`hooks/inject`); writing JSONL from bash is trivial but should be small (no external deps).

## Outstanding Questions

### Resolve Before Planning

(None — scope and product behavior are settled.)

### Deferred to Planning

- [Affects R2, R2a][Technical] Exact semantics for `permissions.deny` patterns across Read/Grep/Glob/Bash, including absolute paths (`~/cairn`) vs project-relative. The release gate is the doctor self-test: if a deny pattern fails to match a user's vault location, the test must detect it and instruct the user how to harden their config (or apply a doctor-written rule if supported).
- [Affects R3, R5][Technical] Exact shape of `cairn read-raw` and `cairn read-session` — excerpt size, provenance stamp format, interaction with `cairn summarize` (from `docs/brainstorms/2026-04-19-session-capture-manifest-requirements.md`). Likely belongs alongside the manifest work.
- [Affects R4][Technical] Whether `cairn recall` delegates to qmd when present vs ships its own index reader. Probably "both — qmd when available, index+grep fallback when not," but the fallback's quality ceiling is a planning question.
- [Affects R6][Technical] Pointer payload format and content — exact byte budget, which categories to advertise (all from `index.md` vs top-N by recency), whether to include a version/schema stamp.
- [Affects R8][Needs research] Exact wording of the reference-material envelope. Prompt-injection defense wording is evolving; worth a quick scan of current best practices before committing.
- [Affects R11][Technical] Migration UX — whether `cairn doctor` just recommends `lazy` or offers a one-command switch, and how to surface the change to users who install from a previous version.
- [Affects R14–R16][Needs research] Whether `sensitivity:` should be a free-form frontmatter string, an enum (`public | internal | private`), or inherited from directory conventions (e.g. `wiki/private/*.md`). Defer; informed by actual usage after L1+L2 ships.

## Next Steps

-> `/ce:plan` for structured implementation planning, or run `document-review` on this requirements doc first to stress-test the trust model before handing off.

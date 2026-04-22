---
date: 2026-04-19
topic: session-capture-manifest
---

# Session Capture as Manifest, Summarization on Read

## Problem Frame

Two problems, addressed together:

**1. Error-as-content.** The Cairn Stop hook at `hooks/session-summary` couples session capture to live LLM summarization. When `claude -p --model haiku` fails, early versions wrote the raw error string ("Prompt is too long") as the session file. 33 of 43 files in a user's vault currently contain only that string. The harden commit (`8991932`) added a keyword guard that also silently drops legitimate summaries containing `error`/`failed`/`too long`.

**2. Size-limit fails at summarization.** The underlying LLM call fails on oversized transcripts regardless of where it runs. Moving it from Stop hook to read-time alone would relocate the failure, not fix it.

**Structural root cause:** Cairn treats `sessions/*.md` as the authoritative session record, duplicating content that is already durably stored elsewhere (Claude Code transcripts at `transcript_path`, Entire checkpoints, git history). This forces every session file to carry a fallible LLM-generated summary, which is exactly the brittle step.

**Affected:** every Cairn user whose sessions exceed the effective prompt limit of the summarizer model, plus every downstream skill (`extract`, `query`, `refine`) that treats the session file as the source of truth.

## Requirements

**Capture (Stop hook → `cairn capture-session`)**
- R1. Stop hook never invokes an LLM. It forwards its stdin JSON to `cairn capture-session`, which does the work.
- R2. Capture writes a manifest at `sessions/<timestamp>-<session_id_short>.md`. Filename is chronologically sortable and collision-free. Fields:
  - `session_id` (string, full UUID)
  - `timestamp` (ISO-8601 UTC)
  - `transcript_path` (absolute path; may be null only for pre-migration legacy files)
  - `transcript_hash` (sha256 of transcript file at capture time; enables stale detection)
  - `transcript_size` (bytes, same purpose)
  - `entire_checkpoint` (string, present only when Entire is installed and a checkpoint is recorded)
  - `git_head` (commit SHA at session end)
  - `branch` (branch name at session end)
  - `files_changed` (list of `{path, action}`; see R3)
  - `excerpt` (object `{head, tail}`; see R4)
- R3. `files_changed` is derived from git, not Entire. When an Entire checkpoint is available in the session's commit trailer, diff between the checkpoint commit and `git_head`. Otherwise, take `git status --porcelain` at Stop time (uncommitted working-tree changes). Entire is optional enrichment, not a dependency.
- R4. `excerpt` captures the first and last 1 KB of user/assistant text extracted from the transcript, as a bounded fallback for when the transcript file is later rotated, compacted, or removed by Claude Code. Semantics: field type is `{head: string, tail: string}`; both fields may be empty; truncation occurs on UTF-8 codepoint boundaries; when total extracted text ≤ 2 KB, `head` holds the full content and `tail` is empty; sessions containing only tool-use (no user/assistant text) produce both fields empty.
- R5. Capture appends a one-line entry to `log.md` using timestamp, branch, and files-changed count. No summary text at capture time.
- R6. Capture is idempotent on `session_id`. If a manifest with the same `session_id` already exists, capture exits 0 without overwriting. Resumed sessions (same `session_id`, different Stop event) do not produce a second manifest; the existing manifest is the authoritative record.
- R7. Capture tolerates incomplete transcripts: before reading, it checks that `transcript_path` mtime has been stable for ≥ 500 ms. If not stable after one retry, capture proceeds with a best-effort excerpt and records `excerpt_incomplete: true` in the manifest.
- R8. Capture exits 0 on any partial failure (missing Entire, no git repo, missing transcript). A partial manifest is better than no manifest.

**Summarization (lazy, chunked, on-demand)**
- R9. A new CLI subcommand `cairn summarize <session>` resolves a manifest and produces a cached summary at `sessions/summaries/<timestamp>-<session_id_short>.md`. Skills invoke this subcommand; there is no in-process TypeScript helper.
- R10. Summarization must succeed regardless of transcript size. When the assembled prompt (transcript + system instructions + Entire context) exceeds a configured threshold, `cairn summarize` chunks the transcript and performs map-reduce: per-chunk partial summaries, then a final reduction pass. Output is a single coherent summary; failure mode is the reduction step, which operates on already-bounded input.
- R11. Summarization reads conversation content by resolving `transcript_path` and verifying `transcript_hash` matches. On hash mismatch (rotated/compacted), or when the file is missing, summarization falls back to `excerpt` and writes `degraded: true` in the summary frontmatter.
- R12. Summary frontmatter records the `transcript_hash` it was derived from, enabling automatic cache invalidation: the resolver regenerates the summary when manifest hash ≠ cached summary hash.
- R13. `cairn summarize --all` bulk-fills summaries for every manifest without a current cached summary. Intended for catch-up after install or after a long backlog.
- R14. Summarization failures do not corrupt the manifest. The manifest remains the durable record; the summary file is simply absent and can be retried.

**Migration (safe, classifier-based, reversible)**
- R15. A new command `cairn migrate-sessions` (or subcommand of `cairn doctor`; planning picks) classifies each existing `sessions/*.md` file:
  - **legacy-error**: file body exactly matches one of the known error strings (`Prompt is too long`, etc.) — no frontmatter or empty frontmatter.
  - **legacy-well-formed**: file has valid frontmatter plus a `## Summary` section.
  - **already-migrated**: frontmatter contains the R2 manifest keys (notably `transcript_hash`).
- R16. Migration is dry-run by default. It prints a preview table (classification, destination) and exits. A `--apply` flag performs the mutation. A `--yes` flag skips the confirmation prompt for scripted use.
- R17. `legacy-error` files are moved to `sessions/.trash/<timestamp>-<session_id>.md` (not hard-deleted). Their corresponding entries in `log.md` are removed to prevent dangling references.
- R18. `legacy-well-formed` files are converted in place: frontmatter keys that already map (e.g., `files_changed`, `entire_checkpoint`, `tags`, `decisions`, `open_threads`, `extracted`) are preserved; missing manifest fields (`transcript_path`, `transcript_hash`, `excerpt`) are set to null. The summary body is moved to `sessions/summaries/<timestamp>-<session_id_short>.md`. Note: all 10 pre-migration summaries will have `transcript_path: null` and be permanently un-regenerable; this is accepted as the cost of preserving their curated content.
- R19. Migration is idempotent. `already-migrated` files are skipped without modification. Re-running migration is a no-op.

**Downstream Skill Contract**
- R20. `extract`, `query`, and `refine` read the manifest as the authoritative session record. `lint` is unchanged — it operates on wiki pages, not sessions.
- R21. Skills invoke `cairn summarize <session>` (subprocess) when they need summary text. On success, skills consume the cached summary file.
- R22. When a summary is marked `degraded: true`, skills must surface this visibly in their output — e.g., `extract` prints `Session YYYY-MM-DD: degraded (excerpt-only, no transcript)` alongside its findings. No silent consumption of degraded data.
- R23. Skills handle summary generation failures without crashing the skill run. A failed summary means that session contributes nothing to the skill's output; other sessions proceed.
- R24. `files_changed` is usable as a primary index for queries of the form "which sessions touched X?" without reading transcripts or invoking the summarizer. Linear scan of manifests is acceptable for v1; if vault size causes latency problems later, an index file is a follow-on.

## Success Criteria

- Zero `Prompt is too long` (or any error-as-content) files produced by the capture path, for any transcript size.
- Summarization succeeds on oversized transcripts via chunking, measured by: `cairn summarize` completing on a synthetic 500 KB transcript that would fail a single-shot haiku call.
- No silent data loss or silent degraded consumption: every completed session produces a manifest, every degraded summary is surfaced by consuming skills.
- A fresh vault with 100 sessions occupies under 500 KB of manifests (excluding cached summaries). Budget raised from earlier draft to account for excerpt + hash fields.
- `cairn query "which sessions touched file.ts"` returns results by scanning manifests only — no transcript reads, no LLM calls.
- Users can re-summarize any session at any time by deleting its cached summary and re-running the consuming skill, or via `cairn summarize <session> --force`.
- Existing vaults migrate without data loss: no hard deletes, `.trash/` recoverable, dry-run preview required before mutation.

## Scope Boundaries

- Not doing: automatic background summarization (no cron, no daemon, no separate hook). Bulk fill is opt-in via `cairn summarize --all`.
- Not doing: retention policy for transcripts or checkpoints. Cairn does not manage upstream lifecycles; it records hashes to detect drift and falls back to excerpt when sources are gone.
- Not doing: multi-machine / synced-vault support. The vault is assumed single-host; `transcript_path` is not qualified by hostname. If users sync vaults via iCloud/git, foreign-host manifests will resolve to excerpt-only. This is acceptable for v1.
- Not changing: Cairn's existing public commands (`extract`, `query`, `refine`, `lint`, `doctor`, `init`, `uninstall`). Their user-visible contracts do not change. Internals do.
- Not extending: the `lint` skill. It continues to operate on wiki pages only.
- Not premising on Entire: Entire is optional enrichment. Core capture works without it. `files_changed` comes from git.

## Key Decisions

- **Sessions become an index, not a content store.** The principle is scoped to `sessions/` specifically, not a vault-wide direction. Wiki pages remain content-first markdown. Rationale: storage cost for sessions, free re-summarization, queries that JOIN transcripts with code changes. CAIRN.md template will be updated to reflect the two-level `sessions/` + `sessions/summaries/` layout.
- **Summarization moves to a dedicated CLI subcommand, chunked by default.** Decouples a fallible LLM call from the Stop hook, and eliminates the size-limit failure mode by construction. Success criterion is "summarization works," not "summarization error is silent."
- **Capture logic lives in `cairn capture-session` (TS), not bash.** The Stop hook becomes a thin bash wrapper that forwards stdin. The TS subcommand is testable via Bun, handles frontmatter assembly, excerpt extraction, hash computation, and git/Entire lookup. Keeps cross-platform behavior unified.
- **Filename scheme = `<timestamp>-<session_id_short>.md`.** Combines chronological sort with collision-free idempotency. Short session_id = first 8 chars of the UUID.
- **Excerpt sizing: 1 KB head + 1 KB tail.** Bounded fallback for transcript rotation, not a substitute. Real resilience comes from transcript_hash detection, not from the excerpt itself.
- **Migration is safe by default.** Dry-run required, `.trash/` over hard-delete, log.md cleaned up. Cairn does not silently mutate the user's vault.
- **`files_changed` comes from git, Entire is optional.** Avoids coupling Cairn's primary index capability to Entire's adoption or output-format stability.

## Dependencies / Assumptions

- **Single-host vault.** Users do not sync `~/cairn/` across machines. `transcript_path` is a machine-local absolute path; if the vault moves, manifests fall back to excerpt-only on the foreign host.
- **Claude Code transcripts persist for the session's useful lifetime.** Claude Code may auto-compact or rotate transcripts; Cairn detects this via `transcript_hash` mismatch and degrades gracefully. The design does not depend on transcripts being immutable — only on them existing at capture time.
- **Entire is optional.** Capture and summarization work without it. When installed, `entire_checkpoint` is recorded for richer summarization context only. `files_changed` never depends on Entire.
- **Git repo is present.** Cairn assumes the vault's source project is a git repo; `git_head`, `branch`, and `files_changed` all require this. Without git, capture still writes a manifest with null git fields.
- **Shell environment has `jq`.** Already required by the current hook; retained.
- **Haiku is the default summarizer model.** Chunking makes this work for any transcript size.

## Outstanding Questions

### Resolve Before Planning

_None._

### Deferred to Planning

- [Affects R2][Technical] Exact chunking threshold (transcript size / token estimate). Planning picks a conservative default and makes it configurable.
- [Affects R9][Technical] Whether `cairn migrate-sessions` is a standalone subcommand or a mode of `cairn doctor`. Planning decides based on `doctor.ts` structure.
- [Affects R10][Needs research] Map-reduce prompt design: chunk-summary prompt vs. reduce-summary prompt. Planning drafts both.
- [Affects R3][Technical] When no Entire checkpoint and no uncommitted changes, should `files_changed` be empty, or diff against `HEAD~1`? Edge case; planning picks.

## Next Steps

-> `/ce:plan` for structured implementation planning

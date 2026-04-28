---
title: "refactor: Session capture as manifest, chunked summarization on read"
type: refactor
status: stale
stale_date: 2026-04-28
stale_reason: Superseded by vault trust boundary + lazy retrieval (Layer 1+2, PR #2). The session capture work was implemented differently — the migration tool and doctor additions described here were abandoned in favor of the trust boundary approach.
date: 2026-04-19
origin: docs/brainstorms/2026-04-19-session-capture-manifest-requirements.md
---

# refactor: Session capture as manifest, chunked summarization on read

## Overview

Rewrite the Cairn session-capture pipeline. Today the Stop hook (`hooks/session-summary`, bash) invokes `claude -p --model haiku` at session end to produce a summary file. Oversized transcripts fail mid-pipeline and error strings get written as the session record; the mitigation guard also silently drops legitimate summaries matching `error|failed|too long`.

The refactor splits capture from summarization:

- **Capture**: the Stop hook becomes a thin shell forwarder that calls a new TypeScript CLI subcommand `cairn capture-session`. The subcommand writes a small manifest pointing at the Claude Code transcript + Entire checkpoint + git state. No LLM is invoked at capture.
- **Summarization**: a new TypeScript CLI subcommand `cairn summarize <session>` does chunked/map-reduce summarization on demand. Invoked by downstream skills (`extract`, `query`, `refine`). Output is cached at `sessions/summaries/<name>.md` keyed by transcript hash.
- **Migration**: a new `cairn migrate-sessions` subcommand classifies existing `sessions/*.md` files and safely reshapes them (dry-run default, `.trash/` instead of hard-delete, journal-based resume).

## Problem Frame

See origin: `docs/brainstorms/2026-04-19-session-capture-manifest-requirements.md`. Two problems, addressed together: error-as-content in the vault, and the summarization call failing on oversized transcripts regardless of where it runs. Structural root cause: Cairn treats session markdown files as authoritative content, duplicating state that is already stored durably in Claude Code's transcript tree and Entire's checkpoint store.

## Why This Scope Now

The two reported bugs are narrow and each has a shallow fix. The deeper motivation is an identity decision from the brainstorm (origin Key Decisions): **sessions become an index, not a content store.** This is an architectural bet that unlocks free re-summarization, queries that JOIN transcripts with code changes, and a stable cache independent of upstream mutation. The bug fixes are the forcing function; the manifest-and-summaries split is the actual work.

We explicitly considered the narrower alternative — strip the broken error guard, move `claude -p` into one TS helper with chunked summarization, keep the current vault shape. It ships in 1–2 units and satisfies the Success Criteria as written. It was rejected because:

1. The brainstorm's identity decision (origin Key Decisions) scopes "index not content store" to sessions specifically as a considered design move, not a byproduct.
2. The narrow path keeps the problematic coupling — session file = summary file = content — which means the next time summarization quality or format changes, the vault layout changes too.
3. The narrow path does not enable R24 ("files_changed as a primary index for queries") without layering additional structure on top.

If the maintainer later decides the architectural bet is not worth the cost, the hotfix version of the narrow path remains available as a retreat.

## Requirements Trace

Origin requirements R1–R24 (see origin). Groupings:

- **R1–R8**: Capture — `cairn capture-session` + thin hook forwarder.
- **R9–R14**: Summarization — `cairn summarize` with chunked/map-reduce and hash-keyed cache.
- **R15–R19**: Migration — classifier, dry-run, `.trash/`, journal.
- **R20–R24**: Downstream skill contract — `extract`, `query`, `refine` read manifests as authoritative; surface degraded/failed state; `files_changed` is a queryable index; `lint` untouched.

## Scope Boundaries

- No automatic background summarization. Only on-demand + explicit `cairn summarize --all`.
- No multi-machine / synced-vault support. Single-host assumption for v1.
- No retention management for upstream sources (transcripts, checkpoints). Cairn detects drift via `transcript_hash` and degrades gracefully.
- `lint` skill is unchanged — it operates on wiki pages.
- No new runtime dependency beyond `yaml` (frontmatter parsing).

### Deferred to Separate Tasks

- **Cross-machine `transcript_path` qualification** — future work if users start syncing vaults.
- **Session materialized index (`sessions/index.json`)** — deferred until query latency justifies it.
- **`extract`/`refine` skill internal prompt updates** — this plan updates them to read the new structure but preserves their existing extraction logic; separate passes can re-tune prompts.

## Context & Research

### Relevant Code and Patterns

- **CLI subcommand pattern**: `src/cli.ts:10-14` registers subcommands as `() => import("./commands/X").then(m => m.default)`. Each command in `src/commands/*.ts` exports `defineCommand({ meta, args, async run({ args }) })`. See `src/commands/doctor.ts`, `src/commands/init.ts`, `src/commands/uninstall.ts`.
- **Vault path resolution**: `src/lib/vault.ts:8-25` — `resolveVaultPath(projectDir)` follows `CAIRN_VAULT` env > `.cairn` marker > `~/cairn`. Reuse verbatim.
- **Entire gateway**: `src/lib/entire.ts` — exports `isEntireOnPath`, `isEntireEnabled(cwd)`, `getHeadCheckpointId(cwd)`, `explainCheckpoint(id, cwd)`, `explainCheckpointFull(id, cwd)`. Header comment codifies the rule: *"Cairn never parses Entire's internal formats directly."* Honor it.
- **Hook wrapper**: `hooks/run-hook.cmd` is a bash/cmd polyglot that forwards stdin + args to `hooks/<name>`. No change needed; we only rewrite `hooks/session-summary` itself.
- **Hook config**: `hooks/hooks.json` `Stop` entry already points at `run-hook.cmd session-summary`; unchanged.
- **Test conventions**: `tests/*.test.ts` use `bun:test` (`describe`/`it`/`beforeEach`). Subprocess tests spawn via `Bun.spawn` with piped stdin/stdout (`tests/session-summary.test.ts:62-69`, `tests/init.test.ts:10-14`). Vault scaffolding via `makeTestVault()` helper (`tests/session-summary.test.ts:8-14`).
- **Doctor walk pattern**: `src/commands/doctor.ts:136-147` — `walkForMarkdown` skips dotfile entries, so `sessions/.trash/` is invisible automatically.
- **Current session file frontmatter** (produced by `hooks/session-summary:104-113`): `session_id`, `status`, `extracted`, `files_changed`, `decisions`, `open_threads`, `tags`, optional `entire_checkpoint`. Manifest schema must preserve the keys that `skills/extract/SKILL.md` reads.
- **Inject hook**: `hooks/inject:64-80` concatenates `sessions/*.md` bodies into SessionStart context. After the refactor, manifests are small enough to include as-is; the hook must skip `sessions/summaries/` and `sessions/.trash/` to avoid duplication.
- **Constants**: `src/lib/constants.ts` — `VERSION`, `VAULT_DIRS = ["wiki","raw","sessions"]`, `ENTIRE_CHECKPOINT_BRANCH`. Add `"sessions/summaries"` and `"sessions/.trash"`. Bump VERSION when the refactor ships.
- **Template**: `templates/CAIRN.md` and `skills/cairn/SKILL.md` describe `sessions/` semantics — both need updating.

### Institutional Learnings

- `docs/solutions/` does not exist in this repo. No prior patterns captured. Consider bootstrapping one with `compound-engineering:ce-compound` after this lands.

### External References

Not used. Codebase patterns are sufficient.

## Key Technical Decisions

- **Add `yaml` as a runtime dependency** for frontmatter parsing and serialization. Only new runtime dep; small, stable, widely used. Alternative (hand-rolled parser) rejected because the summarizer will also read and write cached summary frontmatter and we want one parser everywhere.
- **Hashing uses Node `crypto.createHash("sha256")`** (stdlib, zero-dep). Stream the transcript file in 64 KB chunks to bound memory.
- **Manifest filename = `<ISO-timestamp>-<session_id_short>.md`** where `session_id_short` is the first 8 hex chars of the UUID. Chronological sort preserved. Idempotency and uniqueness come from the lock below, not the filename.
- **Atomic idempotency via O_EXCL lockfile on full `session_id`.** Before any manifest work, `cairn capture-session` calls `fs.openSync(".cairn/sessions/<session_id>.lock", "wx")` — atomic create-or-fail. On EEXIST, exit 0. On success, write manifest via temp-then-rename, then delete the lockfile. This avoids the TOCTOU race in a glob-then-rename approach and makes idempotency a single atomic syscall on the full UUID (not the 8-char short id). Stale lockfiles (> 24h) are garbage-collected by `cairn doctor`.
- **No `flock` dependency.** Neither `fs.flockSync` (doesn't exist in Bun/Node) nor the `flock(1)` binary (absent on macOS by default) is used. All concurrency protection is O_EXCL lockfile-based, via a single helper in `src/lib/lockfile.ts`: `withExclusiveLock(path, fn)` creates `<path>` with `wx`, runs `fn`, deletes `<path>`; stale-lock timeout + retry budget configurable.
- **`log.md` and `.cairn/migration-journal.json` both serialized via `withExclusiveLock`** (using the same `lockfile.ts` helper, not a separate module). `log.md` writers use `.cairn/log.lock`; migration uses `.cairn/migration.lock`. Journal itself is written via write-temp-then-rename inside the lock.
- **Migration journal at `.cairn/migration-journal.json`** tracks each classified file's state (`classified|moved|converted|skipped|error`). Validated against a schema on load; malformed journal aborts with a clear error and a pointer to manual recovery. On resume, sessions/ directory listing is re-hashed and compared to the journal's hash — mismatch aborts.
- **Chunking threshold: derived from haiku's input budget, not guessed.** Default `CAIRN_SUMMARIZE_CHUNK_BYTES = 60000` (approximately 15k tokens with overhead), configurable. Reduction is **hierarchical**: if the reduce-step prompt would exceed the threshold, partials are grouped and re-reduced recursively until one output remains. Single turns larger than the threshold are truncated in-place with an inline marker (`[... N KB of tool output truncated ...]`) and a warning surfaced on the summary's frontmatter (`truncated_turns: N`). Guarantees termination at log(N) depth; guarantees no per-call prompt exceeds the threshold.
- **Cache key: `transcript_hash` recorded in both manifest and summary frontmatter.** Mismatch triggers regeneration. **Exceptions (skip regeneration):** summary frontmatter has `user_edited: true`, OR source manifest has `transcript_path: null` with non-empty summary body (migrated case). **`--force` is NOT destructive:** existing summary is moved to `sessions/.trash/summaries/<name>-<timestamp>.md` before overwrite, preserving recoverability. Only an additional `--destructive` flag bypasses trashing. `user_edited: true` is set automatically by a dedicated command (`cairn summaries pin <session>`); users are not expected to hand-edit frontmatter.
- **Cache invalidation is manifest-hash-based, not transcript-hash-based.** The cache key on summary frontmatter is a hash of the manifest's R2 fields (excluding `excerpt` for hash stability). Since the manifest is intentionally immutable post-capture, the cache is stable across Claude Code auto-compaction. Transcript hash mismatch at read time still triggers degraded-excerpt fallback, but does not invalidate an existing cached summary.
- **Legacy-error classifier uses an explicit const array** at `src/lib/migration.ts`, initially: `["Prompt is too long"]`. Extension via CLI flag is deferred (YAGNI until a real user hits a different error string).
- **Capture failures log to `.cairn/capture-errors.log`** (append-only, per-line JSON). The bash forwarder (`hooks/session-summary`) writes a `{stage: "bash", reason: "bun-not-found"}` line if the `bun` resolution fallback fails. `cairn doctor` surfaces the count from the last 7 days and checks that `bun` is resolvable from a PATH-stripped shell.
- **Files_changed library at `src/lib/git.ts`** — not `entire.ts`. Entire stays a pure Entire gateway; git helpers are separate. `entire.ts` is only used for optional checkpoint context in summarization.
- **Filesystem portability:** manifest writes, journal writes, and `.trash/` moves all use `fs.cpSync + fs.unlinkSync` as a fallback on EXDEV (symlinked vaults across volumes). Filenames are NFC-normalized before comparing / globbing. Migration pre-flight checks writability of `sessions/`, `sessions/.trash/`, `sessions/summaries/`, `.cairn/`, and `log.md`; aborts dry-run-cleanly on any failure.

## Open Questions

### Resolved During Planning

- **Where does the summarize helper live?** New `cairn summarize` CLI subcommand. Skills invoke it via Bash tool call. Matches existing shell-out pattern in `skills/extract/SKILL.md:82-86`.
- **Where does capture logic live?** New `cairn capture-session` subcommand (TS). `hooks/session-summary` becomes a ~5-line forwarder.
- **Migration command placement?** Standalone `cairn migrate-sessions`. `doctor` stays report-only.
- **`cairn summarize <session>` identifier grammar?** Accepts (in order): full path to manifest, filename relative to `sessions/`, or `session_id` prefix ≥ 8 chars. Ambiguous prefix → error with disambiguation list.

### Deferred to Implementation

- Exact chunking prompt templates (map and reduce). Will be drafted during Unit 3; may need tuning against real large transcripts.
- `mtime` stability threshold on slow filesystems (NFS/iCloud). Default 500 ms with one retry; expose `CAIRN_TRANSCRIPT_STABLE_MS` env for override. Tune if beta users report false-positive `excerpt_incomplete`.
- How `extract` should render degraded/failed session summaries (inline warning vs. footnote). Unit 6 picks one format; adjust if UX feedback warrants.
- Edge case for `files_changed` when session has neither Entire checkpoint nor uncommitted changes (clean working tree, no commits). Likely empty list; revisit if this turns out to misrepresent real sessions.

## High-Level Technical Design

> *This illustrates the intended approach and is directional guidance for review, not implementation specification. The implementing agent should treat it as context, not code to reproduce.*

### Capture flow

```
Claude Code Stop event
        │
        ▼
hooks/run-hook.cmd session-summary  (polyglot wrapper, forwards stdin)
        │
        ▼
hooks/session-summary  (thin bash: exec bun <plugin>/src/cli.ts capture-session)
        │  stdin = {session_id, transcript_path, …}
        ▼
cairn capture-session
   ├── resolve vault
   ├── fs.openSync(.cairn/sessions/<session_id>.lock, "wx")
   │     └── on EEXIST → exit 0 (atomic idempotency)
   ├── open transcript_path fd, stability-retry, then use fd for reads
   ├── compute sha256 + extract head/tail excerpt from held fd
   ├── collect git_head, branch, files_changed (via src/lib/git.ts)
   ├── if Entire enabled → record entire_checkpoint + fetch context
   ├── build filename <timestamp>-<session_id_short>.md
   ├── write manifest .tmp → atomic rename (EXDEV fallback: cp+unlink)
   ├── withLogLock → append one-line entry to log.md
   ├── delete session_id lockfile
   └── on fatal error: append .cairn/capture-errors.log, exit non-zero
```

### Summarize flow

```
skill needs summary
        │
        ▼
cairn summarize <session> [--force [--destructive]] [--json]
   ├── resolve <session> argument to a manifest path (path / filename / session_id prefix ≥8)
   ├── compute current manifest_hash (sha256 of R2 fields minus excerpt)
   ├── check sessions/summaries/<name>.md
   │     ├── manifest_hash matches + not user_edited → return cached path
   │     ├── user_edited: true → return cached (unless --force)
   │     └── null transcript + non-empty body → return cached (migrated)
   ├── --force: move existing cached summary to .trash/summaries/<name>-<ts>.md
   │             (skip trashing only with --force --destructive)
   ├── resolve transcript_path, verify transcript_hash
   │     ├── hash matches → read transcript, extract turns
   │     └── hash mismatch / file missing → use manifest.excerpt, flag degraded: true
   ├── truncate oversized single turns in-place with marker; track truncated_turns
   ├── if extracted-text bytes > CAIRN_SUMMARIZE_CHUNK_BYTES:
   │     ├── chunk on turn boundaries, each ≤ threshold
   │     ├── summarize each chunk (CHUNK_PROMPT)
   │     └── reduce recursively: if reduce prompt > threshold, group + re-reduce
   │   else:
   │     └── single-shot summarize
   ├── write sessions/summaries/<name>.md atomically
   │   (frontmatter: manifest_hash, transcript_hash, generated_at, degraded?, chunked?, truncated_turns?)
   ├── emit stdout: absolute path (or JSON if --json)
   └── on failure: exit non-zero to stderr; manifest + cached summary untouched
```

### Migration flow

```
cairn migrate-sessions [--apply] [--yes]
   ├── withMigrationLock (fails fast on concurrent invocation)
   ├── writability pre-flight on sessions/, sessions/.trash/, sessions/summaries/, .cairn/, log.md
   ├── compute sessions_listing_hash
   ├── load .cairn/migration-journal.json if present
   │     ├── schema-invalid → abort with recovery guidance
   │     └── sessions_listing_hash changed → abort (vault changed since interrupt)
   ├── classify each sessions/*.md (NFC-normalized):
   │     ├── body trimmed == "Prompt is too long" → legacy-error
   │     ├── has {transcript_hash, manifest_hash} → already-migrated
   │     ├── has frontmatter + ## Summary section → legacy-well-formed
   │     └── otherwise → unknown (skip with warning)
   ├── if not --apply: print preview table, release lock, exit 0
   ├── confirm (unless --yes)
   ├── for each classified file in journal:
   │     ├── legacy-error → move to sessions/.trash/ (EXDEV fallback), strip log.md entry
   │     ├── legacy-well-formed → write new manifest (transcript_path=null, manifest_hash=null),
   │     │     move body to sessions/summaries/<new-name>.md with user_edited: true
   │     ├── already-migrated → skip
   │     └── unknown → skip with warning
   └── on success: delete migration-journal.json
```

## Implementation Units

- [ ] **Unit 1: Foundation — libraries, constants, types**

**Goal:** Introduce shared helpers the rest of the plan depends on. No user-visible behavior change.

**Requirements:** R2, R3, R4, R6, R7 (support), R11, R12 (support).

**Dependencies:** None.

**Files:**
- Create: `src/lib/frontmatter.ts` (parse/serialize YAML frontmatter + body split)
- Create: `src/lib/git.ts` (`headCommit(cwd)`, `currentBranch(cwd)`, `filesChangedSince(fromSha, cwd)`, `uncommittedChanges(cwd)`)
- Create: `src/lib/hash.ts` (`sha256File(path)`, streams via `crypto.createHash`)
- Create: `src/lib/lockfile.ts` (`withExclusiveLock(lockPath, fn, { staleMs?, retryMs?, retries? })` — O_EXCL-based lock; writes PID to lockfile; auto-reclaims locks older than `staleMs` (default 5 min); retries with backoff up to `retries`; deletes lockfile on success. `withLogLock(vaultPath, fn)` and `withMigrationLock(vaultPath, fn)` are thin convenience wrappers.)
- Create: `src/lib/manifest.ts` (types: `SessionManifest`, `SessionSummaryFrontmatter`; helpers to read/write manifest frontmatter; includes `shortSessionId(uuid)` returning the first 8 hex chars; exposes `excerpt_incomplete` and summary-side fields `degraded`, `chunked`, `user_edited`, `generated_at` as optional in the schema)
- Modify: `src/lib/constants.ts` — add `"sessions/summaries"` and `"sessions/.trash"` to `VAULT_DIRS`; add `CAIRN_DIR = ".cairn"` and `MIGRATION_JOURNAL` const; bump `VERSION` to `"0.6.0"`.
- Modify: `src/lib/vault.ts` — ensure `scaffoldVault` creates new subdirs; no behavior change for existing vaults beyond idempotent `mkdir -p`.
- Modify: `package.json` — add `yaml` to `dependencies`; bump `version` to match constants.
- Test: `tests/frontmatter.test.ts`, `tests/git.test.ts`, `tests/hash.test.ts`, `tests/lockfile.test.ts`, `tests/manifest.test.ts`

**Approach:**
- `frontmatter.ts`: wraps `yaml` package. Round-trip parse/serialize. Handles missing frontmatter, malformed frontmatter, empty body.
- `git.ts`: Bun.spawn `git` subprocess. Null returns on non-git dirs. No error on missing commits — empty list.
- `hash.ts`: streaming sha256 of transcript file. Missing file → null.
- `lockfile.ts`: O_EXCL lockfile via `fs.openSync(path, "wx")`. Write `{pid, createdAt}` JSON to the lockfile on acquire. Delete on release. On EEXIST, check lockfile age — if older than `staleMs`, overwrite (prior process crashed); otherwise retry with backoff up to `retries` times, then throw. No platform primitives required. Works cross-platform (tested on Bun via tmp dir).
- `manifest.ts`: typed schema matching R2 exactly. Validator that rejects manifests missing required keys.

**Patterns to follow:**
- `src/lib/entire.ts` for subprocess + null-return pattern.
- `src/lib/vault.ts` for path resolution precedence.

**Test scenarios:**
- Happy path: `frontmatter.parse` round-trips a known-good manifest.
- Edge case: file with no frontmatter returns `{ data: {}, body: <whole-content> }`.
- Edge case: empty file returns `{ data: {}, body: "" }`.
- Error path: `frontmatter.parse` on a malformed YAML block throws with a file path in the message.
- Happy path: `sha256File` matches `openssl dgst` output on a known fixture.
- Edge case: `sha256File` on a missing file returns `null`, not throws.
- Happy path: `git.filesChangedSince(headSha, cwd)` returns expected `{path, action}` array against a tmp git repo fixture.
- Edge case: `git.filesChangedSince` on non-git dir returns empty array.
- Integration: `withExclusiveLock` serializes two concurrent appends (spawn two bun processes writing simultaneously; assert output interleaving is line-atomic, not byte-interleaved).
- Edge case: stale-lock reclaim — create a lockfile with `{pid: 99999, createdAt: <10min ago>}`, call `withExclusiveLock`, assert it reclaims and succeeds.
- Edge case: retry budget exhaustion — hold a lock on a separate process, call `withExclusiveLock` with `retries: 2`, assert it throws with a descriptive error.
- Happy path: `manifest.read` and `manifest.write` round-trip all R2 fields including optional ones (`entire_checkpoint`, `excerpt_incomplete`).
- Happy path: summary-frontmatter round-trip preserves `transcript_hash`, `generated_at`, `degraded`, `chunked`, `user_edited` without field loss.
- Happy path: `shortSessionId(uuid)` returns exactly 8 hex chars; deterministic for a given UUID.

**Verification:** `bun test tests/frontmatter.test.ts tests/git.test.ts tests/hash.test.ts tests/lockfile.test.ts tests/manifest.test.ts` green; `bun run lint` clean.

---

- [ ] **Unit 2: `cairn capture-session` + hook rewrite**

**Goal:** Replace `hooks/session-summary`'s LLM-invoking body with a thin forwarder, and implement the TS subcommand that writes manifests.

**Requirements:** R1, R2, R3, R4, R5, R6, R7, R8.

**Dependencies:** Unit 1.

**Files:**
- Create: `src/commands/capture-session.ts`
- Modify: `src/cli.ts` — register `"capture-session"` in `subCommands`.
- Rewrite: `hooks/session-summary` — replace the 160-line bash body with a ~5-line forwarder: `exec bun "${CLAUDE_PLUGIN_ROOT}/src/cli.ts" capture-session`. Preserve stdin passthrough and the `set -e` safety. If `bun` is not on PATH, the forwarder must try `$HOME/.bun/bin/bun` as a fallback and, on total failure, append a structured JSON line to `.cairn/capture-errors.log` directly from bash and exit 0 (Claude Code must not see a failed hook).
- Test: `tests/capture-session.test.ts`, update `tests/session-summary.test.ts` to assert the thin-forwarder behavior and drop obsolete LLM-path cases.

**Approach:**
- Subcommand reads stdin as JSON (Claude Code hook payload). Extracts `session_id`, `transcript_path`.
- Resolves vault via `resolveVaultPath(process.cwd())`.
- **Atomic idempotency (first action):** `fs.openSync(".cairn/sessions/<session_id>.lock", "wx")`. On EEXIST, exit 0 immediately without any further work. On success, the returned fd is held in a `try/finally` that deletes the lockfile on completion. This is the single source of truth for "have we captured this session_id yet" — no glob scan, no TOCTOU window.
- Builds filename `<timestamp>-<session_id_short>.md`. Timestamp from a UTC ISO-8601 formatter that matches the existing scheme (`YYYY-MM-DDTHH-MM-SS`).
- **Stability + read via open-and-hold:** open `transcript_path` with `fs.openSync(path, "r")` to hold the fd, then stat via the fd, sleep 500 ms (configurable via `CAIRN_TRANSCRIPT_STABLE_MS`), stat the fd again. If mtime drifted, one retry with the same fd. If still drifting, proceed with the fd anyway and set `excerpt_incomplete: true`. All subsequent reads (hash + excerpt) go through this fd, so Claude Code rotating the file on-disk does not change what this capture sees.
- Extract excerpt: streaming JSONL parser over the held fd; collects user/assistant text; `head` = first 1024 UTF-8 codepoints joined, `tail` = last 1024. UTF-8-safe truncation via codepoint-by-codepoint slicing.
- Compute `transcript_hash` (Unit 1 `sha256File` variant that accepts an fd), `transcript_size`.
- Collect git state via Unit 1 `git.ts`. If Entire's `getHeadCheckpointId` returns non-null, record `entire_checkpoint`, and compute `files_changed` as `filesChangedSince(checkpointSha)`. Otherwise `files_changed = uncommittedChanges()`.
- Write manifest: `<path>.tmp` then `fs.renameSync`. On EXDEV (symlink to another volume), fall back to `fs.cpSync + fs.unlinkSync`.
- Append `log.md` via `withLogLock`. Format: `## [YYYY-MM-DD] session | <branch> | <N> files | <session_id_short>`. The `session_id_short` suffix makes migration's line-removal unambiguous.
- On any thrown error: append JSON line to `.cairn/capture-errors.log` (`{ts, session_id, error, stack}`), exit 1 (the hook wrapper still forces exit 0 at the Stop level, so Claude Code isn't disrupted).

**Execution note:** Test-first for the subcommand. Start with a failing integration test that invokes the compiled CLI via `Bun.spawn` with a fixture JSONL transcript and asserts manifest contents.

**Patterns to follow:**
- `src/commands/doctor.ts` for subcommand structure, args, exit codes.
- `tests/session-summary.test.ts:62-69` spawn pattern for integration tests.

**Test scenarios:**
- Happy path: capture with transcript + Entire checkpoint produces a manifest with all R2 fields populated, `files_changed` derived from git diff against checkpoint commit.
- Happy path: capture without Entire falls back to `git status --porcelain` for `files_changed`.
- Happy path: manifest filename matches `<ISO>-<8hex>.md` scheme.
- Edge case: empty transcript (no user/assistant text) produces `excerpt: {head: "", tail: ""}`.
- Edge case: transcript <2 KB → `head` holds full content, `tail` is empty.
- Edge case: transcript with UTF-8 multibyte (emoji, CJK) truncates on codepoint boundary, not byte.
- Edge case: idempotency — running capture twice with same `session_id` produces one manifest, second invocation exits 0 without writing.
- Edge case: no git repo → manifest has `git_head: null`, `branch: null`, `files_changed: []`.
- Error path: missing transcript_path → manifest still written with `transcript_hash: null` and `excerpt: {head: "", tail: ""}`; capture exits 0 (partial-failure tolerance per R8).
- Error path: non-JSON stdin → capture logs to `.cairn/capture-errors.log` and exits 1.
- Integration: running against a tmp vault asserts `log.md` gets one new line, and concurrent captures (two `Bun.spawn` invocations with distinct session_ids) produce two manifests and two log lines without interleaving.

**Verification:** `bun test tests/capture-session.test.ts` green; manually trigger hook in a dev session and confirm a well-formed manifest lands under `~/cairn/sessions/`; no `Prompt is too long` or other error strings anywhere in the vault; `.cairn/capture-errors.log` absent on a clean run.

---

- [ ] **Unit 3: `cairn summarize` with chunked/map-reduce**

**Goal:** Produce cached summary files from manifests, on demand, regardless of transcript size.

**Requirements:** R9, R10, R11, R12, R13, R14.

**Dependencies:** Unit 1, Unit 2.

**Files:**
- Create: `src/commands/summarize.ts`
- Create: `src/lib/summarizer.ts` (prompt assembly, chunking, LLM invocation, cache check)
- Modify: `src/cli.ts` — register `"summarize"` and `"summaries"` in `subCommands` (the latter for the `pin` subcommand below).
- Create: `src/commands/summaries.ts` (subcommand tree: `cairn summaries pin <session>` / `unpin <session>`)
- Test: `tests/summarize.test.ts`

**Approach:**
- `cairn summarize <session>` argument parser:
  - If `<session>` is an absolute or relative path to an existing manifest → use it.
  - Else if it matches `sessions/<name>.md` → use that.
  - Else treat as `session_id` prefix; if `length >= 8`, glob for matching manifest; unique → use; ambiguous → error listing matches; no match → error.
- **Cache key is `manifest_hash`**, computed from the R2 manifest fields excluding `excerpt`. Recorded on both manifest (cache is computed on read) and on the cached summary frontmatter as `manifest_hash`. This is stable across Claude Code auto-compaction.
- Cache short-circuit:
  - If `sessions/summaries/<name>.md` exists with `manifest_hash == current manifest_hash` → print path and exit 0.
  - If summary has `user_edited: true` → print path and exit 0 (skip unless `--force`).
  - If `manifest.transcript_path == null` and summary body is non-empty → print path and exit 0 (migrated case preservation).
- Transcript resolution:
  - If `transcript_path` exists and `sha256File` matches `transcript_hash` → read transcript, extract turns.
  - Else → use `manifest.excerpt` as the conversation source and set `degraded: true` in output frontmatter.
- Chunking (hierarchical reduction):
  - If extracted-text bytes ≤ `CAIRN_SUMMARIZE_CHUNK_BYTES` (default 60000) → single-shot summarize via `claude -p --model haiku`.
  - Else → chunk on turn boundaries. Summarize each chunk with `CHUNK_PROMPT`. Feed the partial summaries to `REDUCE_PROMPT`; if the reduce prompt itself would exceed the threshold, recursively group partials into sub-batches, reduce each sub-batch, then reduce the sub-batch outputs. Termination: at most `log_base2(N)` reduction levels where N = number of initial chunks.
  - **Single turn larger than threshold:** truncate the turn in-place with an inline marker `[... N KB of tool output truncated ...]`. Summary frontmatter records `truncated_turns: N`.
- Prompts assembled in `src/lib/summarizer.ts` as two template strings: `CHUNK_PROMPT` and `REDUCE_PROMPT`. Both retain the existing output contract (frontmatter + `## Summary` + `## Extraction Candidates`) so `extract` keeps working.
- Write summary via write-temp-then-rename with frontmatter including `manifest_hash`, `transcript_hash`, `generated_at`, `degraded`, optional `chunked: true`, optional `truncated_turns`.
- Failures: exit non-zero, do not write a partial summary. Manifest untouched.
- `--all` flag iterates every manifest whose summary is missing or manifest-hash-mismatched. Progress line per session (`[N/M] summarizing <name>...`). On per-session failure, logs to stderr and continues.
- **`--force` is non-destructive by default.** Moves the existing summary to `sessions/.trash/summaries/<name>-<ISO-timestamp>.md` before regeneration, preserving user intent. Only `--force --destructive` skips trashing. Unit test scenarios reflect this.
- **`cairn summaries pin <session>`** sets `user_edited: true` on the cached summary frontmatter. `unpin` clears it. This is how user-edit protection is declared — users are not expected to hand-edit YAML.
- **Stdout contract for skills:** on success, the last stdout line is the absolute path of the cached summary file. Optional `--json` emits `{"path", "cached": bool, "degraded": bool, "chunked": bool, "truncated_turns": number}` on a single line. Non-zero exit writes a one-line error to stderr.

**Execution note:** Test-first for the cache-short-circuit logic. The LLM invocation is stubbable via `CAIRN_SUMMARIZE_COMMAND` env — an absolute path to a script that receives identical stdin and args as `claude -p --model haiku` and prints a summary-shaped output. Tests set this env to a fixture script at `tests/fixtures/fake-claude.sh` that returns canned output keyed by stdin hash. Documented in `summarizer.ts` JSDoc.

**Patterns to follow:**
- `src/lib/entire.ts` for the subprocess-with-null-return shape when calling `claude`.
- `src/commands/doctor.ts` for progress-style stdout.

**Test scenarios:**
- Happy path: single-shot summarize on a small transcript writes a well-formed summary file with `manifest_hash` in frontmatter; second invocation short-circuits via manifest_hash match.
- Happy path: chunked summarize on a 500 KB synthetic transcript writes one coherent summary; assert `chunked: true` in frontmatter.
- Happy path: resolver accepts full path, relative `sessions/` path, and 8-char session_id prefix.
- Happy path: hierarchical reduction — 12 chunks with per-chunk summaries of ~10 KB force a second-level reduction; assert termination and valid output.
- Happy path: `--json` stdout format emits the documented single-line JSON on success.
- Edge case: ambiguous session_id prefix returns error listing all matches.
- Edge case: degraded path — manifest with null `transcript_path` and migrated body → summary preserved, not regenerated.
- Edge case: `user_edited: true` flag on cached summary → skip regeneration; `--force` WITHOUT `--destructive` trashes existing summary to `.trash/summaries/` before regeneration; `--force --destructive` overwrites without trashing.
- Edge case: single turn larger than threshold → turn truncated with marker, `truncated_turns: 1` in summary frontmatter, no `Prompt is too long` error.
- Edge case: Claude Code auto-compact simulated — transcript file content mutated (hash changes), manifest_hash unchanged → cache hit, no regeneration (asserts hash choice is correct).
- Edge case: `transcript_hash` mismatch at read time with manifest_hash matching → if no cached summary exists, regenerate with `degraded: true` using excerpt; if cached exists, return cached unchanged.
- Happy path: `cairn summaries pin <session>` sets `user_edited: true` on cached frontmatter; subsequent `cairn summarize --force` (without `--destructive`) trashes the pinned version to `.trash/summaries/`.
- Error path: `claude` returns nonzero exit → `summarize` exits nonzero, summary file not written.
- Error path: cache short-circuit is correct even when the summary has different frontmatter key order.
- Integration: `--all` against a vault with 3 manifests, 1 already cached, 1 user-edited, 1 new → produces output lines `[1/3] cached`, `[2/3] user-edited`, `[3/3] summarizing...`, writes exactly one new summary.
- Integration: `--force --all` regenerates all three regardless of cache state.

**Verification:** `bun test tests/summarize.test.ts` green; manual run against a real oversized transcript produces a coherent summary without error; `sessions/summaries/*.md` files have valid frontmatter and `## Summary` sections.

---

- [ ] **Unit 4: `cairn migrate-sessions`**

**Goal:** Safely reshape an existing vault into the manifest layout. Dry-run default, `.trash/` over hard-delete, journal-based resume.

**Requirements:** R15, R16, R17, R18, R19.

**Dependencies:** Unit 1 (frontmatter, manifest schema). Does NOT depend on Units 2–3 (migration doesn't invoke capture or summarize).

**Files:**
- Create: `src/commands/migrate-sessions.ts`
- Create: `src/lib/migration.ts` (classifier, journal read/write, per-file actions)
- Modify: `src/cli.ts` — register `"migrate-sessions"`.
- Test: `tests/migrate-sessions.test.ts`

**Approach:**
- **Mutual exclusion:** entire run is wrapped in `withMigrationLock` (uses `src/lib/lockfile.ts`). Two concurrent `migrate-sessions --apply` invocations: the second exits with a clear message pointing at the first's PID.
- **Writability pre-flight:** before classification, stat-and-test-write on `sessions/`, `sessions/.trash/`, `sessions/summaries/`, `.cairn/`, and `log.md`. Abort with a clear error listing unwritable paths; migration is 100% read until this check passes.
- **Filename normalization:** all filenames NFC-normalized before classification, comparison, and glob. Prevents mismatch on APFS (NFD) when comparing against journal entries.
- Classifier in `src/lib/migration.ts`:
  - Read frontmatter via Unit 1 helper.
  - `legacy-error`: body (trimmed, NFC-normalized) **exactly equals** any string in the const array `LEGACY_ERROR_STRINGS = ["Prompt is too long"]`. Substring matching is rejected as too risky (false-positives on real summaries). The `--legacy-errors <file>` flag is removed from scope — YAGNI.
  - `already-migrated`: frontmatter contains both `transcript_hash` and `manifest_hash` keys (matches the new schema exactly; avoids treating partially-migrated files as done).
  - `legacy-well-formed`: has any frontmatter + `## Summary` heading in body, and is not `already-migrated`.
  - `unknown`: anything else — skipped with warning, preserved in place.
- Dry-run (default): classify every file, print a table (file → class → planned action), exit 0. No vault mutation whatsoever.
- `--apply`:
  - Acquire migration lock.
  - Compute `sessions_listing_hash` (sha256 of the sorted filename list in `sessions/`).
  - If `.cairn/migration-journal.json` exists: load, schema-validate (reject with guidance on corruption), verify its `sessions_listing_hash` still matches the current listing (reject with "vault changed since interrupted migration, manual intervention needed" if not).
  - Else, build a fresh journal including `sessions_listing_hash`. Write journal via write-temp-then-rename. Prompt for confirmation unless `--yes`.
  - For each journal entry, execute the mapped action, then persist journal state via write-temp-then-rename after each successful file (so crashes resume cleanly). All mutations use write-temp-then-rename; all cross-volume moves fall back to `fs.cpSync + fs.unlinkSync` on EXDEV.
    - `legacy-error`: move file to `sessions/.trash/<name>.md` (create dir if absent). Inside `withLogLock`, strip matching `log.md` line — match on `session_id_short` suffix (per Unit 2's new log format) when present; fall back to date-prefix match for very old entries.
    - `legacy-well-formed`: build manifest from existing frontmatter; `transcript_path: null`, `transcript_hash: null`, `manifest_hash: null`, `excerpt: {head: "", tail: ""}`. Preserve `files_changed`, `decisions`, `open_threads`, `tags`, `extracted`, `entire_checkpoint` where present. Write manifest with new filename scheme. Move body to `sessions/summaries/<new-name>.md`; the migrated summary gets `user_edited: true` in its frontmatter to protect it from future regeneration. Delete original file.
    - `already-migrated` / `unknown`: skip, mark in journal as done.
  - On full completion, delete journal via `fs.unlinkSync` (unlink is atomic). Release migration lock.

**Execution note:** Characterization-first for migration. Before any implementation, commit a fixture vault in `tests/fixtures/vault-pre-migration/` that includes the three known categories. Tests run against a copy, assert exact end-state, so any regression on user-data handling is visible.

**Patterns to follow:**
- `src/commands/init.ts` for scaffolding flow with user-visible progress.
- `src/commands/doctor.ts` for table-style output.

**Test scenarios:**
- Happy path (dry-run): vault with 33 legacy-error + 10 legacy-well-formed + 2 already-migrated prints exactly 45 classification rows, no mutation.
- Happy path (`--apply`): same vault, 33 files move to `.trash/`, 10 get new manifests + summaries, 2 untouched, journal is deleted after success.
- Edge case: unknown-class file (valid frontmatter, no `## Summary`, no `transcript_hash`) is classified `unknown`, skipped with a warning, preserved in-place.
- Edge case: `legacy-well-formed` file preserves its `files_changed`, `decisions`, `open_threads`, `tags`, `extracted`, `entire_checkpoint` frontmatter values through conversion.
- Edge case: `log.md` entries corresponding to moved legacy-error files are removed; other entries untouched.
- Edge case: a session file whose body is exactly `Prompt is too long` plus a trailing newline classifies as `legacy-error`; a file whose body contains that string as a quoted phrase inside a larger summary classifies as `unknown` (not `legacy-error`).
- Resume: simulate crash mid-apply (kill after 15 of 33 moves), rerun `--apply`, assert remaining 18 complete and journal deleted.
- Idempotency: run `--apply` twice on a fully migrated vault; second run is a no-op.
- Error path: migration run without `--apply` never mutates the vault (permissions-test: set `sessions/` read-only, run dry-run, assert no EACCES).
- Error path: concurrent `--apply` invocations — second invocation exits with "migration in progress, PID X" message, vault untouched.
- Error path: corrupted journal (invalid JSON, truncated) — migration exits with guidance, vault untouched.
- Error path: journal's `sessions_listing_hash` no longer matches (user moved files in between) — migration exits, does not resume.
- Edge case: EXDEV on a symlinked vault (symlink to another volume) — `.trash/` move falls back to cp + unlink; final state identical to non-EXDEV case.
- Integration: after `--apply`, `cairn doctor` reports the vault as healthy with the new directory shape.

**Verification:** `bun test tests/migrate-sessions.test.ts` green; manual run on a backup of the real 43-file vault produces dry-run output matching expectations; after `--apply --yes`, `sessions/.trash/` holds 33 files, `sessions/summaries/` holds 10, `sessions/` holds 10 manifests + 2 already-migrated.

---

- [ ] **Unit 5: Doctor additions — capture errors, migration status, sessions/ health**

**Goal:** Make new pipeline health observable through `cairn doctor`.

**Requirements:** Supports R8 (capture failure observability), R20 (manifest as authoritative — doctor validates).

**Dependencies:** Units 1–4 (so doctor can probe the new shape).

**Files:**
- Modify: `src/commands/doctor.ts`
- Test: update `tests/doctor.test.ts` (or create if absent).

**Approach:**
- New checks in the existing doctor walk:
  - `.cairn/capture-errors.log` line count in the last 7 days. If >0, report as warning (`! 3 capture errors in last 7 days — see .cairn/capture-errors.log`).
  - `.cairn/migration-journal.json` presence. If present, report as warning (`! migration in progress — run 'cairn migrate-sessions --apply' to resume or delete journal`).
  - **Bun resolvability from a PATH-stripped shell.** Run `env -i PATH="/usr/bin:/bin:/usr/local/bin:$HOME/.bun/bin" command -v bun`; if empty, warn (`! bun not found on hook PATH — Stop hook will fail`). This catches the primary silent-capture regression.
  - **Stale session lockfiles.** List `.cairn/sessions/*.lock`; any older than 24 hours are deleted (they indicate a crashed capture that held the lock). Report count as an info line.
  - **Legacy session files still present.** Count `sessions/*.md` entries without manifest schema keys (matches `unknown` + `legacy-error` + `legacy-well-formed` classifier output). If >0, warn with: `! N legacy session files detected — run 'cairn migrate-sessions' to migrate`.
  - `sessions/` directory layout:
    - Count of manifests at top level (filter: files with `manifest_hash` frontmatter key).
    - Count of cached summaries under `sessions/summaries/`.
    - Count of trashed files under `sessions/.trash/` (non-recursive, includes `.trash/summaries/`).
    - Flag any manifest missing a required R2 field.
- Modify `walkForMarkdown`: add an explicit skip-list parameter for `sessions/summaries/`, `sessions/.trash/`, and `.cairn/`. The existing dotfile skip handles `.trash/` and `.cairn/`; the explicit skip is defensive and makes the intent readable in code.

**Test scenarios:**
- Happy path: clean vault reports no warnings.
- Edge case: vault with 3 capture-error lines from the last week produces the warning line; older errors don't.
- Edge case: presence of `migration-journal.json` produces the resume warning.
- Edge case: a manifest missing `transcript_hash` field is flagged.
- Integration: doctor against the post-migration vault (from Unit 4) reports expected counts.

**Verification:** `bun test tests/doctor.test.ts` green; `cairn doctor` on a clean dev vault prints no warnings.

---

- [ ] **Unit 6: Downstream skills — extract, query, refine**

**Goal:** Update the skill prompts to read manifests as authoritative and to shell out to `cairn summarize` for summary content; surface degraded/failed state visibly.

**Requirements:** R20, R21, R22, R23, R24.

**Dependencies:** Units 1–3 (need `cairn summarize` to exist and produce output). Units 4–5 not strict prerequisites.

**Files:**
- Modify: `skills/extract/SKILL.md`
- Modify: `skills/refine/SKILL.md`
- Modify: `skills/cairn/SKILL.md` (the overview — document the new two-level `sessions/` layout)
- Modify: `commands/query.md` (slash command for `/cairn:query` — add the "which sessions touched X" manifest-scan path described in R24)
- Modify: `commands/extract.md` (slash command — wire the summarize flow into the description)
- Modify: `templates/CAIRN.md` (scaffolded vault documentation — describe manifests + summaries subdirs)
- Test: update `tests/session-summary.test.ts` or add `tests/skill-contract.test.ts` — assert the documented flow is still structurally readable; skill prompts themselves are hard to unit-test, so coverage is shaped as "reference checks" (file reads the right paths) and contract snapshots.

**Approach:** This unit is **editing markdown prompt files**, not writing code. Each file gets explicit text changes that a reviewer can diff. The test coverage is "reference checks" because skill prompts are narrative — we assert the new terms appear in the right places, and a manual integration run in Unit 7 validates behavior end-to-end.

Per-file edit specs (what to add/replace; exact paths to reference):

- `skills/extract/SKILL.md`: Replace the step sequence that reads session files directly. New sequence:
  1. "List manifests in `sessions/` whose frontmatter has `extracted: false`."
  2. "For each manifest: run `cairn summarize --json <manifest-path>` via the Bash tool. Parse the single-line JSON result; note `path`, `cached`, `degraded`."
  3. "If exit code is nonzero, add the session's filename to a `Skipped:` list. Continue with the next session."
  4. "Read the `path` file and proceed with the existing extraction prompt."
  5. "Prefix extraction items from sessions with `degraded: true` with `⚠ Degraded (excerpt-only):`."
  6. "At the end of the output, if `Skipped:` is non-empty, print it under a `## Summary generation failed` heading."
- `skills/refine/SKILL.md`: Identical edit sequence where it references session content.
- `skills/cairn/SKILL.md`: In the "Vault structure" section, replace the single `sessions/` row with three:
  - `sessions/<name>.md` — session manifests (one per session).
  - `sessions/summaries/<name>.md` — cached summaries derived from manifests; regenerable.
  - `sessions/.trash/` — migration quarantine; contents are recoverable but not read by skills.
  Add a note: "`lint` operates only on wiki pages; sessions/ is out of scope."
- `commands/query.md`: Add a new block titled "Query sessions by files touched":
  - "If the user query names a file path, use the Grep tool to search `files_changed` YAML lists inside `sessions/*.md` manifests. Return matching manifests' timestamps and branches. Do not invoke `cairn summarize` for this path — the index answer is sufficient."
- `commands/extract.md`: Add one paragraph noting that `/cairn:extract` now requires `cairn summarize` to be installed on PATH, and that summary generation happens lazily during extraction.
- `templates/CAIRN.md`: In the "Vault structure" table, add rows for `sessions/summaries/` and `sessions/.trash/` with their semantics; update the "Maintained by" column for `sessions/` to "Agent (via Stop hook → `cairn capture-session`)".

**Test scenarios:**
- Reference check: `skills/extract/SKILL.md` references `cairn summarize` and `sessions/summaries/`.
- Reference check: `skills/refine/SKILL.md` references `cairn summarize`.
- Reference check: `skills/cairn/SKILL.md` describes the new sessions/ layout (grep for `summaries`).
- Reference check: `commands/query.md` mentions `files_changed` and manifest scan.
- Reference check: `templates/CAIRN.md` vault-structure table includes `sessions/summaries` and `sessions/.trash`.
- Integration (manual, tracked as verification): run `/cairn:extract` on the post-migration vault; assert a degraded session shows the `⚠ Degraded` prefix; assert a failed summarize shows up in a Skipped section.

**Verification:** Reference-check tests pass; manual `/cairn:extract` run produces the expected surfacing; `/cairn:query "worker.ts"` returns matching manifests by scanning `files_changed` without invoking an LLM.

---

- [ ] **Unit 7: Cleanup, release, docs**

**Goal:** Finalize: migrate the author's own vault (self-hosted dogfood), bump version, sync README / docs, ensure full test matrix green.

**Requirements:** All (integration checkpoint).

**Dependencies:** Units 1–6.

**Files:**
- Modify: `README.md` (if it discusses sessions/)
- Modify: `docs/superpowers/plans/2026-04-15-entire-session-integration.md` or equivalent — mark the prior session-summary approach as superseded (note link, do not delete).
- Modify: `CHANGELOG.md` if present; else a short note in README.
- Verify: `package.json` version matches `src/lib/constants.ts` VERSION.

**Approach:**
- Dogfood: run `cairn migrate-sessions` on the author's vault (`~/cairn`). Confirm 33 → `.trash/`, 10 → `summaries/`. Sanity-check three sampled manifests.
- Run `cairn summarize --all` on the migrated vault. Confirm all 10 pre-migration summaries are preserved (not regenerated). Confirm any new post-refactor captures get summaries via chunking when oversized.
- Run `cairn doctor`. Assert no warnings.
- README / docs: note the split and the new commands. Describe the `cairn migrate-sessions` path for existing users.

**Test scenarios:**
- Test expectation: none — this unit is integration-level dogfood and documentation. All automated tests live in Units 1–6.

**Verification:**
- `bun test` green across the whole suite.
- `bun run lint` (= `bunx tsc --noEmit`) clean.
- Dogfood checklist above passes.
- `git grep -l "claude -p --model haiku" hooks/` returns nothing (hook body cleaned).
- `git grep "Prompt is too long"` finds only tests/migration fixtures, no production code paths.

---

## System-Wide Impact

- **Interaction graph:** Stop hook → `cairn capture-session`; skills → `cairn summarize`; `cairn migrate-sessions` reads/writes `log.md` (coordinated with capture via lock); `doctor` observes both. **`hooks/inject` must be modified** to skip `sessions/summaries/` and `sessions/.trash/` (glob currently non-recursive, so `.trash/` and nested `summaries/` are already out of reach, but if `summaries/` is placed alongside manifests the glob may pick it up — explicit skip is safer). The byte-budget concatenation logic itself is unchanged; the scope narrows to manifests only.
- **Error propagation:** Capture failures are visible via `.cairn/capture-errors.log` and surfaced by `doctor`. Summarize failures surface to the invoking skill, which must show them to the user per R22/R23. Migration partial failures resume via `.cairn/migration-journal.json`.
- **State lifecycle risks:** Two main risks: (a) concurrent Stop events racing on the same `session_id` — mitigated via write-temp-then-rename + existence glob; (b) migration crash mid-apply — mitigated via journal. Both are explicit in Unit 2 and Unit 4.
- **API surface parity:** `cairn init`, `cairn doctor`, `cairn uninstall` remain. New public commands: `cairn capture-session`, `cairn summarize`, `cairn migrate-sessions`. The `capture-session` subcommand is machine-facing (hook-invoked), but should work interactively for debugging.
- **Integration coverage:** Unit 2 and Unit 4 each include integration tests that spawn real subprocesses against tmp vaults; Unit 3 stubs the `claude` binary for deterministic tests. Unit 7's dogfood run catches anything the unit tests miss.
- **Unchanged invariants:** `hooks/inject`'s byte-budget concatenation logic; `cairn init` scaffolding (only adds dirs); `src/lib/entire.ts` purity rule (Cairn never parses Entire internals directly); `lint` skill scope (wiki only); `CAIRN_VAULT` env precedence; `.cairn/state.json` schema.

## Risks & Dependencies

| Risk | Mitigation |
|------|------------|
| Chunked summaries lose coherence vs. single-shot on borderline-sized transcripts | Hierarchical reduction with log(N) termination depth; `REDUCE_PROMPT` instructs contradiction resolution; threshold is configurable; Unit 7 dogfood flags quality issues before release |
| Single turn larger than chunk threshold | In-place truncation with inline marker + `truncated_turns` frontmatter flag; guarantees no prompt overruns the model budget |
| Plugin surface area roughly doubles on a small codebase | Scope is explicitly scoped to Sessions; wiki/ and commands remain small. `logfile.ts`→`lockfile.ts` consolidation reduces module count; deferred `--legacy-errors` flag drops one surface |
| Bun not on PATH inside Stop-hook env | Bash forwarder falls back to `$HOME/.bun/bin/bun`; on total failure writes to `.cairn/capture-errors.log` directly and exits 0; `cairn doctor` check catches this proactively |
| `yaml` npm dep introduces supply-chain surface | Pin exact version; it's the most-used YAML parser in the ecosystem, widely audited |
| Hook rewrite breaks on Windows | `hooks/run-hook.cmd` polyglot already handles cross-platform dispatch; new `hooks/session-summary` body is 3–5 shell lines, posix-only assumptions ok |
| Migration loses user content | Dry-run default, `.trash/` not hard-delete, journal resume with sessions-listing-hash guard, migration lock (single writer), characterization-first fixture tests (Unit 4), manual dogfood against a backup (Unit 7) |
| `--force` destroys user-edited summaries | `--force` trashes to `sessions/.trash/summaries/<name>-<timestamp>.md` by default; only `--force --destructive` overwrites; `cairn summaries pin` is the public mechanism to declare user intent |
| Cache invalidation churn on Claude Code auto-compact | Cache key is **manifest_hash** (computed from immutable manifest fields excluding `excerpt`), not transcript_hash. Manifest is fixed at capture time, so auto-compaction of the transcript does not invalidate the cache |
| Existing `skills/extract` users on old vault pre-migrate and get confused output | Doctor warns about legacy files post-upgrade; migrate command is idempotent; dry-run default prevents accidental mutation |
| Capture idempotency race (same session_id) | O_EXCL lockfile on full `session_id` — atomic create-or-fail, no TOCTOU window |
| Concurrent migration invocations | Migration acquires `withMigrationLock`; second invocation exits with a clear message pointing at the first's PID |
| Cross-volume `sessions/.trash/` moves fail with EXDEV | All moves fall back to `fs.cpSync + fs.unlinkSync` on EXDEV |
| `extract` subprocess-per-session latency regresses for large vaults | Acceptable for v1 (Bun cold start ~80 ms; 100 sessions ≈ 8 s overhead against minutes of LLM time); `--batch` mode deferrable |
| SessionStart inject quality regresses (manifest excerpt vs. full summary) | Unit 6 checks whether `hooks/inject` should prefer cached summary when present, falling back to manifest excerpt; Unit 7 dogfood specifically measures first-session context quality |

## Documentation / Operational Notes

- **CHANGELOG / release notes**: call out the vault layout change and the required one-time `cairn migrate-sessions --apply` for existing users.
- **Upgrade path for existing users**: `cairn doctor` warns if unmigrated files are detected; the warning names the exact command to run. Doctor also checks that `bun` is resolvable from a minimal shell PATH (catches the silent-capture failure mode where Stop hook cannot find Bun).
- **Rollback**: the migration journal is not a true rollback mechanism. If a user wants to revert, they can restore `sessions/.trash/` contents. Document this in the release notes.
- **Observability**: `.cairn/capture-errors.log` is the canonical surface; advise users to report issues by sharing that file's contents.

## Sources & References

- **Origin document**: `docs/brainstorms/2026-04-19-session-capture-manifest-requirements.md`
- **Current hook**: `hooks/session-summary` (to be rewritten)
- **CLI entry**: `src/cli.ts`
- **Existing subcommand examples**: `src/commands/doctor.ts`, `src/commands/init.ts`, `src/commands/uninstall.ts`
- **Entire gateway**: `src/lib/entire.ts`
- **Vault path resolver**: `src/lib/vault.ts`
- **Constants**: `src/lib/constants.ts`
- **Hook config**: `hooks/hooks.json`, `hooks/run-hook.cmd`
- **Template**: `templates/CAIRN.md`
- **Related prior plan**: `docs/plans/2026-04-17-002-feat-session-aggregation-plan.md` (adjacent work on session aggregation)

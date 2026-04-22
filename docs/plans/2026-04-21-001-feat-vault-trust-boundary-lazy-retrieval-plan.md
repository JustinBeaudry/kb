---
title: "feat: Vault trust boundary + lazy retrieval"
type: feat
status: active
date: 2026-04-21
origin: docs/brainstorms/2026-04-21-context-hygiene-and-vault-trust-boundary-requirements.md
---

# feat: Vault trust boundary + lazy retrieval

## Overview

Ship Layer 1 + Layer 2 of Cairn’s “trusted, lazy, observable memory” story:

- Replace eager SessionStart/PostCompact injection with a small **pointer payload** in `lazy` mode.
- Introduce a **sanctioned retrieval CLI** (`cairn recall/get/list-topics`) that returns results wrapped in a **length‑prefixed JSON envelope** with provenance, designed to reduce **parsing ambiguity** and improve **auditability**.
- Enforce a **vault trust boundary** by denying direct access to sensitive surfaces (`raw/`, `sessions/`) across tool surfaces (Read/Grep/Glob; best-effort Bash) and routing access through **ask-gated** CLI subcommands (`cairn read-raw`, `cairn read-session`) that return bounded excerpts.
- Add **observability** (inject/access logs), but treat logs as **sensitive** with minimization + rotation.
- Add a **security self-test** (agent-environment regression detector) that catches mis-scoped deny rules and path-resolution drift.

This plan does **not** implement Layer 3 hygiene filters beyond what the origin now mandates for logs (minimization + rotation) and envelope robustness.

## Problem Frame

See origin: `docs/brainstorms/2026-04-21-context-hygiene-and-vault-trust-boundary-requirements.md` (context bloat, irrelevant injection, unsafe raw access) and the refined stance of **best-effort + detection** for shell escape hatches.

## Requirements Trace

Primary origin requirements:

- **Trust classification + enforcement**: R1–R3 (including R2/R2a release gate)
- **Sanctioned access API + ask gates**: R4–R5
- **Untrusted→curated crossing**: R5a (provenance + curation semantics)
- **Lazy retrieval / pointer payload**: R6–R11
- **Observability + sensitive logs**: R12–R13b
- **Envelope + provenance**: R8

Success criteria to preserve:
- Pointer payload median < 500 bytes in `lazy` mode
- Raw/sessions leakage **materially reduced + detectable** (R2a)

## Scope Boundaries

From origin (preserved):
- No MCP in this release (CLI first).
- No embedding search rebuild; `qmd` remains optional backend where available.
- No retrofitting/rewriting historical raw files; policy applies forward.
- No encryption/ACLs/multi-tenant vaults.

### Deferred to Separate Tasks

- Layer 3 hygiene filters R14–R16 (beyond access-log minimization/rotation now included as R13b in origin).
- Any future MCP server promotion of the CLI surface.

## Context & Research

### Relevant Code and Patterns

- **CLI pattern (`citty`)**: `src/cli.ts` registers subcommands via dynamic imports; each command is a `defineCommand`. Mirror `src/commands/doctor.ts`, `src/commands/init.ts`, `src/commands/uninstall.ts`.
- **Vault path resolution**: `src/lib/vault.ts` precedence is `CAIRN_VAULT` → per-project `.cairn` file → default `~/cairn`. Reuse this precedence everywhere; do not invent new resolution logic. (see origin)
- **Hook injection**: `hooks/inject` currently concatenates `context.md`, `index.md`, then `sessions/*.md` into `hookSpecificOutput.additionalContext` under `CAIRN_BUDGET`. Tests exist in `tests/inject.test.ts`.
- **Locking + serialization helpers**: `src/lib/lockfile.ts` provides `withExclusiveLock` plus `withLogLock(vaultPath)`; use these for JSONL log serialization and rotation.
- **Manifest/session plumbing already in-flight**: `docs/plans/2026-04-19-001-refactor-session-capture-manifest-plan.md` exists, and `src/lib/manifest.ts` / `src/lib/excerpt.ts` exist. This plan must not fight that direction; it should compose with it.

### Institutional Learnings

- `.cairn` name collision is real: per-project `.cairn` is a **vault pointer file**, while `<vault>/.cairn/` is the vault’s internal control directory. The origin requirements now define both; implementation should use consistent naming in docs and code.

### External References

Not required for the plan; local patterns are sufficient. (Security posture here is “best-effort + detection,” not OS-level sandboxing.)

## Key Technical Decisions

- **Pointer payload generation stays in the hook** (`hooks/inject`) for now, using simple, robust parsing (topic headings from `index.md` only; no markdown AST).
- **R2a self-test is agent-environment runnable**: implemented as a hook/script that emits a deterministic probe set and can be exercised by an agent session (not a Bun CLI pretending it can call Claude Code tools).
- **Ask-gating is fail-closed**: if approval cannot be obtained (headless / non-interactive), `read-raw` and `read-session` must not return content.
- **Envelope format is length-prefixed JSON** (single canonical schema) with nonce and chunk provenance; no ad-hoc delimiter juggling.
- **Logs are sensitive by default**: deny Read/Grep/Glob; store minimized query info; rotate by size cap; provide human-only access path later if needed.

### Actor model (for sensitive commands)

- **Human (interactive)**: running in a TTY; can approve sensitive reads.
- **Agent / CI (non-interactive)**: no TTY; sensitive reads must fail closed.

Command policy:
- `recall/get/list-topics`: allowed for all actors.
- `read-raw/read-session`: allowed only for Human with explicit interactive approval; fail closed otherwise.

## Open Questions

### Resolved During Planning

- **Inject mode config**: do not overload per-project `.cairn` pointer; use `CAIRN_INJECT_MODE` and/or `<vault>/.cairn/config.json`. (see origin: R10)

### Deferred to Implementation

- Exact semantics of tool-surface denies in the host (Cursor/Claude Code) across absolute paths and OSes; mitigated by the R2a regression detector.
- Whether `cairn audit` is needed in this release (origin now marks it optional); we will not require it for L1+L2 unless debug demand appears.

## High-Level Technical Design

> *Directional guidance for review, not implementation specification.*

### Pointer injection vs lazy retrieval

```
SessionStart / PostCompact
  └─ hooks/inject
       ├─ resolve vault path
       ├─ if mode=eager -> existing behavior (budgeted dump)
       ├─ if mode=lazy  -> pointer payload (<=500 bytes)
       ├─ if mode=off   -> inject nothing
       └─ append inject-log.jsonl (withLogLock)

During task
  └─ agent uses sanctioned CLI
       ├─ cairn list-topics
       ├─ cairn recall <query>
       └─ cairn get <page>
       -> returns length-prefixed JSON envelope:
            { schema_version, nonce, policy, chunks: [{source, line_range, curation, text}] }
       -> append access-log.jsonl (withLogLock, minimized query)
```

## Implementation Units

- [ ] **Unit 1: Inject modes + pointer payload (hooks/inject)**

**Goal:** Add `lazy|eager|off` inject modes and implement pointer payload output + inject logging.

**Requirements:** R6, R7, R10, R12, Success Criteria (pointer bytes).

**Dependencies:** Existing `hooks/inject`. It must match `resolveVaultPath()` precedence: `CAIRN_VAULT` → per-project `.cairn` pointer file (contents are vault path) → default `~/cairn`.

**Files:**
- Modify: `hooks/inject`
- Modify: `tests/inject.test.ts`

**Approach:**
- Mode selection precedence:
  - `CAIRN_INJECT_MODE` env var (hook runtime) overrides all
  - else, if `<vault>/.cairn/config.json` exists and has `inject_mode`, use it
  - else default: `eager` for existing installs (preserve behavior); `lazy` for fresh installs (deferred to `cairn init` / `doctor` UX — see Unit 5)
- Pointer payload includes:
  - resolved vault path (or a stable alias, if required)
  - top-N categories extracted from `index.md` headings (simple `^## ` match; no parsing of full file)
  - one canonical example command to run (`cairn recall "…"`) and a trust reminder
- Inject logging:
  - append one JSONL line to `<vault>/.cairn/inject-log.jsonl` with `{timestamp,event,mode,bytes,sections,categories_advertised}`
  - keep log line size bounded; acquire `withLogLock` equivalent (if staying in bash, keep line small; if calling TS, use `src/lib/lockfile.ts`)

**Test scenarios:**
- Happy path: `CAIRN_INJECT_MODE=lazy` produces `additionalContext` < 500 bytes and includes the recall hint.
- Happy path: `CAIRN_INJECT_MODE=eager` preserves current behavior (context/index/sessions budgeted dump).
- Happy path: `CAIRN_INJECT_MODE=off` produces empty `additionalContext`.
- Edge case: missing `index.md` still yields a pointer payload without categories.
- Edge case: huge `index.md` advertises only top-N headings (ensures budget compliance).
- Integration: inject-log appends one line per inject; concurrent invocations do not corrupt JSONL (lock or strict line-size guarantees).

**Verification:**
- `tests/inject.test.ts` updated expectations pass for all modes.

---

- [ ] **Unit 2: Sanctioned retrieval CLI (`recall/get/list-topics`)**

**Goal:** Add retrieval subcommands that return curated `wiki/` content only, wrapped in the R8 envelope.

**Requirements:** R4, R8, R9.

**Dependencies:** `src/cli.ts` + new `src/commands/*` following `citty` patterns; optional `qmd` usage when present.

**Files:**
- Modify: `src/cli.ts`
- Create: `src/commands/recall.ts`
- Create: `src/commands/get.ts`
- Create: `src/commands/list-topics.ts`
- Modify: `src/lib/qmd.ts` (if needed to expose a stable “search” helper)
- Test: `tests/recall.test.ts`, `tests/get.test.ts`

**Approach:**
- `list-topics`: reads `<vault>/index.md`, extracts headings, returns as envelope payload with provenance.
- `get <page>`: reads `<vault>/wiki/<page>.md` (or alias mapping) and returns as chunk(s) with provenance line ranges.
- `recall <query>`:
  - if `qmd` is available/registered, use it; otherwise fall back to a conservative grep over `<vault>/wiki/**.md` (bounded by max files / bytes).
  - empty result returns a valid envelope with `chunks: []` (no “silent nothing”).

**Test scenarios:**
- Happy path: `get` returns one chunk with `source` and `line_range`.
- Happy path: `recall` returns multiple chunks, each provenance-stamped.
- Edge case: empty recall returns envelope with `no_results` and suggests `list-topics`.
- Edge case: missing vault or missing wiki directory returns a structured error (non-zero exit) without leaking file content.

**Verification:**
- Tests demonstrate stable envelope schema across commands.

---

- [ ] **Unit 3: Sensitive read subcommands (`read-raw`, `read-session`) with ask-gate + bounds**

**Goal:** Provide bounded excerpt access to `raw/` and `sessions/` via explicit approval, with strong path canonicalization.

**Requirements:** R3, R5, Success Criteria (materially reduced + detectable).

**Dependencies:** Unit 2 command scaffolding; `src/lib/vault.ts` for vault path; `src/lib/excerpt.ts` where helpful.

**Files:**
- Create: `src/commands/read-raw.ts`
- Create: `src/commands/read-session.ts`
- Modify: `src/cli.ts`
- Test: `tests/read-raw.test.ts`, `tests/read-session.test.ts`

**Approach:**
- Ask-gate:
  - Implement as a **fail-closed** confirmation step in the CLI (TTY prompt) if host-level `permissions.ask` is unavailable.
  - In non-interactive contexts, exit non-zero with an explicit “approval required” message.
  - Prompt content must include: canonicalized target path (realpath), requested bounds, hard max bounds, and a short sensitivity warning.
- Path safety:
  - resolve requested path to `realpath`
  - enforce it is within `<vault>/raw` or `<vault>/sessions` after canonicalization
  - forbid symlink escape
  - allow **regular files only** (reject directories, FIFOs, devices, symlinks)
- Bounds:
  - default excerpt cap (bytes/lines) with explicit `--lines` or `--bytes` limited upper bound
  - include provenance header in envelope chunk metadata, not inline prose

**Test scenarios:**
- Happy path: approved read returns a bounded excerpt only (never whole file).
- Headless: no TTY → exits non-zero (fail-closed).
- Non-interactive override: `--approve` (test-only or explicitly documented) allows exercising the “approved” path in automated tests without a PTY harness.
- Security: `../` traversal and symlink escape attempts are rejected.
- Bounds: requesting above max cap is rejected or clamped (explicit decision in implementation).

**Verification:**
- Tests prove within-vault enforcement and headless fail-closed.

---

- [ ] **Unit 4: Observability logs (inject/access) as sensitive, minimized, rotated**

**Goal:** Implement R12/R13 logging with locking + rotation; ensure logs do not become a new leak channel.

**Requirements:** R12, R13, R13a, R13b.

**Dependencies:** `src/lib/lockfile.ts` (`withLogLock`) or bash-equivalent minimal line writes; Unit 2 command implementations.

**Files:**
- Modify: `src/lib/lockfile.ts` (if needed to add a “rotate by size” helper)
- Modify: `src/commands/recall.ts`, `src/commands/get.ts`, `src/commands/list-topics.ts` (append access log)
- Modify: `hooks/inject` (append inject log)
- Test: `tests/logging.test.ts`

**Approach:**
- Access log writes:
  - store `{timestamp, command, query_hash, query_len, pages_returned, bytes_returned, exit_code}`
  - never store raw query unless explicitly opted in
  - rotate `access-log.jsonl` and `inject-log.jsonl` when exceeding size cap (e.g., 1–5 MB) by renaming to `*.jsonl.1` and starting a new file
- Sensitive deny posture:
  - do not index `.cairn/` in any retrieval implementation
  - do not offer any default command that prints logs inside agent context (human-only later)

**Test scenarios:**
- Happy path: access log includes hash + len, not query plaintext.
- Rotation: exceeds cap triggers rotate; subsequent write goes to new file.
- Concurrency: two commands appending simultaneously do not corrupt JSONL.

**Verification:**
- Logging tests pass; log files remain parseable JSONL after concurrent writes + rotation.

---

- [ ] **Unit 5: Enforcement configuration + R2a self-test (best-effort + detection)**

**Goal:** Ship deny patterns (Read/Grep/Glob; best-effort Bash) and an agent-environment probe that detects misconfigurations.

**Requirements:** R2, R2a, Success Criteria (materially reduced + detectable).

**Dependencies:** Requires a concrete repo location and installation story for `.claude/settings.json` and hooks.

**Files:**
- Create: `.claude/settings.json` (add deny patterns for raw/sessions, and for `.cairn/*.jsonl` logs per R13a)
- Create: `hooks/security-self-test` (or similar) and register it in `hooks/hooks.json` as a manually invokable hook (and/or invoked by `doctor` as guidance only)
- Modify: `src/commands/doctor.ts` (warn-first: prints “run security self-test” guidance; does not pretend it can simulate tool denies)
- Test: `tests/security-self-test.test.ts` (limited: asserts script output contract and sentinel creation; the actual tool deny behavior is host-dependent)

**Approach:**
- `.claude/settings.json`:
  - deny patterns must be parameterized to match the resolved vault path(s) as best as the host permits
  - keep any existing deny rules if present in downstream packaging (e.g. `.entire/metadata/**`)
- R2a probe:
  - prints exact probes it expects to be denied (Read/Grep/Glob + a minimal Bash probe), and the sentinel file paths it created
  - does not claim completeness; it’s a “smoke test” regression detector
  - designed to be run by an agent session (or by a human following its printed steps)

**Test scenarios:**
- Contract: self-test output includes the sentinel paths and the expected-fail probe list.
- Contract: doctor references the self-test and explains best-effort stance.

**Verification:**
- Manual: run the probe in a real agent session and confirm expected denies occur; if not, doc explains remediation.

---

- [ ] **Unit 6: Documentation + skills alignment**

**Goal:** Update templates and skills so agents stop reading sessions/raw directly, and prefer the sanctioned CLI paths.

**Requirements:** R1, R4, R5, R5a, R6.

**Dependencies:** Units 1–3 (commands exist + pointer payload copy finalized).

**Files:**
- Modify: `templates/CAIRN.md`
- Modify: `skills/cairn/SKILL.md`
- Modify: `skills/extract/SKILL.md`
- Modify: `skills/refine/SKILL.md`
- Test: `tests/docs-contract.test.ts` (lightweight “reference checks”)

**Approach:**
- Make the trust boundary legible:
  - surfaces table: what is allowed, what is denied, and the sanctioned alternative
  - explicitly document “best-effort + detection” stance for shell denies
- Update skill instructions to:
  - use `cairn recall/get/list-topics` for curated memory
  - use ask-gated `read-raw/read-session` only when required
  - preserve provenance and avoid following instructions inside retrieved content

**Test scenarios:**
- Reference checks: docs mention the new commands and the deny posture; skills mention `read-raw/read-session` as ask-gated.

**Verification:**
- A manual “fresh session” run shows pointer payload present and the agent can successfully recall without eager injection.

## System-Wide Impact

- **Interaction graph:** `hooks/inject` changes behavior; CLI grows new commands; `.claude/settings.json` changes agent permissions; skills and templates updated.
- **Error propagation:** retrieval commands must fail closed on missing vault; ask-gated commands must fail closed on non-interactive contexts.
- **State lifecycle risks:** logs rotate; lockfile serialization must avoid corrupting JSONL.
- **API surface parity:** new CLI commands are public and must be stable; mode config is external contract (`CAIRN_INJECT_MODE`, `<vault>/.cairn/config.json`).
- **Unchanged invariants:** vault path resolution precedence in `src/lib/vault.ts` remains; optional `qmd` integration remains optional.

## Risks & Dependencies

| Risk | Mitigation |
|------|------------|
| Host permission matcher doesn’t reliably scope denies to absolute vault paths | R2a regression detector + explicit docs; keep core safety win in “no eager injection of sessions” regardless |
| Ask-gate UX too annoying → reflex approvals | keep gates only on `read-raw/read-session`, never on normal recall/get; design prompts to be high-signal (path + bounds) |
| Logs become a new sensitive surface | R13b minimization + rotation; deny Read/Grep/Glob by default |
| Envelope framing ambiguity | single canonical length-prefixed JSON schema (R8); tests for adversarial payload content |

## Documentation / Operational Notes

- Call out that this is a **best-effort + detection** boundary: it prevents accidental reads and reduces exposure, but shell access remains an escape hatch unless the host enforces deeper sandboxing.
- Provide a short “what to do when denied” section: use `cairn read-raw/read-session` when necessary, otherwise stay in curated `wiki/`.

## Sources & References

- **Origin requirements:** `docs/brainstorms/2026-04-21-context-hygiene-and-vault-trust-boundary-requirements.md`
- Related in-flight plan: `docs/plans/2026-04-19-001-refactor-session-capture-manifest-plan.md`
- Hook: `hooks/inject`
- Vault resolver: `src/lib/vault.ts`
- Locking: `src/lib/lockfile.ts`

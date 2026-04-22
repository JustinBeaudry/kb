---
title: "fix: Remove /init slash command shadowing Claude Code built-in"
type: fix
status: active
date: 2026-04-17
---

# fix: Remove /init slash command shadowing Claude Code built-in

## Overview

Delete the plugin-provided `/init` slash command so it no longer shadows Claude Code's built-in `/init` (which creates `CLAUDE.md`). The CLI entry point `bunx cairn init` — which is what the README already instructs users to run — is unaffected.

## Problem Frame

The Cairn plugin ships `commands/init.md`, which Claude Code registers as the `/init` slash command. Claude Code also ships a built-in `/init` command that scaffolds `CLAUDE.md` for a repo. The plugin command takes precedence, so users trying to create a `CLAUDE.md` hit the Cairn installer hint instead. The plugin command adds no value over the existing CLI invocation documented in `README.md`, so the fix is to delete it.

## Requirements Trace

- R1. `/init` in Claude Code resolves to the built-in CLAUDE.md bootstrap, not the Cairn plugin command.
- R2. `bunx cairn init` remains the supported entry point for vault setup and is unchanged.
- R3. No dangling references to the removed slash command in docs, tests, or other commands/skills.

## Scope Boundaries

- Not renaming the command to `/cairn:init` or similar — remove only.
- Not touching `src/commands/init.ts` (CLI installer) or its tests.
- Not modifying hooks, skills, or other slash commands.
- No changes to `bunx cairn init` behavior, flags, or output.

## Context & Research

### Relevant Code and Patterns

- `commands/init.md` — the slash command definition to delete. Body is a 17-line hint that re-instructs `bunx cairn init`.
- `commands/*.md` — sibling slash commands (`ingest`, `query`, `lint`, `refine`, `extract`). None reference `/init`.
- `src/commands/init.ts` + `src/cli.ts:11` — CLI installer; unrelated to the slash command and stays.
- `README.md:16, 25` — already documents `bunx cairn init` as the install path; no doc update required.
- `skills/cairn/SKILL.md` — no references to `/init`.

### Institutional Learnings

- None relevant.

### External References

- None needed.

## Key Technical Decisions

- **Delete, don't rename.** The user's intent is to stop shadowing the built-in. A rename (e.g., `/cairn:init`) would also work but adds a near-duplicate of `bunx cairn init` with no new capability. Remove cleanly.
- **Leave the CLI untouched.** `bunx cairn init` is the real installer and is already the documented path.

## Open Questions

### Resolved During Planning

- Does anything depend on `commands/init.md`? No — grep across repo (excluding `node_modules`) shows zero references to the slash-command file from other commands, skills, hooks, tests, or docs.

### Deferred to Implementation

- None.

## Implementation Units

- [ ] **Unit 1: Delete the slash command file**

**Goal:** Remove `commands/init.md` so Claude Code's built-in `/init` is no longer shadowed.

**Requirements:** R1, R2

**Dependencies:** None

**Files:**
- Delete: `commands/init.md`

**Approach:**
- Remove the file with `git rm commands/init.md`. No code, no other commands, and no docs import or reference it, so no follow-on edits are required in this unit.

**Patterns to follow:**
- N/A (deletion).

**Test scenarios:**
- Test expectation: none — pure file deletion with no behavioral logic to cover. Verification in Unit 2 confirms the absence of lingering references.

**Verification:**
- `commands/init.md` no longer exists on disk.
- `bunx cairn init` still runs end-to-end (smoke check — no code changed, but confirm no accidental coupling).

- [ ] **Unit 2: Verify no lingering references**

**Goal:** Confirm nothing in the repo still points at the removed slash command.

**Requirements:** R3

**Dependencies:** Unit 1

**Files:**
- Inspect: repo-wide (no edits expected).

**Approach:**
- Repo-wide grep for `commands/init`, `/init` (plugin-style references only — the built-in is fine), and `init.md` (excluding `node_modules` and `src/commands/init.ts`, which is the CLI and legitimately matches).
- If any stale mention surfaces in `README.md`, `docs/`, `skills/`, `hooks/`, or `tests/`, remove or rewrite it in this unit.

**Patterns to follow:**
- N/A.

**Test scenarios:**
- Test expectation: none — static verification via grep, no behavioral change.

**Verification:**
- Grep returns only the expected CLI hits (`src/cli.ts`, `src/commands/init.ts`, `tests/init.test.ts`, `README.md` lines that describe `bunx cairn init`).
- `bun run lint` (`bunx tsc --noEmit`) passes.
- `bun test` passes.

## System-Wide Impact

- **Interaction graph:** Removing a plugin-level slash command changes only what Claude Code registers. No runtime code paths in `src/` are affected.
- **Error propagation:** None — the file isn't imported anywhere.
- **State lifecycle risks:** None. Existing vaults and hooks are untouched.
- **API surface parity:** The CLI `cairn init` subcommand remains the canonical install path; no parity work needed.
- **Integration coverage:** `tests/init.test.ts` covers the CLI installer and continues to apply. No new integration scenario required.
- **Unchanged invariants:** `bunx cairn init`, hook registration behavior, vault scaffolding, and all other slash commands (`/cairn:ingest`, `/cairn:query`, `/cairn:lint`, `/cairn:refine`, `/cairn:extract`) are explicitly unchanged.

## Risks & Dependencies

| Risk | Mitigation |
|------|------------|
| User muscle memory expects `/init` to run the Cairn installer. | Low — README already documents `bunx cairn init`. Call it out in the commit/PR body so upgraders know. |
| A downstream consumer relies on the slash command existing. | Grep confirms no internal references; external users would only see behavior change as `/init` reverting to Claude Code's built-in, which is the desired outcome. |

## Documentation / Operational Notes

- No README change required — install instructions already point at `bunx cairn init`.
- Commit/PR message should mention the shadowing conflict so the rationale is preserved in history.
- Consider a minor version bump (`0.5.0` → `0.6.0` or `0.5.1`) in `package.json` and `.claude-plugin/plugin.json` on release, since removing a user-visible command is a behavior change. Version bump itself is out of scope for this plan unless the user wants it bundled.

## Sources & References

- Slash command file: `commands/init.md`
- CLI entry point (unchanged): `src/commands/init.ts`, `src/cli.ts:11`
- Install docs (already correct): `README.md:16`, `README.md:25`
- Plugin manifest: `.claude-plugin/plugin.json`

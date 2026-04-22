---
name: extract
description: >
  Extract wiki-worthy knowledge from unprocessed session manifests.
  Sessions are sources — extraction runs the normal ingest workflow.
argument-hint: "[on|off]"
---

# Cairn — Extract from Sessions

Sessions are sources. This skill triggers lazy summarization for unprocessed
session manifests, then runs the ingest workflow on confirmed knowledge with
user confirmation.

## Finding Your Vault

Check these in order:
1. `CAIRN_VAULT` environment variable
2. `~/cairn` (default location)

## Arguments

- `/cairn:extract` — extract from unprocessed sessions now
- `/cairn:extract on` — enable session start nudge (reminds you when unprocessed sessions exist)
- `/cairn:extract off` — disable session start nudge

## Toggle Behavior

When the user runs `/cairn:extract on` or `/cairn:extract off`:

1. Read `<vault>/.cairn/state.json`
2. Set `"autoExtractNudge": true` or `false`
3. Write the file back
4. Confirm: "Extract nudge enabled/disabled."

When `autoExtractNudge` is `true`, the agent should mention unprocessed sessions
at session start: "You have N unprocessed sessions. Run `/cairn:extract` to review."

## Extraction Workflow

When the user runs `/cairn:extract` (no arguments):

1. Read `<vault>/.cairn/state.json` to find the vault path.
2. List manifest files in `<vault>/sessions/` whose frontmatter has `extracted: false`.
   Ignore `sessions/summaries/` and `sessions/.trash/`.
3. For each unprocessed manifest:
   a. Run `cairn summarize --json <manifest-path>` via the Bash tool.
   b. Parse the single-line JSON result and note `path`, `cached`, and `degraded`.
   c. If the command exits nonzero, add the manifest filename to a `Skipped:` list and continue.
   d. Read the summary file at `path`.
   e. Read the `## Extraction Candidates` section.
   f. If no candidates, mark the manifest `extracted: true` and skip.
   g. **Present candidates to the user**: "Session YYYY-MM-DD had N candidates: ..."
      Prefix candidates from `degraded: true` summaries with `Degraded (excerpt-only):`.
   h. User confirms which candidates to file.
5. For each confirmed candidate, run the ingest cascade from CAIRN.md (steps 3–9 of the Ingest workflow; skip step 2's `raw/` copy when the candidate is Entire-sourced — provenance lives in the checkpoint branch, see below):
   - Create or update wiki pages using the correct page template.
   - Cascade updates to related existing pages.
   - Update backlinks on target pages.
   - Every page links to 2+ others.
   - Update `context.md` if relevant to current focus.
   - Add entries to `index.md` under appropriate categories.
6. Set `extracted: true` in the manifest frontmatter.
7. Append to `<vault>/log.md`:

```
## [YYYY-MM-DD] ingest | session extraction

Extracted from sessions: YYYY-MM-DDTHH-MM-SS. Created [[Page A]], [[Page B]]. Updated [[Page C]].
```
8. If `Skipped:` is non-empty, print it under:

```
## Summary generation failed
- <manifest filename>
```

## Entire Checkpoint Provenance

When a session manifest has `entire_checkpoint` in its frontmatter, the session
was captured with Entire context and the full checkpoint is available via `entire explain`.

During extraction:

1. Note the checkpoint ID from the session's `entire_checkpoint` field.
2. When creating wiki pages from this session, set the `source` field to:
   ```yaml
   source: "entire://<checkpoint-id>"
   ```
3. This enables future re-extraction: `entire explain --checkpoint <id>` retrieves
   the full session context, not just the lossy summary.
4. Do NOT copy Entire session data to `raw/` — the checkpoint branch is the
   provenance store. `raw/` is for non-Entire sources only.
5. If you need more context than the summary provides, run:
   ```bash
   entire explain --checkpoint <id> --no-pager
   ```
   to get the detailed view with scoped prompts and file changes.

## Key Rules

1. Sessions are sources — treat extraction like any other ingest.
2. Discuss before filing — always present candidates and get confirmation.
3. Use the correct page template for each candidate's type.
4. Mark manifests `extracted: true` after processing, even if no candidates were filed.
5. Read `CAIRN.md` before your first vault operation if you haven't already this session.

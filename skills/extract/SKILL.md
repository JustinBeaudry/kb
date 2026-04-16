---
name: extract
description: >
  Extract wiki-worthy knowledge from unprocessed session summaries.
  Sessions are sources — extraction runs the normal ingest workflow.
argument-hint: "[on|off]"
---

# Cairn — Extract from Sessions

Sessions are sources. This skill triggers the ingest workflow on unprocessed
session summaries, extracting wiki-worthy knowledge with user confirmation.

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
2. List all files in `<vault>/sessions/`.
3. Read each session file. Filter to those with `extracted: false` in frontmatter.
4. For each unprocessed session:
   a. Read the `## Extraction Candidates` section (written by the Stop hook).
   b. If no candidates, mark `extracted: true` and skip.
   c. **Present candidates to the user**: "Session YYYY-MM-DD had N candidates: ..."
   d. User confirms which candidates to file.
5. For each confirmed candidate, run the standard ingest workflow from CAIRN.md:
   - Create or update wiki pages using the correct page template.
   - Cascade updates to related existing pages.
   - Update backlinks on target pages.
   - Every page links to 2+ others.
   - Update `context.md` if relevant to current focus.
   - Add entries to `index.md` under appropriate categories.
6. Set `extracted: true` in the session's frontmatter.
7. Append to `<vault>/log.md`:

```
## [YYYY-MM-DD] ingest | session extraction

Extracted from sessions: YYYY-MM-DDTHH-MM-SS. Created [[Page A]], [[Page B]]. Updated [[Page C]].
```

## Entire Checkpoint Provenance

When a session summary has `entire_checkpoint` in its frontmatter, the session
was captured by Entire and the full transcript is available via `entire explain`.

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
4. Mark sessions `extracted: true` after processing, even if no candidates were filed.
5. Read `CAIRN.md` before your first vault operation if you haven't already this session.

---
name: refine
description: >
  Systematic vault refinement — find stale pages, weak connections,
  merge/split candidates, and backlink gaps. Interactive, user-approved.
---

# KB — Refine Vault

The fourth core vault operation alongside ingest, query, and lint. Refine
actively compounds synthesis by improving vault structure and connections.

## Finding Your Vault

Check these in order:
1. `KB_VAULT` environment variable
2. `~/kb` (default location)

## Refinement Workflow

When the user runs `/kb:refine`:

1. Read `KB.md` for vault conventions if you haven't already this session.
2. Run the vault health dashboard to establish a baseline:

```
## Vault Health (Baseline)
- Pages: N | Links: N | Avg links/page: N.N
- Orphans: N | Dead links: N | Stale (>30d): N
- Types: concept(N) entity(N) source-summary(N) comparison(N) overview(N)
- Backlinks coverage: N/N pages (N%)
```

3. **Stale pages** (updated > 30 days ago):
   - Read each stale page. Check if content is still accurate.
   - Present to user: "[[page-name|Page Name]] last updated YYYY-MM-DD. Still accurate? [update/archive/leave]"
   - For updates: refresh content, set `updated` to today.
   - For archives: move to a `wiki/archive/` subdirectory (don't delete — provenance).

4. **Under-connected pages** (< 3 inbound links):
   - Check `## Backlinks` sections to count inbound links.
   - Check your tool list for `mcp__qmd__qmd_search` (or `qmd_search`). If
     present, use it to find related pages by keyword overlap. Otherwise, use
     `kb recall <keyword>` to find related pages in `wiki/`, or `kb list-topics`
     to scan category neighbors.
   - Suggest new connections: "[[page-a|Page A]] has 1 inbound link. Related to [[page-b|Page B]] and [[page-c|Page C]]?"
   - If approved, add wikilinks and update backlinks on both sides.

5. **Merge candidates** (overlapping topics):
   - Find pages with similar tags or significant wikilink overlap.
   - Present both pages side-by-side with a proposed merged structure.
   - If approved: create merged page, redirect old pages (update all inbound links), remove originals from index.

6. **Split candidates** (overly broad pages):
   - Find pages with 4+ H2 sections covering unrelated topics.
   - Propose split into focused pages with correct page types.
   - If approved: create new pages, update all inbound/outbound links, update index.

7. **Backlinks audit**:
   - Scan all wikilinks in the vault.
   - For each link target, verify the target's `## Backlinks` section lists the source.
   - Fix any gaps silently (no user approval needed for backlink sync).

8. **Research gap surfacing**:
   - Pull implicit concepts and thin topics from the Lint findings (Lint steps 9–10), re-verifying thin topics against the post-pass backlink counts from step 4.
   - Collect open questions implied by `Gaps` sections on overview pages (heading level may vary).
   - Present to user as "Investigations worth pursuing": specific questions to ask, terms to research, external sources to ingest. Never auto-create pages from gaps — they are prompts for the next ingest, not filings.

9. **Session-derived context, if needed**:
   - Treat `sessions/*.md` files as manifests, not summaries.
   - When a refinement question needs session content, run `kb summarize --json <manifest-path>`, then retrieve the cached summary with `kb read-session summaries/<name>.md --approve` (direct Reads under `sessions/` are deny-ruled; rule 7 applies).
   - If summary generation fails, skip that manifest and list it under `Skipped session summaries`.
   - If the JSON result has `degraded: true`, label any finding from that summary as `Degraded (excerpt-only)`.

10. Run the vault health dashboard again to show improvement:

```
## Vault Health (After Refinement)
- Pages: N | Links: N | Avg links/page: N.N
- Orphans: N | Dead links: N | Stale (>30d): N
- Types: concept(N) entity(N) source-summary(N) comparison(N) overview(N)
- Backlinks coverage: N/N pages (N%)
```

11. Append to `<vault>/log.md`:

```
## [YYYY-MM-DD] refine | vault refinement pass

Baseline: N pages, N.N avg links. Updated [[page-a|Page A]], merged [[page-b|Page B]] + [[page-c|Page C]]. Result: N pages, N.N avg links.
```

## Key Rules

1. All structural changes (merge, split, archive) require user approval.
2. Backlink sync is automatic — no approval needed.
3. Use the correct page template when creating pages from splits.
4. Update `index.md` after every structural change.
5. Update `context.md` if refinement affects current focus areas.
6. Never delete pages — archive instead (move to `wiki/archive/`).
7. Refine only operates on `wiki/**`, `index.md`, `context.md`, `log.md`. Do not read `sessions/` or `raw/` during refinement; if a stale wiki page cites an untrusted source, pull excerpts via `kb read-raw` / `kb read-session` with user approval.

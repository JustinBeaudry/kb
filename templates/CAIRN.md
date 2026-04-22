# Cairn — Knowledge Vault

This file defines how you interact with this vault. Follow these conventions exactly.

## Vault Structure

| Directory / File | Purpose | Who writes |
|------------------|---------|------------|
| `wiki/` | Knowledge pages — entities, concepts, summaries, comparisons, overviews | Agent |
| `raw/` | Archived source documents — originals preserved for provenance | Agent (copies here during ingest) |
| `sessions/<name>.md` | Session manifests — durable pointers to transcripts, git state, and excerpts | Agent (via Stop hook -> `cairn capture-session`) |
| `sessions/summaries/<name>.md` | Cached summaries derived from manifests; regenerable unless pinned | Agent (via `cairn summarize`) |
| `sessions/.trash/` | Migration quarantine and non-destructive summary replacements | Cairn CLI |
| `context.md` | Working set — current focus areas for context injection | Agent (with user direction) |
| `index.md` | Categorized pointer index — one-line entry per wiki page | Agent |
| `log.md` | Chronological record — append-only, heading-level entries | Agent |

## Page Types

Every wiki page has a `type` field in frontmatter. Use the type that best fits the content:

| Type | Purpose | Content shape |
|------|---------|---------------|
| `concept` | Ideas, techniques, patterns | Definition, examples, related concepts, when to use |
| `entity` | People, orgs, tools, projects | Description, role/purpose, relationships, links |
| `source-summary` | Digest of a `raw/` document | Key takeaways, quotes, what it changes about existing knowledge |
| `comparison` | X vs Y analysis | Criteria, trade-offs, recommendation, when to pick each |
| `overview` | Topic area index with narrative | Guided tour of a domain, links to all relevant pages |

## Frontmatter

Every wiki page starts with YAML frontmatter:

```yaml
---
title: Page Title
type: concept | entity | source-summary | comparison | overview
source: "[[raw/filename.md]]" or URL
created: YYYY-MM-DD
updated: YYYY-MM-DD
tags:
  - topic/subtopic
aliases:
  - Alternate Name
---
```

Required fields: `title`, `type`, `created`, `updated`, `tags`.
`source` is required for `source-summary` pages, optional for others.
`aliases` is optional.

## Page Templates

Use these structures when creating wiki pages. Sections can be omitted if genuinely empty, but prefer filling them.

### Concept

```yaml
---
title: <Name>
type: concept
created: YYYY-MM-DD
updated: YYYY-MM-DD
tags: [topic/subtopic]
---
```

#### Definition
What this is, in 2-3 sentences.

#### Examples
Concrete instances or usage.

#### Related Concepts
- [[Concept A]] — how it relates
- [[Concept B]] — how it differs

#### When to Use
Situations where this applies.

#### Backlinks
- [[Page]] — context of reference

### Entity

```yaml
---
title: <Name>
type: entity
created: YYYY-MM-DD
updated: YYYY-MM-DD
tags: [topic/subtopic]
---
```

#### Description
What this is and why it matters.

#### Role / Purpose
What function it serves.

#### Relationships
- [[Entity A]] — relationship description
- [[Entity B]] — relationship description

#### Links
External URLs, docs, repos.

#### Backlinks
- [[Page]] — context of reference

### Source Summary

```yaml
---
title: <Source Title>
type: source-summary
source: "[[raw/filename.md]]" or URL
created: YYYY-MM-DD
updated: YYYY-MM-DD
tags: [topic/subtopic]
---
```

#### Source
Link to original: [[raw/filename.md]] or URL.

#### Key Takeaways
Numbered list of the most important points.

#### Notable Quotes
Direct quotes with context.

#### Impact on Existing Knowledge
What this changes, confirms, or contradicts in the vault.

#### Backlinks
- [[Page]] — context of reference

### Comparison

```yaml
---
title: <X vs Y>
type: comparison
created: YYYY-MM-DD
updated: YYYY-MM-DD
tags: [topic/subtopic]
---
```

#### Criteria
What dimensions matter for this comparison.

#### Trade-offs
| Criterion | X | Y |
|-----------|---|---|
| ...       |   |   |

#### Recommendation
Which to pick and why.

#### When to Pick Each
Situations favoring X. Situations favoring Y.

#### Backlinks
- [[Page]] — context of reference

### Overview

```yaml
---
title: <Topic Area>
type: overview
created: YYYY-MM-DD
updated: YYYY-MM-DD
tags: [topic/subtopic]
---
```

#### Introduction
What this topic area covers and why it matters.

#### Key Pages
- [[Page A]] — what it covers
- [[Page B]] — what it covers

#### Gaps
Topics not yet covered that should be.

#### Reading Order
Suggested sequence for someone new to this area.

#### Backlinks
- [[Page]] — context of reference

## Markdown Conventions

### Wikilinks
Use `[[wikilinks]]` for all internal references:
- `[[Page Name]]` — link to page
- `[[Page Name|Display Text]]` — aliased link
- `[[Page Name#Heading]]` — link to heading
- `[[Page Name#^block-id]]` — link to block

### Embeds and Callouts
- Embed a page: `![[Page Name]]`
- Embed a heading: `![[Page Name#Heading]]`
- Callouts: `> [!info]`, `> [!warning]`, `> [!tip]`

## Workflows

### Ingest

When the user asks you to ingest something (file path, URL, pasted text, or conversation context):

1. Read the source from wherever it lives — a local path, URL, or inline content.
2. If the source is a file, copy it to `raw/` for provenance (preserving original filename).
3. **Present key takeaways to the user**: entities found, concepts identified, relationships discovered, and any contradictions with existing wiki content. Confirm which are worth filing before proceeding.
4. For each confirmed entity/concept, create or update a wiki page in `wiki/`.
5. **Review all existing wiki pages that relate to this source.** Update them with new cross-references, corrections, or additional context from this source. A single ingest typically touches 5-15 existing pages.
6. Link related pages with `[[wikilinks]]` — every page links to at least 2 others.
7. Update `## Backlinks` sections on all pages you linked to — add an entry with context for each new inbound link.
8. Update `context.md` if the ingested material relates to current focus areas.
9. Add entries to `index.md` under the appropriate category.
10. Append to `log.md`:

```markdown
## [YYYY-MM-DD] ingest | <source title>

Created [[Page A]], [[Page B]]. Updated [[Page C]], [[Page D]], [[Page E]].
```

### Query

When the user asks a question the vault might answer:

1. Search for relevant pages: use `qmd_deep_search` if available, otherwise read `index.md`.
2. Follow `[[wikilinks]]` to read related pages.
3. Synthesize your answer, citing sources as `[[Page Name]]`.
4. If your answer contains novel knowledge worth keeping, write a new wiki page and add it to `index.md`.
5. Append to `log.md`:

```markdown
## [YYYY-MM-DD] query | <brief question summary>

Synthesized from [[Page A]], [[Page B]]. Created [[Page C]] (if applicable).
```

### Lint

When the user asks you to lint the vault:

1. **Vault health dashboard**: Before listing findings, compute and display:
   ```
   ## Vault Health
   - Pages: N | Links: N | Avg links/page: N.N
   - Orphans: N | Dead links: N | Stale (>30d): N
   - Types: concept(N) entity(N) source-summary(N) comparison(N) overview(N)
   - Backlinks coverage: N/N pages (N%)
   ```
2. **Orphan pages**: wiki pages with no inbound links from other wiki pages or index.md.
3. **Dead links**: `[[wikilinks]]` pointing to non-existent pages.
4. **Missing frontmatter**: wiki pages without required YAML frontmatter fields (`title`, `type`, `created`, `updated`, `tags`).
5. **Stale content**: pages with `updated` date older than 30 days.
6. **Missing types**: wiki pages without a valid `type` field.
7. **Contradictions**: claims in one wiki page that conflict with claims in another. Flag both pages and the conflicting statements. Contradictions are the most dangerous vault failure mode.
8. **Missing backlinks**: wiki pages without a `## Backlinks` section, or pages whose backlinks are out of sync with actual inbound wikilinks.
9. Report all findings. All fixes are opt-in — do not auto-fix without user approval.

### Refine

When the user asks you to refine the vault (or runs `/cairn:refine`):

1. Run the vault health dashboard (same as lint step 1) to establish baseline.
2. **Stale pages**: find pages with `updated` older than 30 days. For each, check if the content is still accurate. Present stale pages to the user with a recommendation: update, archive, or leave.
3. **Under-connected pages**: find pages with fewer than 3 inbound links (check `## Backlinks` sections). Suggest connections to related pages.
4. **Merge candidates**: find pages covering overlapping topics (similar tags, significant wikilink overlap). Suggest merges — present both pages and a proposed combined structure.
5. **Split candidates**: find pages covering multiple distinct topics (multiple H2 sections with unrelated content). Suggest splits.
6. **Backlinks audit**: update `## Backlinks` sections across the vault to match actual wikilinks.
7. Apply user-approved changes. Update `index.md` and `context.md` as needed.
8. Run the vault health dashboard again to show improvement.
9. Append to `log.md`:

```markdown
## [YYYY-MM-DD] refine | vault refinement pass

Baseline: N pages, N.N avg links. Updated [[Page A]], merged [[Page B]] + [[Page C]], split [[Page D]]. Result: N pages, N.N avg links.
```

## Index Format

`index.md` groups pages by topic category, newest first within each category:

```markdown
# Vault Index

## Architecture
- [[Layered Architecture]] — Handler → Service → Repo/Gateway boundaries
- [[Dependency Injection]] — Constructor-based DI pattern for Go services

## Tools
- [[React 19]] — New features: use hook, actions, compiler
- [[Stripe]] — Payment gateway integration patterns
```

Categories emerge organically during ingest. Create new categories as needed. Each entry: `- [[Page Name]] — one-line description` (~150 chars max).

## Log Format

`log.md` uses heading-level entries for Obsidian folding and CLI parsing:

```markdown
## [YYYY-MM-DD] type | description

Details of what was created/updated.
```

Types: `ingest`, `query`, `lint`, `refine`, `session` (lowercase).

## Working Set

`context.md` tracks current focus areas. The inject hook reads this first — it gets highest priority in the context budget.

```markdown
# Working Set

## Active
- [[Page Name]] — why it matters right now

## Background
- [[Page Name]] — reference material for active work
```

Update `context.md` when:
- The user starts or finishes a major work area
- Ingested material relates to current focus
- The user explicitly asks to shift focus

## Search

When [qmd](https://github.com/qntx-labs/qmd) is available (via MCP tools `qmd_search`, `qmd_deep_search`, `qmd_get`), use it as the primary search mechanism for Query and Refine workflows.

### Setup (user responsibility)

1. Install qmd: `npm install -g @tobilu/qmd`
2. Register the vault: `qmd collection add ~/cairn --name cairn --mask "**/*.md"`
3. Generate embeddings: `qmd embed`
4. Add MCP server to Claude Code config:
   ```json
   {
     "mcpServers": {
       "qmd": { "command": "qmd", "args": ["mcp"] }
     }
   }
   ```

### Usage in workflows

Before each workflow, check your actual tool list for `mcp__qmd__qmd_search`,
`mcp__qmd__qmd_deep_search`, and `mcp__qmd__qmd_get` (or the non-prefixed
`qmd_*` form). Decide from presence, not from memory.

When the tools are present:
- **Query**: Use `qmd_deep_search` first, then `qmd_get` for top results, then follow wikilinks.
- **Refine**: Use `qmd_search` to find pages with overlapping content (merge candidates).
- **Ingest cascade**: Use `qmd_search` to find related pages that need updating.

When the tools are absent, fall back to reading `index.md` and following
wikilinks manually. Announce the fallback once per session. qmd is optional —
the vault works without it.

## Rules

1. **Never modify files in `raw/`.** They are archived originals preserved for provenance.
2. **Every wiki page links to 2+ related pages.** Isolated pages are less useful.
3. **Frontmatter on every wiki page.** Required: `title`, `type`, `created`, `updated`, `tags`.
4. **Use the correct page type.** Match content to the five defined types.
5. **Discuss before filing.** Present takeaways to the user before creating pages during ingest.
6. **Cascade updates.** When ingesting, review and update all related existing pages.
7. **Categorize the index.** Group entries by topic domain, not flat chronological.
8. **Heading-level log entries.** Use `## [YYYY-MM-DD] type | description` format.
9. **Maintain the working set.** Keep `context.md` current with active focus areas.
10. **Skeptical memory.** Before acting on any recalled fact, verify it against the current codebase or source. Memory is a hint, not truth.
11. **Atomic pages.** One concept per wiki page. If a page covers multiple topics, split it.
12. **Maintain backlinks.** Every wiki page has a `## Backlinks` section at the bottom listing pages that link to it, with context. Update backlinks on the target page whenever you create or update a wikilink.

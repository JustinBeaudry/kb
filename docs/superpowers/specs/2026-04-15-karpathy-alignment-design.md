# Karpathy LLM Wiki Alignment — Design Spec

**Date:** 2026-04-15
**Status:** Draft
**Scope:** Align Cairn's vault conventions, injection, and skill with Karpathy's LLM Wiki pattern (April 2026 gist)

## Problem

Cairn's core architecture (three-layer storage, three operations, local-first markdown) already matches Karpathy's LLM Wiki. But the vault conventions lack specificity in areas that determine whether knowledge actually compounds:

1. No page type taxonomy — all wiki pages are untyped blobs
2. Incomplete frontmatter — no `type`, no `source` provenance, no `updated` date
3. Ingest doesn't cascade — creates/updates one page instead of revising 5-15 related pages
4. No discuss-before-filing step — agent writes directly without human confirmation
5. Flat index — doesn't scale past ~30 pages
6. Naive injection — sessions can crowd out index content under budget
7. No contradiction detection in lint spec
8. log.md format not parseable by tooling
9. No working set concept for injection prioritization

## Changes

Files modified:
- `templates/CAIRN.md` — vault conventions (page types, frontmatter, workflows, lint, log format)
- `hooks/inject` — injection priority order
- `skills/cairn/SKILL.md` — skill instructions reflecting all changes
- `src/lib/templates.ts` — add `context.md` stub template
- `src/lib/constants.ts` — add `context.md` to vault files list
- `src/commands/init.ts` — scaffold `context.md` during init

### 1. Page Type Taxonomy

Add to CAIRN.md. Five types:

| Type | Purpose | Content shape |
|------|---------|---------------|
| `concept` | Ideas, techniques, patterns | Definition, examples, related concepts, when to use |
| `entity` | People, orgs, tools, projects | Description, role/purpose, relationships, links |
| `source-summary` | Digest of a `raw/` document | Key takeaways, quotes, what it changes about existing knowledge |
| `comparison` | X vs Y analysis | Criteria, trade-offs, recommendation, when to pick each |
| `overview` | Topic area index with narrative | Guided tour of a domain, links to all relevant pages |

`type` becomes required frontmatter on every wiki page.

### 2. Expanded Frontmatter

Replace current frontmatter spec:

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

- `date` removed, replaced by `created` + `updated`
- `type` required (see taxonomy)
- `source` required for `source-summary`, optional for others
- `updated` used by lint for staleness detection (30+ days without update)

### 3. Cascading Ingest

Full revised ingest workflow:

1. Read the source from wherever it lives (local path, URL, inline content)
2. If the source is a file, copy it to `raw/` for provenance (preserving original filename)
3. **Present key takeaways to the user**: entities found, concepts identified, relationships discovered, contradictions with existing wiki content. Confirm which are worth filing before proceeding.
4. For each confirmed entity/concept, create or update a wiki page in `wiki/`
5. **Review all existing wiki pages that relate to this source.** Update them with new cross-references, corrections, or additional context. A single ingest typically touches 5-15 existing pages.
6. Link related pages with `[[wikilinks]]` — every page links to at least 2 others
7. Update `context.md` if the ingested material relates to current focus areas
8. Add an entry to `index.md` under the appropriate category
9. Append to `log.md`: `## [YYYY-MM-DD] ingest | <source title>`

Step 3 is the discuss-before-filing gate. Step 5 is the cascading update. Step 7 is working set maintenance.

### 4. Categorized Index

Replace flat newest-first list with topic-grouped format:

```markdown
# Vault Index

## Architecture
- [[Dependency Injection]] — Constructor-based DI pattern for Go services
- [[Layered Architecture]] — Handler → Service → Repo/Gateway boundaries

## Tools
- [[React 19]] — New features: use hook, actions, compiler
```

Rules:
- Categories emerge organically during ingest
- Agent creates new categories as needed
- Newest entries first within each category
- Each entry: `- [[Page Name]] — one-line description` (~150 chars max)
- CAIRN.md defines the format, not the categories

### 5. Inject Script Prioritization

New priority order (no fallbacks — all files expected to exist):

1. **`context.md`** — working set, always injected first
2. **`index.md`** — fills remaining budget
3. **Recent sessions** — fills whatever remains

Default budget stays 2KB (configurable via `CAIRN_BUDGET`). With `context.md` taking priority, the most curated context always wins.

Implementation: read `context.md` first, subtract its size from budget, then index, then sessions.

### 6. Contradiction Detection in Lint

Add to lint workflow:

> **Contradictions**: Claims in one wiki page that conflict with claims in another (e.g., "Service X uses REST" in one page vs "Service X uses gRPC" in another). Flag both pages and the conflicting statements. Contradictions are the most dangerous vault failure mode.

### 7. log.md Heading Format

Replace `[TYPE] YYYY-MM-DD description` with heading-level entries:

```markdown
## [YYYY-MM-DD] type | description

Details of what was created/updated.
```

Types: `ingest`, `query`, `lint`, `session` (lowercase).

Benefits: Obsidian folding, Dataview queries, CLI parsing (`grep "^## "`).

### 8. Working Set — `context.md`

New file at vault root, part of default scaffold:

```markdown
# Working Set

Current focus areas for context injection. Updated by the agent when focus shifts.

## Active
<!-- Pages and topics currently being worked on -->

## Background
<!-- Reference material relevant to active work -->
```

Rules:
- Agent updates when user starts/finishes major work areas
- User can edit directly
- Inject script reads this first (highest priority)
- Agent proposes changes to working set during ingest if relevant

### 9. SKILL.md Updates

Update the skill file to reflect:
- New page types and when to use each
- Expanded frontmatter requirements
- Discuss-before-filing step in ingest
- Cascading updates emphasis
- `context.md` working set maintenance
- Categorized index format
- New log.md format

## Files Changed

| File | Change |
|------|--------|
| `templates/CAIRN.md` | Page types, frontmatter, ingest workflow, index format, log format, lint additions, context.md reference |
| `hooks/inject` | Priority order: context.md → index.md → sessions |
| `skills/cairn/SKILL.md` | Reflect all new conventions |
| `src/lib/templates.ts` | Add `CONTEXT_MD_STUB` template |
| `src/lib/constants.ts` | Add `context.md` to `VAULT_FILES` |
| `src/commands/init.ts` | Create `context.md` during scaffold |
| `templates/CAIRN.md` (on disk) | Source of truth for the template |

## Not Changed

- Hook architecture (SessionStart, PostCompact, Stop) — unchanged
- Vault directory structure (`wiki/`, `raw/`, `sessions/`) — unchanged, `context.md` added at root level
- CLI commands (`init`, `uninstall`) — unchanged behavior, just scaffold one more file
- Plugin registration — unchanged
- Test structure — existing tests remain valid, new tests for context.md scaffold and inject priority

## Success Criteria

- `cairn init` scaffolds `context.md` alongside other vault files
- Inject script reads `context.md` → `index.md` → sessions in priority order
- CAIRN.md teaches all five page types with examples
- Ingest workflow includes discuss-before-filing and cascading update steps
- Index uses categorized format
- log.md uses heading-level format
- Lint detects contradictions
- SKILL.md reflects all changes

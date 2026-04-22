# Cairn

A persistent memory plugin for Claude Code. Markdown vault maintained by
your agent across sessions. Based on [Karpathy's LLM Wiki pattern](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f).

## What it does

- **SessionStart hook** injects working set, index, and recent session summaries or manifests (priority order)
- **Stop hook** writes a small session manifest when you finish working
- **PostCompact hook** re-injects vault context after compaction (solves compaction amnesia)
- **CAIRN.md template** teaches Claude to ingest sources, answer queries, and lint your vault

## Install

```bash
bunx cairn init
```

Scaffolds `~/cairn` with vault structure and registers hooks in Claude Code.
Safe to run again — idempotent.

### Custom vault path

```bash
bunx cairn init --vault-path /path/to/vault
```

### Per-project vault

Create a `.cairn` file in your project root containing the vault path:

```
/path/to/project/vault
```

## Usage

### Ingest
Give Claude any source — file path, URL, pasted text, or conversation context:

> "Ingest this article on dependency injection"

Claude reads source, copies files to `raw/` for provenance, presents
takeaways for confirmation, writes wiki pages, cascades updates to related
pages, updates index and log. Single ingest typically touches 5-15 pages.

### Query
Ask Claude anything your vault might know:

> "What decisions did we make about the auth system?"

Claude checks index, follows wikilinks, answers with citations. Novel
insights get filed as new wiki pages automatically.

### Lint
Ask Claude to check vault health:

> "Lint the vault"

Reports orphan pages, dead links, missing frontmatter, stale content,
missing types, and contradictions.

### Refine
Ask Claude to improve vault structure:

> "Refine the vault"

Finds stale pages, under-connected pages, merge/split candidates, and
backlink gaps. Presents findings for approval, then applies changes.
Shows vault health before and after.

### Extract
Extract knowledge from session manifests into wiki pages:

> "Extract from sessions"

Sessions are sources. Claude asks `cairn summarize` to lazily create cached
summaries for unprocessed manifests, presents extraction candidates (entities,
concepts, decisions, patterns), and runs the standard ingest workflow on
confirmed items.

Toggle session-start nudge: `/cairn:extract on` or `/cairn:extract off`.

## Page types

Every wiki page has a `type` in frontmatter:

| Type | Purpose |
|------|---------|
| `concept` | Ideas, techniques, patterns |
| `entity` | People, orgs, tools, projects |
| `source-summary` | Digest of a `raw/` document |
| `comparison` | X vs Y trade-off analysis |
| `overview` | Guided index of a topic area |

Each type has a structural template defined in CAIRN.md with recommended sections.

## Vault structure

```
~/cairn/
  CAIRN.md        # Schema — conventions, workflows, rules
  context.md      # Working set — current focus areas (injected first)
  index.md        # Categorized pointer index, grouped by topic
  log.md          # Chronological operation log (heading-level entries)
  wiki/           # Knowledge pages (agent-maintained, typed)
  raw/            # Source documents (immutable, provenance)
  sessions/       # Session manifests (auto-generated)
    summaries/    # Cached summaries from manifests
    .trash/       # Migration quarantine and non-destructive replacements
```

## Context injection

Inject hook reads vault in priority order under a 2KB budget (configurable
via `CAIRN_BUDGET` env var):

1. **`context.md`** — working set, always first
2. **`index.md`** — categorized page index
3. **Recent sessions** — cached summaries first, then manifests as fallback

## Session migration

Cairn v0.6.0 stores session manifests separately from cached summaries. Existing
vaults can preview and apply the one-time migration:

```bash
bunx cairn migrate-sessions
bunx cairn migrate-sessions --apply --yes
```

`cairn doctor` warns when legacy session files remain and reports manifest,
summary, and trash counts.

## Search (optional)

For larger vaults, [qmd](https://github.com/qntx-labs/qmd) provides
BM25 + vector hybrid search over markdown files.

```bash
# Install
npm install -g @tobilu/qmd

# Register vault
qmd collection add ~/cairn --name cairn --mask "**/*.md"
qmd embed

# Add MCP server to Claude Code
# In your Claude Code MCP config:
# { "mcpServers": { "qmd": { "command": "qmd", "args": ["mcp"] } } }
```

When qmd MCP tools are available, Query and Refine workflows use
`qmd_deep_search` before falling back to manual index reading.

## Model choice

Stop hooks do not call an LLM. Summaries are generated on demand by
`cairn summarize`, which calls `claude -p --model haiku` by default. For tests
or custom integrations, set `CAIRN_SUMMARIZE_COMMAND` to a compatible command.

## Uninstall

```bash
bunx cairn uninstall
```

Removes hooks from Claude Code. Your vault is preserved.

## License

MIT

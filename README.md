# Cairn

A persistent memory plugin for Claude Code. Markdown vault maintained by
your agent across sessions. Based on [Karpathy's LLM Wiki pattern](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f).

## What it does

- **SessionStart hook** injects working set, index, and recent sessions (priority order)
- **Stop hook** writes structured session summary when you finish working
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
Drop a file in `~/cairn/raw/` and ask Claude:

> "Ingest the new file in raw/"

Claude reads source, presents takeaways for confirmation, writes wiki pages,
cascades updates to related pages, updates index and log. Single ingest
typically touches 5-15 existing pages.

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

## Page types

Every wiki page has a `type` in frontmatter:

| Type | Purpose |
|------|---------|
| `concept` | Ideas, techniques, patterns |
| `entity` | People, orgs, tools, projects |
| `source-summary` | Digest of a `raw/` document |
| `comparison` | X vs Y trade-off analysis |
| `overview` | Guided index of a topic area |

## Vault structure

```
~/cairn/
  CAIRN.md        # Schema — conventions, workflows, rules
  context.md      # Working set — current focus areas (injected first)
  index.md        # Categorized pointer index, grouped by topic
  log.md          # Chronological operation log (heading-level entries)
  wiki/           # Knowledge pages (agent-maintained, typed)
  raw/            # Source documents (immutable, provenance)
  sessions/       # Session summaries (auto-generated)
```

## Context injection

Inject hook reads vault in priority order under a 2KB budget (configurable
via `CAIRN_BUDGET` env var):

1. **`context.md`** — working set, always first
2. **`index.md`** — categorized page index
3. **Recent sessions** — newest first, fills remaining budget

## Model choice

Hooks use `claude -p` and inherit your model configuration. Haiku is the
sensible default for session summarization. Cairn is unopinionated — use
whatever model you prefer.

## Uninstall

```bash
bunx cairn uninstall
```

Removes hooks from Claude Code. Your vault is preserved.

## License

MIT

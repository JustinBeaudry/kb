# Cairn

A persistent memory plugin for Claude Code. Markdown vault maintained by
your agent across sessions. Based on [Karpathy's LLM Wiki pattern](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f).

## What it does

- **SessionStart hook** injects your vault's index and recent session summaries
- **Stop hook** writes a structured session summary when you finish working
- **PostCompact hook** re-injects vault context after compaction (solves compaction amnesia)
- **CAIRN.md template** teaches Claude to ingest sources, answer queries, and lint your vault

## Install

```bash
bunx cairn init
```

This scaffolds `~/cairn` with the vault structure and registers hooks in
Claude Code. Run it again safely — it's idempotent.

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

Claude reads the source, writes wiki pages, updates the index.

### Query
Ask Claude anything your vault might know:

> "What decisions did we make about the auth system?"

Claude checks the index, follows links, answers with citations.

### Lint
Ask Claude to check vault health:

> "Lint the vault"

Reports orphan pages, dead links, contradictions, stale content.

## Vault structure

```
~/cairn/
  CAIRN.md        # Schema — conventions, workflows, rules
  index.md        # Pointer index (~150 chars per entry)
  log.md          # Chronological operation log
  wiki/           # Knowledge pages (agent-maintained)
  raw/            # Source documents (user-owned, read-only to agent)
  sessions/       # Session summaries (auto-generated)
```

## Model choice

Hooks use `claude -p` and inherit your model configuration. Haiku is the
sensible default for session summarization. Cairn is unopinionated — use
whatever model you prefer.

## Uninstall

```bash
bunx cairn uninstall
```

Removes hooks from Claude Code settings. Your vault is preserved.

## License

MIT

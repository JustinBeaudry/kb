# KB

A persistent memory plugin for Claude Code. Markdown vault maintained by
your agent across sessions. Based on [Karpathy's LLM Wiki pattern](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f).

## What it does

- **SessionStart hook** injects vault context — a compact pointer by default
  (lazy mode), or the working set, index, and recent session summaries in
  eager mode
- **Stop hook** writes a small session manifest when you finish working
- **PostCompact hook** re-injects vault context after compaction (solves compaction amnesia)
- **Trust boundary** separates curated knowledge (`wiki/`) from untrusted
  content (`raw/`, `sessions/`), with sanctioned CLI commands for each tier
- **KB.md template** teaches Claude to ingest sources, answer queries, and lint your vault

## Install

Download the prebuilt `kb` binary for your platform, verify its checksum, and
put it on your PATH — one command:

```bash
curl -fsSL https://raw.githubusercontent.com/JustinBeaudry/kb/main/install.sh | sh
```

Installs to `/usr/local/bin` (falling back to `~/.local/bin` if that isn't
writable). Pin a version or change the location with env vars:

```bash
curl -fsSL https://raw.githubusercontent.com/JustinBeaudry/kb/main/install.sh \
  | KB_VERSION=0.7.0 KB_INSTALL_DIR="$HOME/bin" sh
```

Prefer not to pipe to a shell? Download the archive for your OS/arch from the
[Releases page](https://github.com/JustinBeaudry/kb/releases), verify it
against `checksums.txt`, extract the `kb` binary, and move it onto your PATH.
(Windows: grab the `.zip`.)

Then scaffold the vault:

```bash
kb init
```

Scaffolds `~/kb` with the vault structure. `init` does not touch Claude Code
configuration — hooks ship with the KB plugin:

```bash
claude plugin marketplace add JustinBeaudry/kb
claude plugin install kb@kb
```

Safe to run `init` again — idempotent. The plugin's hooks run TypeScript
directly and require [Bun](https://bun.sh); the standalone `kb` binary does
not.

### Custom vault path

```bash
kb init --vault-path /path/to/vault
```

### Per-project vault

Create a `.kb` file in your project root containing the vault path:

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

Sessions are sources. Claude enumerates unprocessed manifests with
`kb sessions --unprocessed`, generates cached summaries with `kb summarize`,
retrieves them through the ask-gated `kb read-session`, runs the standard
ingest workflow on confirmed items, and marks finished manifests with
`kb mark-extracted`. Summaries require the `claude` CLI (or a
`KB_SUMMARIZE_COMMAND` equivalent) — without one, `kb summarize` exits
nonzero and extraction cannot proceed.

Toggle session-start nudge: `/kb:extract on` or `/kb:extract off`.

## CLI reference

| Command | What it does |
|---------|--------------|
| `kb init` | Scaffold the vault (does not register hooks) |
| `kb doctor` | Check vault health, hook wiring, and inject-budget pressure |
| `kb uninstall` | Print plugin-removal instructions; vault is preserved |
| `kb map [query]` | Budget-bounded structural map of the wiki — node summaries for navigation |
| `kb get-node <id>` | Fetch a page or section by node ID (`--neighbors`, `--follow-wikilinks <n>`) |
| `kb recall <query>` | Search curated pages, return evidence envelope |
| `kb get <page>` | Fetch a curated page by name |
| `kb list-topics` | List index topics |
| `kb read-raw <file>` | Ask-gated bounded read from `raw/` |
| `kb read-session <file>` | Ask-gated bounded read from `sessions/` (incl. `summaries/`) |
| `kb sessions [--unprocessed]` | List session manifest names — never content |
| `kb mark-extracted <file>` | Mark a manifest as extracted |
| `kb capture-session` | Write a session manifest (used by the Stop hook) |
| `kb summarize <manifest>` | Generate or reuse a cached session summary |
| `kb summaries pin\|unpin` | Pin or unpin a cached summary |

All commands accept `--vault-path <dir>` (alias `-p <dir>`) to target a vault explicitly, overriding `KB_VAULT` and per-project `.kb` files.

## Trust boundary

Vault content is two-tier:

- **Curated** (`wiki/`, `index.md`, `context.md`) — trusted, retrieved via
  `kb recall`, `kb get`, `kb list-topics`.
- **Untrusted** (`raw/`, `sessions/`) — provenance and machine-generated
  content, readable only through the ask-gated `kb read-raw` /
  `kb read-session` (bounded excerpts, explicit `--approve` or `KB_APPROVE=1`
  in non-interactive use).

All retrieval commands emit a length-prefixed JSON envelope with source
attribution, and access is recorded in `.kb/access-log.jsonl` with hashed
queries — never plaintext. The plugin ships deny rules that block direct
Read/Grep of `raw/` and `sessions/`, plus a `security-self-test` hook that
detects when those rules regress.

## Page types

Every wiki page has a `type` in frontmatter:

| Type | Purpose |
|------|---------|
| `concept` | Ideas, techniques, patterns |
| `entity` | People, orgs, tools, projects |
| `source-summary` | Digest of a `raw/` document |
| `comparison` | X vs Y trade-off analysis |
| `overview` | Guided index of a topic area |

Each type has a structural template defined in KB.md with recommended sections.

## Vault structure

```
~/kb/
  KB.md           # Schema — conventions, workflows, rules
  context.md      # Working set — current focus areas
  index.md        # Categorized pointer index, grouped by topic
  log.md          # Chronological operation log (heading-level entries)
  wiki/           # Knowledge pages (agent-maintained, typed)
  raw/            # Source documents (immutable, provenance)
  sessions/       # Session manifests (auto-generated)
    summaries/    # Cached summaries from manifests
    .trash/       # Non-destructive quarantine for replaced summaries
  .kb/            # Operational state (not knowledge)
    state.json    #   toggles (e.g. autoExtractNudge)
    config.json   #   inject_mode and other settings
    *.jsonl       #   access and inject logs, hashed queries only
```

## Context injection

The inject hook has three modes, resolved in priority order:
`KB_INJECT_MODE` env var → `inject_mode` in `.kb/config.json` → `eager`.

- **`lazy`** — the default written to `.kb/config.json` on fresh installs. Injects
  a ~500-byte pointer (vault location, topic headings, retrieval commands);
  the agent pulls content on demand via `kb recall`/`kb get`.
- **`eager`** — the fallback for vaults without a config. Injects content
  directly under a 32KB budget (configurable via `KB_BUDGET`), in priority
  order:
  1. **`context.md`** — working set, always first
  2. **`index.md`** — categorized page index
  3. **Recent sessions** — cached summaries first, then manifests as fallback
- **`off`** — no injection.

`kb doctor` warns when the eager budget is under pressure.

## Environment variables

| Variable | Effect |
|----------|--------|
| `KB_VAULT` | Vault location (overrides `~/kb` and per-project `.kb` files) |
| `KB_INJECT_MODE` | `lazy` \| `eager` \| `off` — overrides `.kb/config.json` |
| `KB_BUDGET` | Eager-mode injection budget in bytes (default 32768) |
| `KB_MAP_BUDGET` | `kb map` output budget in bytes (default 16384); `--budget` overrides |
| `KB_SUMMARIZE_COMMAND` | Summarizer command (default `claude -p --model haiku`) |
| `KB_APPROVE` | `1` approves ask-gated reads non-interactively |

## Search (optional)

For larger vaults, [qmd](https://github.com/qntx-labs/qmd) provides
BM25 + vector hybrid search over markdown files.

```bash
# Install
npm install -g @tobilu/qmd

# Register vault
qmd collection add ~/kb --name kb --mask "**/*.md"
qmd embed

# Add MCP server to Claude Code
# In your Claude Code MCP config:
# { "mcpServers": { "qmd": { "command": "qmd", "args": ["mcp"] } } }
```

When qmd MCP tools are available, Query and Refine workflows use
`qmd_deep_search` before falling back to manual index reading.

## Model choice

Stop hooks do not call an LLM. Summaries are generated on demand by
`kb summarize`, which calls `claude -p --model haiku` by default. For tests
or custom integrations, set `KB_SUMMARIZE_COMMAND` to a compatible command.

## Uninstall

```bash
kb uninstall
```

Prints the plugin-removal command (`claude plugin remove kb`). Your vault is
preserved either way. To remove the binary itself, delete it from wherever
the installer placed it (`/usr/local/bin/kb` or `~/.local/bin/kb`).

## License

MIT

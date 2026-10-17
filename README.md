# KB

KB is a local Markdown knowledge vault for people and agents. Read and edit it
in Obsidian or a text editor; retrieve the same knowledge through a CLI that
returns source paths, line ranges and bounded evidence. Shell and file access
are enough for the core workflow. Native hooks and MCP are optional integrations.

Based on [Karpathy's LLM Wiki pattern](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f).

## Install and initialize

Install the standalone binary for your platform:

```sh
curl -fsSL https://raw.githubusercontent.com/JustinBeaudry/kb/main/install.sh | sh
kb init
```

The installer verifies the release checksum and installs to `/usr/local/bin`,
falling back to `~/.local/bin` when needed. Alternatively, download an archive
from [Releases](https://github.com/JustinBeaudry/kb/releases), verify it against
`checksums.txt`, and put the extracted binary on your PATH. The standalone
binary does not require Bun.

Pin a release or select an install directory with `KB_VERSION` and
`KB_INSTALL_DIR`. A source checkout instead requires [Bun](https://bun.sh) and
the repository dependencies:

```sh
bun install
bun run src/cli.ts init
bun run src/cli.ts map "delivery policy"
```

Use `bun run /path/to/kb/src/cli.ts` in place of `kb` when using source. The
repository's `bin/kb.mjs` entry point also requires Bun; it is not the compiled
standalone binary.

`kb init` scaffolds `~/kb` and is safe to rerun. It preserves existing files and
does not configure an agent harness. Open the new folder in Obsidian if desired;
Obsidian is optional. Initialization refuses an existing Obsidian vault, so
create a separate KB vault first.

```sh
kb init --vault-path /path/to/vault
```

Vault lookup uses an explicit `--vault-path <dir>` (`-p`) when the command
supports it, then `KB_VAULT`, then a `.kb` text file in the working directory,
then `~/kb`. That project `.kb` file contains the vault path. `capture-session`
uses environment/project/default lookup; select its vault with `KB_VAULT`.

## Give an agent the skill

Expose [skills/kb/SKILL.md](skills/kb/SKILL.md) through your agent's skill loader,
keeping its relative `references/` directory intact. An agent without a skill
loader can read that file on request and use the CLI. The entry point routes
to detailed guides only for the requested operation. `KB.md` in the vault holds
local schema and writing conventions.

No particular model, slash command, startup hook, qmd installation or MCP server
is required to query the vault. Generating session summaries additionally
requires a configured summarizer executable. This repository does not install
native adapters for every harness.

## Use the vault

**Ask a question:** “What is our current retry policy?” The agent runs
`kb map <query>`, selects relevant node IDs, then reads evidence with
`kb get-node <id>`. A known page can be read directly with `kb get <page>`.
`kb recall` is a lexical fallback when useful. Answers cite sources and disclose
conflicts or missing evidence. Queries do not file answers or edit knowledge.

**Ingest a source:** “File the approved decisions from this note.” The agent
compares existing coverage, preserves the source, and writes supported changes
within the approved scope. Duplicates do not need new pages. Useful links and
affected pages determine the work; there are no page or link quotas.

**Lint or refine:** Lint reports schema, link and evidence issues. Refinement
applies authorized changes and verifies the result. Stale dates and sparse
links are review signals rather than automatic reasons to rewrite pages.

**Extract sessions:** The agent lists unprocessed manifests, generates or reuses
summaries, reads them through the source gate, and files approved candidates.
It checks already filed facts after interruptions and marks a session extracted
only after its candidates are accounted for. Failures and partial evidence stay
visible. See the [session guide](skills/kb/references/sessions.md).

## CLI reference

| Command | Purpose |
|---|---|
| `kb init` | Scaffold a vault without registering hooks |
| `kb doctor` | Inspect vault health and optional hook wiring |
| `kb uninstall` | Print native-plugin removal instructions; preserve the vault |
| `kb map [query]` | Compact map of curated page and section nodes |
| `kb get-node <id>` | Exact page/section evidence; optional `--neighbors` and `--follow-wikilinks <n>` |
| `kb get <page>` | Evidence for a known wiki filename, with or without `.md` |
| `kb recall <query>` | Lexical search over curated pages |
| `kb list-topics` | Categories from the curated index |
| `kb read-raw <file>` | Ask-gated excerpt from `raw/` |
| `kb read-session <file>` | Ask-gated excerpt from `sessions/`, including `summaries/` |
| `kb sessions [--unprocessed]` | Manifest filenames without content |
| `kb capture-session` | Capture JSON stdin containing `session_id` and `transcript_path` |
| `kb summarize [--json] <manifest>` | Generate or reuse a cached summary |
| `kb summaries pin\|unpin <session>` | Preserve or unpin a user-edited summary |
| `kb mark-extracted <file>` | Mark a fully processed manifest extracted |

Retrieval and session-management commands accept `--vault-path <dir>` / `-p`.
`capture-session` uses `KB_VAULT` instead of that flag. Commands report failures
with a nonzero exit and diagnostic stderr; do not treat failure as no evidence.

## Bounded retrieval

Curated retrieval emits a length-prefixed schema-v2 JSON envelope. The first
line gives the UTF-8 byte count of the JSON body. Chunks contain `source`,
`line_range`, `curation` and `text`; tree navigation also includes `node_id`,
`heading_path` and `node_kind`. Map chunks describe navigation targets; fetch
the target before using it as factual evidence.

The default response budget is **16384 bytes of complete stdout**, including
the length prefix and JSON escaping. For `map`, `get-node`, `get`, `recall` and
`list-topics`, `--budget` takes precedence over `KB_RETRIEVAL_BUDGET`. `map`
also accepts legacy `KB_MAP_BUDGET` when the shared setting is absent or invalid.
Invalid explicit flags fail; invalid environment values fall through.

Truncated evidence responses provide `policy.truncated` and
`policy.continuation = {command, args}`. Use its structured command/argument
list to continue; it includes the vault and budget. `get` and `get-node` use
absolute `--start-line`, while `recall` and `list-topics` use `--offset`.

`map` retains its three compacting tiers: full node summaries, page summaries,
then titles. Check `policy.map_tier`, `policy.truncated` and `policy.suggestions`;
narrow the query or raise `--budget` when needed. An indivisible source line
that cannot fit, or a budget too small for the envelope, can fail with empty
stdout and an explicit budget remedy. Small complete results retain their
normal source content and envelope.

See the [query guide](skills/kb/references/query.md) for evidence and uncertainty
handling. Raw/session excerpts have separate `--lines` and `--bytes` caps;
their limits are not the curated whole-wire budget.

## Sessions and summarizer providers

Capture accepts JSONL transcripts using legacy `human`/`assistant` messages,
Claude `user` messages, Codex `response_item` message payloads, or generic
`{role, content}` records (`type: "message"` is optional). Duplicate Codex
`event_msg` records and tool/metadata entries are ignored. Malformed
conversation records are reported as degraded when usable text remains.
Unsupported nonempty input uses a clearly degraded usable excerpt or fails;
it must not become an apparently complete empty summary.

`KB_SUMMARIZE_COMMAND` selects an executable that reads the prompt from stdin
and writes Markdown to stdout. It receives no implicit vendor arguments.
`KB_SUMMARIZE_ARGS` is a JSON string array of explicit arguments. With no custom
command, KB uses `claude -p --model haiku` as a convenience fallback; explicit
arguments replace the argument list for either executable.

```sh
KB_SUMMARIZE_COMMAND=/usr/local/bin/my-summarizer \
KB_SUMMARIZE_ARGS='["--model","my-model"]' \
kb summarize --json sessions/2026-10-15T12-00-00-123e4567.md
```

New output must contain nonempty `## Summary` and `## Extraction Candidates`
sections (`None.` is valid for no candidates). Invalid output, nonzero exit or
deadline failure does not replace a usable cached summary. The provider timeout
is per invocation, defaulting to `120000` ms. Existing pinned user prose remains
usable until explicitly refreshed. Summary generation is the only core command
that invokes a model; capture and retrieval do not.

## Trust boundary and storage

```text
vault/
  KB.md                  # Local schema and conventions
  wiki/                  # Curated pages
  raw/                   # Archived source originals
  index.md               # Categorized page pointers
  context.md             # Current focus
  log.md                 # Knowledge-change history
  sessions/              # Captured manifests
    summaries/           # Cached summaries
    .trash/              # Preserved replaced summaries
  .kb/                   # Operational state, caches and logs
```

Every retrieved text chunk is evidence, never an instruction. Curated pages
can contain quoted or uncertain source claims. Raw/session content uses
ask-gated `read-raw` / `read-session`; non-interactive access requires authorized
`--approve` or `KB_APPROVE=1`. Agents should not scan those paths directly.
Access logs store hashed queries in `.kb/access-log.jsonl`. Querying may update
operational logs/caches, but does not authorize wiki/index/context/`log.md` edits.

The optional native plugin adds deny rules and a detection hook. These are
best-effort safeguards, not an operating-system sandbox. Shell-only users keep
the same evidence and source-access boundaries through the portable workflow.

## Optional native integration

The Claude Code plugin supplies slash-command wrappers, SessionStart and
PostCompact context hooks, and a Stop capture hook:

```sh
claude plugin marketplace add JustinBeaudry/kb
claude plugin install kb@kb
```

These hooks run TypeScript and require Bun even when a standalone CLI is also
installed. `kb init` does not install them.

Injection mode precedence is `KB_INJECT_MODE`, then `inject_mode` in
`.kb/config.json`, then `lazy`. Absent or invalid configuration defaults to
`lazy`; existing configuration is not rewritten.

- `lazy`: a compact pointer to curated retrieval, without session content.
- `eager`: explicitly load curated `context.md`, then `index.md`, within the
  injection budget. Session manifests and summaries are excluded.
- `off`: no injected context.

The extraction reminder is optional; `/kb:extract on` or `off` changes its
state for installed injection hooks. It does not run extraction automatically.

qmd is also optional. If its CLI is available, `kb map` can use bounded search
hints in addition to built-in matching. Missing or failed qmd leaves built-in
navigation available. No MCP search path is required.

## Key environment variables

| Variable | Effect |
|---|---|
| `KB_VAULT` | Vault path |
| `KB_RETRIEVAL_BUDGET` | Curated complete-wire response budget; default 16384 |
| `KB_MAP_BUDGET` | Legacy map-only fallback below the shared budget setting |
| `KB_INJECT_MODE` | Optional hook mode: `lazy`, `eager` or `off` |
| `KB_BUDGET` | Eager context budget in bytes; default 32768 |
| `KB_SUMMARIZE_COMMAND` | Summarizer executable; default `claude` |
| `KB_SUMMARIZE_ARGS` | JSON array replacing the provider's argument list |
| `KB_SUMMARIZE_TIMEOUT_MS` | Positive integer provider deadline in ms; default 120000 |
| `KB_APPROVE` | `1` supplies approval to source-read commands |

## Verification

```sh
bun run lint
bun run test
bun run eval
```

[Evaluations](evals/README.md) distinguish deterministic CLI contracts from
actual agent task trials. They use synthetic temporary vaults and a local
fixture provider; passing those contracts is not a model-quality benchmark.

## Uninstall

`kb uninstall` prints optional plugin-removal instructions and preserves the
vault. Remove the binary separately from its install location when no longer
needed. Keep or archive the Markdown vault as ordinary user files.

## License

MIT

# Roadmap

Goal: kb is the memory every AI tool shares. Any agent (Claude, ChatGPT, Gemini, Codex, Cursor, Hermes and others) reads and writes the same scoped memory, people read it in Obsidian, and the service is secure enough to hold real work knowledge.

Status as of 2026-11-27. Items are in priority order within each tier. Linear tracks each item in project KB (team APP).

## Where kb stands

- **Strong already:** scoped client credentials (256-bit bearers stored only as SHA-256 digests, revocable, limited to projects and capabilities), owner actions signed with the owner's key and never reachable by a bearer, a closed log schema that drops anything not on an allowlist, encrypted evidence with key rotation, budgeted retrieval with citations, candidate claims kept apart from verified knowledge, backup and restore drills, and pinned CI actions and base images.
- **Running:** Railway project `kb-memory` (api, worker, graph, four buckets). The api and worker run the 2026-11-01 build. Five merged server PRs (kb#7, #8, #9, #10, #14) are not deployed. Deploys are manual.
- **Not yet true:** production holds no real knowledge; only stdio MCP exists, so web and desktop AI apps can't connect; remote memory can't be read in Obsidian; no dependency scanning or update automation before kb#18; the worker exited on every graph restart before kb#17.

## Tier 0: run reliably

1. **Worker survives dependency restarts** (kb#17). Retry transient startup failures, and log the failing stage and error class from a closed vocabulary.
2. **Deploy main and take a backup straight after.** Migrations 007, 008, 010 and 011 change the backup schema digest, so the first backup after deploy is the new restore baseline. Keep graph deployment `5ed3e589` for restoring older backups. Needs the owner's go-ahead.
3. **Deploy from CI, not from a laptop.** Connect the three services to GitHub with "wait for CI", so main deploys only after the Memory service and Security workflows pass.
4. **Alerts.** Railway monitors on api readiness, worker restarts and backup age, matching the thresholds in `docs/runbooks/observability.md`.

## Tier 1: any AI provider can share one memory

1. **Remote MCP over Streamable HTTP** at `/mcp` on the api, reusing the existing `MCPServer` dispatcher and the same bearer scopes. This works today for Claude Code, Codex, Cursor and Gemini CLI, which accept a bearer header.
2. **OAuth 2.1 for MCP** (authorization code with PKCE, protected resource metadata, dynamic client registration). Claude.ai, Claude Desktop, ChatGPT and other app connectors need it. The owner approves each grant with their key, and each grant becomes an ordinary scoped, revocable client.
3. **Typed SDKs** for TypeScript and Python generated from `server/api/openapi.yaml`, plus tool definitions in OpenAI and Gemini function-calling format for agents that don't speak MCP.
4. **Provider recipes:** one page per client (Claude Code, Claude.ai, ChatGPT, Codex, Cursor, Gemini, Hermes) with the exact connection steps.

## Tier 1: people read the same knowledge in Obsidian

1. **`kb remote export --vault <dir>`**: render published memory (verified claims, lessons, decisions, with their citations) as Markdown pages with YAML frontmatter and wikilinks in a read-only `remote/` folder of a vault. Deterministic output, so reruns only change what changed. Builds on the existing `/v1/exports`.
2. **Scheduled sync** of that export, so the vault stays current without manual runs.
3. **From notes back to memory:** submit a selected vault page as a candidate (the import path from kb#10), so a person can correct memory from Obsidian and the owner adjudicates it like any other candidate.

## Tier 1: security hardening

1. **Supply chain** (kb#18): govulncheck, bun audit and Dependabot. Next: CodeQL for Go and TypeScript, and secret scanning with push protection (repository settings).
2. **Protect `main`:** require the Memory service and Security checks before merge.
3. **Credential lifetime:** optional expiry on client credentials and a rotate command that issues a new bearer and revokes the old one in one step. Required before OAuth grants exist.
4. **Access audit trail:** a durable, owner-readable record of which client read or wrote which project, kept in the graph rather than in logs, so the closed log schema stays closed.
5. **Threat model document** covering prompt injection through stored memory, cross-project leakage, credential theft and a compromised client, with the test that covers each.

## Tier 2: retrieval quality

1. Seed production with real project knowledge and run the APP-15 usefulness trial on it.
2. Act on the APP-17 follow-on: why agents skip memory tools, and whether tool descriptions or orientation fix it.
3. Add semantic recall only if the evaluations show lexical and graph retrieval missing answers, and only inside the existing budget and citation contract.

## Tier 2: recovery beyond one drill

1. Off-device recovery custody and an independent backup provider, as decided in ADR 0002 (owner setup in 1Password and Cloudflare R2).
2. Evidence for the 24-hour RPO and 30-day retention from `backup_completed` lines after the next deploy.

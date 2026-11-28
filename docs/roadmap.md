# Roadmap

Goal: kb is the memory every AI tool shares. Any agent (Claude, ChatGPT, Gemini, Codex, Cursor, Hermes and others) reads and writes the same scoped memory, people read it in Obsidian, and the service is secure enough to hold real work knowledge.

Status as of 2026-11-28. Items are in priority order within each tier. Linear tracks each item in project KB (team APP).

## Where kb stands

- **Strong already:** scoped client credentials (256-bit bearers stored only as SHA-256 digests, revocable, expiring, limited to projects and capabilities), owner actions signed with the owner's key and never reachable by a bearer, a closed log schema that drops anything not on an allowlist, encrypted evidence with key rotation, budgeted retrieval with citations, candidate claims kept apart from verified knowledge, a per-client access audit trail, backup and restore drills, and pinned dependencies, actions and base images with vulnerability scanning and CodeQL.
- **Any AI tool can connect:** remote MCP at `/mcp` with a bearer for Claude Code, Codex, Cursor, Gemini CLI and Hermes, and OAuth (including Claude's published client identity) for Claude, ChatGPT and other apps, each grant approved by the owner on the host. Agents without MCP get OpenAI and Gemini function definitions and typed TypeScript and Python clients.
- **People read and correct it in Obsidian:** `vault-sync` keeps a read-only view current, and an edited page goes back as a candidate for the owner to judge.
- **Running:** Railway project `kb-memory` (api, worker, graph, four buckets), with the three services built from GitHub at a merged commit of `main`. Deploys are still started by hand.
- **Not yet true:** production holds no real knowledge yet; `main` deploys without waiting for CI; no Railway alerts; `main` has no branch protection.

## Tier 0: run reliably

1. **Worker survives dependency restarts** (done, kb#17). Retry transient startup failures, and log the failing stage and error class from a closed vocabulary.
2. **Back up after each deploy that changes the schema.** `main` is deployed. Migrations 007, 008, 010, 011 and later change the backup schema digest, so the first backup after deploying one is the new restore baseline. Keep graph deployment `5ed3e589` for restoring backups from before 007.
3. **Deploy from CI, not from a laptop.** Connect the three services to GitHub with "wait for CI", so main deploys only after the Memory service and Security workflows pass.
4. **Keep security patches from breaking restore** (done). Restore compares a restore-format digest of kb's backup format and the graph edition and version, not the whole tool manifest, so Go, library and base-image patch releases no longer start a restore boundary. The real-graph runtime test restores a backup on a build with a newer Go patch release and refuses one from another graph version.
5. **Alerts.** Railway monitors on api readiness, worker restarts and backup age, matching the thresholds in `docs/runbooks/observability.md`.

## Tier 1: any AI provider can share one memory

1. **Remote MCP over Streamable HTTP** (done, kb#21) at `/mcp` on the api, reusing the existing `MCPServer` dispatcher and the same bearer scopes. This works today for Claude Code, Codex, Cursor and Gemini CLI, which accept a bearer header.
2. **OAuth 2.1 for MCP** (done; authorization code with PKCE, protected resource metadata, dynamic client registration). Credential expiry and rotation (security hardening item 3) ship first, because every grant must expire. Claude.ai, Claude Desktop, ChatGPT and other app connectors need it. The owner approves each grant with their key, and each grant becomes an ordinary scoped, revocable client. Plan: [docs/plans/2026-11-27-oauth-mcp](plans/2026-11-27-oauth-mcp/plan.md).
3. **Typed SDKs** (done) for TypeScript and Python generated from `server/api/openapi.yaml`, plus tool definitions in OpenAI and Gemini function-calling format for agents that don't speak MCP. Done: the tool definitions ([function-calling.md](integrations/function-calling.md)) and the [TypeScript](../sdk/typescript/README.md) and [Python](../sdk/python/README.md) clients.
4. **Provider recipes** (done): [docs/integrations/clients.md](integrations/clients.md) has the exact setup for Claude Code, Codex, Cursor and Gemini CLI with a bearer, for Claude and ChatGPT over OAuth, and for Hermes Agent either way.
5. **OAuth Client ID Metadata Documents** (done, kb#55). A client can identify itself with an HTTPS URL to its metadata instead of dynamic registration, as Claude does by default and ChatGPT can. kb fetches the document from public addresses only, with no proxy, redirects or credentials, at most 8 KiB within 5 seconds, caches it for one minute to a day, and still matches redirect URIs exactly, so the default connector settings work. A client seen before is let through even when the 256 pending registrations are full; a first-time client still counts against that cap and is told to try again later when it is full.

## Tier 1: people read the same knowledge in Obsidian

1. **Read-only Obsidian view** (done, kb#22): `kb transfer vault-sync` renders the memory a client may see as Markdown pages with provenance front matter, tags and wikilinks in a folder of a vault. Output is deterministic, so reruns only change what changed, and claim text is rendered inert.
2. **Scheduled sync** (done): `vault-sync --every <minutes>` keeps the view current and logs one line per run, and the [migration runbook](runbooks/migration.md#keep-the-view-current) has launchd and systemd timers for running it unattended. A folder lock keeps two syncs from interleaving.
3. **From notes back to memory** (done): a person edits a synced page and submits it with `kb transfer preview` and `apply`. Only their text becomes the candidate, with the corrected claim and revision as provenance, and the owner adjudicates it like any other candidate. See the [migration runbook](runbooks/migration.md#correct-memory-from-obsidian).

## Tier 1: security hardening

1. **Supply chain**: govulncheck, bun audit and Dependabot (kb#18); every action pinned to a commit, release included (kb#20); CodeQL with the security-extended queries for Go, TypeScript, Python and the workflows themselves. Secret scanning with push protection is on in the repository settings.
2. **Protect `main`:** require the Memory service and Security checks before merge.
3. **Credential lifetime:** done. Optional expiry on client credentials (`kb-admin --ttl`, HTTP `ttl_seconds`, reported as `expires_at`) and `kb-admin rotate`, which issues a new bearer and revokes the old one in one step.
4. **Access audit trail** (done): every authorization decision an authenticated client causes is counted per client, project, capability and UTC day, and flushed every 30 seconds to `AccessRollup` nodes in the graph, kept 400 days. The owner reads it on the host with `kb-admin access`. Requests for projects outside a client's grant are recorded under `*`, so a client can't write arbitrary names into the trail, and the closed log schema stays closed.
5. **Threat model** (done): [docs/threat-model.md](threat-model.md) covers prompt injection through stored memory, cross-project leakage, credential theft, a compromised client, owner key loss and storage exposure, names the test behind each defense, and lists the open gaps.

## Tier 2: retrieval quality

1. Seed production with real project knowledge and run the APP-15 usefulness trial on it.
2. Act on the APP-17 follow-on: why agents skip memory tools, and whether tool descriptions or orientation fix it.
3. Add semantic recall only if the evaluations show lexical and graph retrieval missing answers, and only inside the existing budget and citation contract.

## Tier 2: recovery beyond one drill

1. Off-device recovery custody and an independent backup provider, as decided in ADR 0002 (owner setup in 1Password and Cloudflare R2).
2. Evidence for the 24-hour RPO from `backup_completed` lines after the next deploy, and for 30-day retention by checking that backup sets older than 30 days are still listed in the backup bucket once that much time has passed.

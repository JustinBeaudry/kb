# Connecting AI clients

Every client below talks to the same memory through the remote MCP endpoint,
`https://<your-kb-host>/mcp`, so what one agent learns is there for the others.
The endpoint's guarantees are in [remote.md](remote.md#remote-mcp-over-http).

## Before you start

1. **One credential per client.** On the host, enroll a separate agent
   credential for each client with only the projects and capabilities it
   needs, and an expiry (`--ttl 2160h` is 90 days). See
   [credentials.md](../runbooks/credentials.md#enrollment-and-rotation). One
   client can then be revoked or rotated without touching the others, and
   `kb-admin access --client-id ID` shows what each one did.
2. **Keep the bearer out of files you share.** Each recipe reads it from an
   environment variable, here `KB_MEMORY_BEARER`, set in your shell profile or
   a secret manager. Never put it in a checked-in project config.
3. **Check the connection** by asking the agent to call `memory_status`. A
   `401` means the bearer is wrong, revoked or expired. A `403` means the
   project or capability is outside the credential's grant.

Replace `https://memory.example.com` with your kb origin throughout.

## Claude Code

```sh
claude mcp add --transport http kb https://memory.example.com/mcp \
  --header "Authorization: Bearer $KB_MEMORY_BEARER"
```

Add `--scope user` to use it in every project.

## Codex

Codex reads `~/.codex/config.toml` (or `.codex/config.toml` in a trusted
project) and sends the bearer from the named environment variable:

```toml
[mcp_servers.kb]
url = "https://memory.example.com/mcp"
bearer_token_env_var = "KB_MEMORY_BEARER"
```

`codex mcp add` has no flag for a bearer variable, so edit the file.

## Cursor

Add to `~/.cursor/mcp.json` (all projects) or `.cursor/mcp.json` (one
project):

```json
{
  "mcpServers": {
    "kb": {
      "url": "https://memory.example.com/mcp",
      "headers": { "Authorization": "Bearer ${env:KB_MEMORY_BEARER}" }
    }
  }
}
```

Cursor resolves `${env:NAME}` from the environment it was started in. If the
variable is set only in your shell profile, start Cursor from a terminal, or
set it where your desktop session sees it. A literal `${env:...}` in a `401`
means the variable was not visible.

## Gemini CLI

Add the server to your user `settings.json`, not a project file you share:

```json
{
  "mcpServers": {
    "kb": {
      "httpUrl": "https://memory.example.com/mcp",
      "headers": { "Authorization": "Bearer <bearer>" }
    }
  }
}
```

`httpUrl` selects Streamable HTTP; `url` would select SSE, which kb does not
serve. Gemini CLI documents environment expansion only for a server's `env`,
not its `headers`, so the bearer is written into the file here. Keep that file
readable only by you (`chmod 600`), and give this client a short `--ttl` so a
leaked copy expires.

## Apps that require OAuth

Claude.ai and Claude Desktop connectors, ChatGPT connectors and other apps
that can't send a custom header need OAuth, which kb is adding now (see the
[OAuth plan](../plans/2026-11-27-oauth-mcp/plan.md)). Once it is deployed, you
add `https://memory.example.com/mcp` as a custom connector, the app opens an
approval page that shows an eight-letter code, and you approve that code on
the host. The app then gets its own scoped, expiring credential that you can
list and revoke like any other. Cursor, Codex and Gemini CLI can also use
that flow instead of a bearer.

## Clients without MCP

Agents that only speak HTTP use the REST API under `/v1` with the same bearer;
see [remote.md](remote.md) and `server/api/openapi.yaml`.

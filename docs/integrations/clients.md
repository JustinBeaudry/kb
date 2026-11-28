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
3. **Check the connection** by asking the agent to call `memory_status`. An
   HTTP `401` means the bearer is wrong, revoked or expired, and the client
   reports the server as failing to connect. A tool call outside the
   credential's projects or capabilities still connects, but the tool returns
   an error result with the code `forbidden`.

Replace `https://memory.example.com` with your kb origin throughout.

## Claude Code

Claude Code expands `${VAR}` in a server's `headers` when it loads the
server, so the bearer stays out of the file and out of the command line. Add
the server to the project's `.mcp.json`:

```sh
claude mcp add --scope project --transport http kb https://memory.example.com/mcp \
  --header 'Authorization: Bearer ${KB_MEMORY_BEARER}'
```

The single quotes matter: they keep your shell from putting the bearer itself
into the command's arguments, where other processes on the machine can read
it. The resulting entry is safe to check in, since it names only the variable:

```json
{
  "mcpServers": {
    "kb": {
      "type": "http",
      "url": "https://memory.example.com/mcp",
      "headers": { "Authorization": "Bearer ${KB_MEMORY_BEARER}" }
    }
  }
}
```

To keep the bearer in a keychain or secret manager instead of the
environment, use `headersHelper`, a command that prints
`{"Authorization": "Bearer ..."}`.

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

## Hermes Agent

Hermes reads MCP servers from `~/.hermes/config.yaml` under `mcp_servers`.
It can sign in with OAuth, so no bearer has to be written anywhere:

```yaml
mcp_servers:
  kb:
    url: "https://memory.example.com/mcp"
    auth: oauth
```

Hermes registers itself with kb and opens kb's approval page in your browser;
approve the code on the host as in
[Apps that sign in with OAuth](#apps-that-sign-in-with-oauth). kb accepts a
local sign-in return only on a loopback IP address such as `127.0.0.1`, so if
you set `oauth.redirect_host`, don't set it to `localhost`. Hermes keeps the tokens in `~/.hermes/mcp-tokens/kb.json`, so
keep that directory readable only by you. Run `hermes mcp login kb` to sign
in again after `kb-admin revoke` or when the grant ends.

To use an enrolled bearer instead, put `KB_MEMORY_BEARER=...` in
`~/.hermes/.env` and name the variable in the header. Make that file readable
only by you first (`chmod 600 ~/.hermes/.env`):

```yaml
mcp_servers:
  kb:
    url: "https://memory.example.com/mcp"
    headers:
      Authorization: "Bearer ${KB_MEMORY_BEARER}"
```

Hermes resolves `${VAR}` in `headers` from the environment, which includes
`~/.hermes/.env`. A `401` from kb means the bearer is wrong, revoked or
expired, or the variable was not set where Hermes reads it. Run
`/reload-mcp` in a session, or restart Hermes, after editing the file.

`tools.include` limits which kb tools Hermes sees, for example
`include: [memory_status, memory_orient, memory_search, memory_fetch]` for an
agent that should only read. That only hides tools; the credential's
capabilities are what kb enforces.

For automatic recall and turn capture inside a Hermes conversation, see the
native memory provider in [hermes.md](hermes.md). The MCP server above works
on its own or alongside it.

## Apps that sign in with OAuth

Claude (web and desktop), ChatGPT and other apps that can't send a custom
header connect with OAuth instead of a bearer. The app registers itself with
kb, opens an approval page, and gets its own scoped, expiring client once you
approve it on the host. Nothing is granted until you do.

The approval itself is the same for every app:

1. The app opens kb's approval page, which shows an eight-letter code and
   what the app asked for. Leave the page open; it continues by itself.
2. On the kb host, `kb-admin oauth-requests` lists pending requests with
   their codes, the host the app returns to, and the scope it asked for.
   Check that the code and host match what you see.
3. Approve with
   `kb-admin oauth-approve --code CODE --project LIST --capabilities LIST`,
   granting only what the app needs (for example `--capabilities
   policy:read,status:read,retrieval:read,evidence:read`). You can grant less
   than it asked for, never more. Or refuse with `kb-admin oauth-deny --code
   CODE`.

The app then appears in `kb-admin list` as `oauth-<grant id>`. Its access
token works only at `/mcp` and lasts an hour, the app refreshes it until the
grant ends (30 days by default, `--ttl` up to 90), and `kb-admin revoke
--client-id oauth-<grant id>` cuts it off at once. See
[credentials.md](../runbooks/credentials.md#approving-an-app-over-oauth).

### Claude

Custom connectors are available on Claude's Free (one connector), Pro, Max,
Team and Enterprise plans, and a connector added on claude.ai is also there in
Claude Desktop.

1. Go to **Customize > Connectors** and choose **Add custom connector**. On
   Team or Enterprise, an owner adds it under **Organization settings >
   Connectors** instead, and members then **Connect** it.
2. Name it `kb` and enter `https://memory.example.com/mcp` as the server URL.
3. For authentication choose **Sign in when needed**. For the OAuth client
   keep **Use Claude's published identity**: kb reads Claude's published
   client description and shows that site on the approval page.
   **Register automatically** also works, using kb's dynamic client
   registration, and the approval page then marks the app "not verified".
   Leave the client ID and secret empty.
4. Add the connector, then connect it. Approve the code on the host as above.

Claude reaches kb from Anthropic's cloud, not from your computer, so the kb
origin has to be reachable from the public internet. Turn the connector on
for a chat from the **+** menu under **Connectors**.

### ChatGPT

1. In ChatGPT on the web, add a custom MCP server (under **Plugins**, or
   **Settings > Connectors** with developer mode on, depending on your plan
   and workspace).
2. Enter `https://memory.example.com/mcp` as the server URL and choose
   **OAuth** for authentication, leaving any client ID and secret empty so
   ChatGPT registers itself.
3. Confirm the warning about custom servers, create it, then connect it and
   approve the code on the host as above.

A workspace admin may need to allow custom servers first. ChatGPT also reaches
kb from its own cloud, so the origin must be public.

### Checking an app's connection

Ask the app to call `memory_status`. If it can't connect, `kb-admin
oauth-requests` shows whether its request reached kb, and `kb-admin access
--client-id oauth-<grant id>` shows what an approved app has done. Cursor,
Codex and Gemini CLI can also use this flow instead of a bearer.

## Clients without MCP

Agents built on a model API's function calling can use the same tools from
published OpenAI and Gemini definitions; see
[function-calling.md](function-calling.md). Agents that only speak HTTP use
the REST API under `/v1` with the same bearer; see [remote.md](remote.md),
`server/api/openapi.yaml`, and the typed clients for
[TypeScript](../../sdk/typescript/README.md) and
[Python](../../sdk/python/README.md).

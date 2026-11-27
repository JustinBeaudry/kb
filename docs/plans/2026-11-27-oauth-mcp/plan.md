# OAuth 2.1 for remote MCP

Status: planned, 2026-11-27. Roadmap Tier 1 item 2. Prerequisites shipped: credential expiry and rotation (kb#37 for HTTP), the access audit trail (kb#39), the per-credential rate limit (kb#41).

## Why

Claude.ai, Claude Desktop, ChatGPT and other app connectors add a remote MCP server by URL and expect the MCP authorization flow: OAuth 2.1 with PKCE, discovered from the server. Today `/mcp` accepts only a bearer that the owner enrolls on the host and pastes into the client. That works for CLI agents and fails for every hosted connector.

## Rules this design must keep

These come from the threat model and are not negotiable:

1. **The owner approves every grant, with an owner credential.** No grant is ever issued by a browser click alone. The service never holds the owner's Nostr key.
2. **A grant is an ordinary scoped client.** It gets an `oauth-<id>` client record with a role of agent, explicit projects and capabilities, and an expiry. Everything that already holds for clients (scope checks, candidate-only writes, revocation on the next request, the access audit trail, the public guard) holds for it without new code paths.
3. **Tokens are stored as SHA-256 digests only**, like bearers today.
4. **Every token expires.** Access tokens live 1 hour. Refresh tokens rotate on every use and the grant itself expires after at most 90 days.
5. **Tokens are bound to kb.** The `resource` parameter (RFC 8707) must name kb's own MCP endpoint, and a token is accepted only there.
6. **Nothing a client says about itself is trusted.** Client names and logos from registration are shown to the owner as claims, never as identity.

## Protocol surface

kb is both the authorization server and the resource server, served by the existing api service.

| Endpoint | Standard | Notes |
| --- | --- | --- |
| `GET /.well-known/oauth-protected-resource/mcp` | RFC 9728 | The address §3.1 derives from `resource` = `https://<public host>/mcp`. The root address is not served, because §3.3 would make its `resource` the origin. `authorization_servers` = the same origin, added in phase 2. Served before the public guard: constant, credential-free, no graph work. |
| `401` on `/mcp` | RFC 9728 §5.1 | Adds `WWW-Authenticate: Bearer resource_metadata="…"`. Bearer clients see no other change. |
| `GET /.well-known/oauth-authorization-server` | RFC 8414 | `code` grant and `refresh_token` only; `S256` only; `token_endpoint_auth_methods_supported: ["none"]`. Ships with phase 2, never before the endpoints it names exist. |
| `POST /oauth/register` | RFC 7591 | Public clients only. Exact redirect URIs: `https://`, or `http://127.0.0.1`/`[::1]` with any port for native apps. Unapproved registrations expire after 24 hours; at most 256 pending. |
| `GET /oauth/authorize` | OAuth 2.1 | Requires `code_challenge` (S256), `state`, `resource`, and a registered `redirect_uri`. Creates a pending request and shows the approval page. |
| `GET /oauth/requests/{id}` | kb | Polled by the approval page. Returns pending until the owner decides, then, once, the URL to send the browser to: the redirect URI with the code and `state`, or with `error=access_denied` and `state`. Answers only the browser that started the request (see Owner approval). |
| `POST /oauth/token` | OAuth 2.1 | Code exchange with PKCE verifier, and refresh with rotation. |
| `POST /oauth/revoke` | RFC 7009 | Revokes the whole grant from either token. |

Client ID Metadata Documents (the newer MCP registration path) follow in phase 4; until then hosted connectors use dynamic registration, which they all support.

## Owner approval

`/oauth/authorize` never asks for a password. It records an `OAuthRequest` (client, redirect URI, requested scope, code challenge, state, 10-minute expiry) and shows a page with:

- an 8-character approval code, from an alphabet without look-alike characters;
- the redirect host, in large type, and the client's self-reported name marked as unverified;
- the projects and capabilities the client asked for.

The owner approves on the host:

```sh
kb-admin oauth-approve CODE --project demo --capability retrieval:read --ttl 30d
```

The command runs over the existing owner-only Unix socket. The owner chooses the final grant: projects and capabilities may be narrowed from the request and never widened beyond it, and `--ttl` is capped at 90 days. `kb-admin oauth-deny CODE` refuses it. The page polls the request. Once the owner approves, the poll returns the registered redirect URI with the code and the original `state`, and the page sends the browser there. Once the owner denies, or the request expires, it returns the same redirect URI with `error=access_denied` and `state` (RFC 6749 §4.1.2.1), so the connector stops waiting.

The poll answers only the browser that opened the request. `/oauth/authorize` sets a random request cookie (`HttpOnly`, `Secure`, `SameSite=Strict`, path `/oauth/requests/`), and the service keeps only its digest with the request. A poll without the matching cookie gets `404`, and the redirect URL is released once. So a code reaches the browser that started the flow, and the client still has to present the code verifier and the same redirect URI to use it.

Phase 3 adds approval in the browser with a NIP-07 signer: the page asks the extension to sign an `OwnerChallenge` whose digest binds the request ID, the exact grant and the redirect URI, and the service verifies it against the enrolled owner key, exactly as decision challenges are verified today.

## Grants and tokens

- **Approval** creates the client `oauth-<grant id>` through the same enrollment path as `kb-admin enroll`, with the chosen grant, plus an `OAuthGrant` node holding the registration ID, redirect URI, the grant's own expiry, the current refresh-token digest and the digests of refresh tokens already used.
- **The grant's expiry is fixed at approval** and stored only on the `OAuthGrant`. Each token response sets the client's bearer expiry to one hour from now or the grant's expiry, whichever is sooner, so refreshing can never carry a grant past its limit. Code exchange and refresh refuse a grant past its expiry, and expiry removes the client record.
- **Authorization codes** are single use, live 60 seconds, and are bound to the redirect URI and code challenge. A second use of a code revokes the grant (OAuth 2.1 §4.1.2).
- **Access tokens** are the client's bearer, rotated by each token response. They are bound to `/mcp`: the client record carries `resource: mcp`, `Authenticate` puts it on the principal, and every route other than `/mcp` refuses a principal bound to a resource. A test sends an OAuth access token to `/v1/exports` and expects `401`.
- **Refresh tokens** rotate on every use. The used token's digest moves to the grant's used list, which is kept until the grant ends, so an old token still finds its grant. Presenting one that was already used revokes the grant, because it means a copy is in someone else's hands (RFC 9700 §4.14).
- **Revocation** by `kb-admin revoke oauth-<id>`, `/oauth/revoke`, or grant expiry all remove the client record, so the next request fails.

## What the owner sees

- `kb-admin list` shows OAuth grants with their redirect host and expiry.
- `kb-admin access --client-id oauth-<id>` shows what a grant touched.
- The threat model gains an "OAuth grant" section, closing open gap 3.

## Abuse and failure cases

| Case | Handling |
| --- | --- |
| Registration flood | The bearer guard cannot serve routes used before a token exists, so phase 2 adds an unauthenticated guard for `/oauth/*`: a small global rate and concurrency bound plus a per-source limit, applied before any graph work. Pending registrations and requests are capped and expired. |
| Phishing the owner with a look-alike client | The page shows the redirect host and marks the name unverified; the owner types the grant on the host, so a click alone grants nothing. |
| Stolen authorization code | PKCE; 60-second life; reuse revokes. |
| Stolen refresh token | Rotation with reuse detection revokes the grant. |
| Token replayed to another server | Audience is kb's own resource URI only. |
| Clickjacking the approval page | `Content-Security-Policy: frame-ancestors 'none'`, no inline script. |
| Open redirect | Redirect URIs must match a registered value exactly; errors before the redirect URI is validated are shown on kb's page, never redirected. |

## Phases

1. **Discovery.** Protected resource metadata and the `WWW-Authenticate` header on `401` from `/mcp`. No change for bearer clients. The public origin comes from `KB_PUBLIC_URL`, or Railway's `RAILWAY_PUBLIC_DOMAIN`, never from request headers. The metadata names no authorization server yet, so clients are not sent to endpoints that do not exist.
2. **Grants.** Authorization server metadata and `authorization_servers` in the resource metadata, registration, authorize, host approval, token exchange, refresh rotation, revocation. Real-graph tests for every row in the abuse table, and an end-to-end test that drives the flow with a scripted client.
3. **Browser approval** with a NIP-07 signer.
4. **Client ID Metadata Documents.**

Phase 2 ships as three pull requests, each safe on its own: (2a) the `/oauth/*` guard and registration, (2b) authorize, the request poll and `kb-admin oauth-approve`/`oauth-deny`, (2c) token exchange, refresh, revocation, and last the authorization server metadata that tells clients the flow exists.

Each phase is its own pull request and ships behind no flag: phase 1 is inert without phase 2, and phase 2 does nothing until the owner approves a request.

## Tests that must exist before phase 2 merges

- A grant can never carry a project or capability the request did not ask for.
- A grant that keeps refreshing still ends at its own expiry.
- An OAuth access token is refused on every route other than `/mcp`.
- A denied or expired request redirects with `error=access_denied` and the original `state`.
- A poll without the request cookie learns nothing.
- A code works once, only with its verifier and redirect URI, and only within 60 seconds.
- A reused refresh token revokes the grant, and the next MCP call fails.
- A token request naming any `resource` other than kb's own MCP endpoint is refused.
- Revoking the grant stops the next request.
- An unapproved request expires and leaves no client record.
- Grant activity appears in the access audit trail.

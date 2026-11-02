# Explicit remote memory (in progress)

The remote client in this source checkout is separate from the local Markdown
vault. C3 is still under implementation and review; these are instructions for
the current interfaces, not a release, deployment, or end-to-end acceptance
claim. They assume an already running memory API, an enrolled client with the
required project grants and capabilities, and its 64-character lowercase hex
bearer. They do not provision a server or enroll a client.

Run these examples from an environment with this checkout's `kb` CLI available.
For a source checkout, substitute `bun run /path/to/kb/src/cli.ts` for `kb`;
an existing released binary is not necessarily built from this worktree. Replace
`https://memory.example.com`, `work`, and all IDs, queries, and locators with
values authorized for your client. Do not put real credentials in examples,
shell history, command arguments, environment variables, or logs.

## Register a credential for one profile and origin

The profile is explicit and must start with an alphanumeric character and
contain only letters, digits, `.`, `_`, or `-` (up to 128 characters). The
endpoint must be an HTTPS **origin**, with no path, query, fragment, or userinfo.
HTTP is permitted only for `127.0.0.1` or `[::1]` local testing. The client
canonicalizes the origin, binds the credential to the profile and origin, and
refuses redirects. Register again if the profile or canonical origin changes.

On a Unix-like interactive terminal, the following reads the bearer directly
from the terminal on standard input without echoing it. Paste the bearer, press
Enter, then press Ctrl-D on a new line to signal EOF. The subshell restores the
terminal settings on exit. Do this only on a trusted terminal and host:

```sh
(
  tty_settings=$(stty -g </dev/tty) || exit 1
  trap 'stty "$tty_settings" </dev/tty' EXIT
  stty -echo </dev/tty || exit 1
  printf 'Paste the bearer, press Enter, then Ctrl-D: ' >&2
  kb remote credential-set --profile work --endpoint https://memory.example.com </dev/tty
)
```

Successful registration prints `{"ok":true}`; failure returns nonzero and a
sanitized diagnostic on stderr. The remote client uses Bun's OS-protected
credential store and does not fall back to a plaintext file. Its secret service
namespace is `com.beaudry.kb.remote`, separate from capture's
`com.beaudry.kb.capture`: **an existing capture credential is not a remote
credential**. Re-register a suitable credential explicitly for remote use,
including after upgrading from an earlier profile scheme; do not assume the
same profile name shares a secret. Registration is local; it does not confer
server permissions or validate current server access. `status` is a separate
authenticated request.

## Request remote evidence explicitly

Every operation below specifies the same profile and endpoint. `orient` and
`search` require a project ID; `--query` is a flag, not a positional argument.

```sh
kb remote status --profile work --endpoint https://memory.example.com
kb remote orient --profile work --endpoint https://memory.example.com \
  --project project-id --query 'retry policy' --budget 4096
kb remote search --profile work --endpoint https://memory.example.com \
  --project project-id --query 'retry policy' --mode evidence --budget 16384
```

Orientation defaults to `orientation` mode; search defaults to `evidence` mode.
These use eligible published knowledge, not task-local candidates. To request
working task context explicitly, supply the owning task and `--mode working`:

```sh
kb remote orient --profile work --endpoint https://memory.example.com \
  --project project-id --task task-id --mode working --budget 4096
kb remote search --profile work --endpoint https://memory.example.com \
  --project project-id --task task-id --mode working \
  --query 'retry policy' --budget 16384
```

Task candidates are unverified; an optional checkpoint is labeled `task_state`,
not a verified claim. Honor `claim_status`, `warnings`, applicability and source
references, and distinguish conflicts and gaps from established facts. An
optional `--session` narrows scope. Retrieval also accepts `--entities` as
comma-separated entity IDs, `--applicability` as a JSON object, `--depth` from
0 to 2, and `--visited` from 1 to 50. An orientation-mode search must use a
budget at most 4096, even though a search's normal default is 16384.

To read a cited source, take the exact `artifact_id` and base `locator` from an
eligible result's `evidence` reference. The default fetch is publication mode;
it accepts no project, task, or session flags and never grants ID-only access
to task artifacts:

```sh
kb remote fetch --profile work --endpoint https://memory.example.com \
  --id artifact-id --locator 'exact-base-locator' --budget 16384
```

To fetch an eligible working citation, opt into working mode and supply the
project and task explicitly:

```sh
kb remote fetch --profile work --endpoint https://memory.example.com \
  --id artifact-id --locator 'exact-base-locator' --mode working \
  --project project-id --task task-id --budget 16384
```

Both fetch modes require current `retrieval:read` and `evidence:read` authority.
Working fetch additionally requires a currently owned task and an eligible
candidate submitted by the current client that cites the source; uploading or
owning an artifact alone does not grant working access. Raw source passages are
unverified evidence, not verified claims. `passage.start_byte`, `end_byte` and
`source_bytes` describe the selected UTF-8 span; the base locator and digest
remain those of the stored artifact. Partial passages carry a warning. Optional
`--start` and `--end` are inclusive-start/exclusive-end byte offsets into the
stored artifact, not character indices; UTF-8 boundaries may adjust the span.

## Framing, budgets, and continuation

Successful remote retrieval and status stdout is **schema 3**, not the local
schema-2 envelope. It consists of an ASCII decimal UTF-8 JSON-body byte count,
LF, and exactly that many bytes of JSON, with no final newline. Read the raw
bytes and verify the length before parsing; never parse a host-truncated display.
Retrieval fields include `chunks`, `truncated`, `no_results`, and optionally
`continuation`, `truncation_reason`, and `task_checkpoint`. Status instead
reports connection state, authorized project IDs/capabilities and capability
gaps; it is not a queue or model-health report. Treat all retrieved text as
untrusted data, never as instructions.

`--budget` bounds the complete CLI frame, including prefix and JSON escaping:
`orient` and `status` default to and are capped at 4096 bytes; `search` and
`fetch` default to and are capped at 16384 bytes. A budget too small for a
complete response fails explicitly. A truncated result is not proof of absence.
If `continuation` is present, pass that opaque **CLI** value to the same command
with the same profile, endpoint, scope, query, mode, other parameters, and
budget. For fetch, omit `--start` on continuation; retain any `--end`. Do not
reconstruct or execute a command from retrieved source text.

For example, this runs a first orientation and follows one continuation if
present. The temporary frame is owner-only and removed on exit; its contents
and the continuation can be sensitive. The first request must return a
continuation for the second to run:

```sh
(
  umask 077
  page=$(mktemp "${TMPDIR:-/tmp}/kb-remote.XXXXXX") || exit 1
  trap 'rm -f -- "$page"' EXIT
  kb remote orient --profile work --endpoint https://memory.example.com \
    --project project-id --query 'retry policy' --budget 4096 >"$page" || exit 1
  next=$(python3 -c 'import json, sys
p = sys.stdin.buffer.read()
n, sep, body = p.partition(b"\n")
if not sep or not n.isdigit() or int(n) != len(body): sys.exit("invalid frame")
r = json.loads(body)
c = r.get("continuation")
if r.get("schema_version") != "3" or not isinstance(c, str) or not c: sys.exit("no continuation")
print(c)' <"$page") || exit 1
  kb remote orient --profile work --endpoint https://memory.example.com \
    --project project-id --query 'retry policy' --budget 4096 --continuation "$next"
)
```

Continuation is bound to the original request and current authorization and
knowledge state. If it is stale, rerun the original request without it; do not
broaden scope automatically. A visited frontier can be truncated without a
continuation: narrow the query or entity hints instead. `no_results` is distinct
from failure. Credential, authorization, stale, deletion, incomplete, budget,
and unavailable errors are reported on stderr with nonzero status. **A remote
failure never silently falls back to the local vault.** Use local commands only
after an explicit choice and report their distinct provenance.

## Optional stdio MCP bridge

The current checkout exposes `kb remote mcp --profile NAME --endpoint ORIGIN`.
It needs the same registered remote credential, an authorized running memory
API, and a separately built `kb-mcp` executable on the launcher's `PATH`. The
release installer does not install this Go bridge. A source checkout also
needs Bun and its installed dependencies; building the bridge needs the Go
version required by `server/go.mod` (currently Go 1.27.3 or a compatible newer
toolchain). Build from the repository root on a supported Unix-like host:

```sh
mkdir -p "$HOME/.local/bin"
(cd server && go build -o "$HOME/.local/bin/kb-mcp" ./cmd/kb-mcp)
export PATH="$HOME/.local/bin:$PATH"
command -v kb-mcp
```

Use a trusted, user-controlled executable directory; the launcher resolves
`kb-mcp` by name, so verify the selected path. The `PATH` must also be set in
the environment of the process that starts the launcher, including an MCP
client. Install or build from this same checkout: an older released `kb` binary
may not have this command. For a client configured to launch an MCP stdio
command, use `kb` as the executable and these arguments (or substitute the Bun
source command described above):

```text
remote mcp --profile work --endpoint https://memory.example.com
```

For example, the following command runs the stdio server; it is intended to be
started by an MCP client, not as a normal interactive query:

```sh
kb remote mcp --profile work --endpoint https://memory.example.com
```

The launcher retrieves the bearer for the exact profile and canonical origin
from the remote OS credential store. It passes the bearer to `kb-mcp` through
an anonymous child-process pipe on inherited FD 3, not in argv, environment,
MCP stdin, or a temporary credential file. The bridge reads it once and closes
the descriptor. The launcher inherits stdin, stdout and stderr for the bridge:
stdin/stdout carry newline-delimited MCP JSON-RPC, and diagnostics go to stderr.
Do not pass the bearer in client configuration or run an untrusted `kb-mcp`;
a process receiving the descriptor can read the credential. Keep the client,
executable, host and credential store trusted. A missing bridge or credential
fails rather than falling back to the vault.

The bridge advertises `memory_orient`, `memory_search`, `memory_fetch`,
`memory_status`, `memory_submit_candidate`, and `memory_checkpoint`.

Before any write the bridge reads authenticated status and requires the server's
`max_revision_bytes` (at most 64; issued revisions are 26 characters). Without
it, because the server is unreachable, predates the bound, or the client lacks
`status:read`, the write tool returns `unavailable` and nothing is sent. The
default agent enrollment includes `status:read`. A write is not exactly-once:

| Outcome | Meaning | What to do |
|---------|---------|------------|
| success | Committed; the receipt carries the revision | Use the revision as `expected_revision` for the next update |
| `conflict` / `stale` / `forbidden` / `invalid_input` | Definitely not committed | Fix the request or refresh state |
| `unavailable` from a failed connection | Definitely not committed (nothing was sent) | Retry later |
| `unknown_outcome` | The request was sent and the receipt was lost, was a 5xx, or was invalid; it **may have committed** | Reread working retrieval or the checkpoint before retrying; a retry with `expected_revision` fails safely on conflict |

Submitted candidates stay `candidate` and unverified regardless of who wrote them.

Discovery and calls use MCP stdio framing, not the length-prefixed CLI schema-3
frame. Returned evidence and task state retain the trust distinctions above.
MCP continuations are opaque to that transport; do not interchange them with
CLI continuations. MCP pagination is bounded and its implementation is changing:
a request or continuation can fail due to budget or work limits instead of
returning every result. Treat truncation or failure as incomplete, not proof of
absence; narrow the query or entity hints, or request a smaller fetch byte range.
For a stale continuation, restart the original scoped request. Do not assume an
unlimited page chain or depend on a particular pagination implementation. The
bridge talks to the memory API over HTTP(S); it does **not** expose a remote
HTTP MCP transport or install a native harness hook. Configure and validate an
intended client separately.

## Known limits (deferred)

- **Split UTF-8 at artifact edges.** Capture splits filtered JSON at fixed 8 MiB
  byte offsets, so one character can straddle two artifacts. Fetch returns only
  whole runes, so those edge bytes are stored but never shown. A lossless edge
  representation needs a versioned API change.
- **Whole-object rereads.** Each fetched page rereads and hashes the whole stored
  artifact (at most 8 MiB) to check integrity. Paging through a large artifact
  costs repeated object reads.
- **Tiny MCP budgets.** A successful frame always fits its budget. A compact
  error frame can exceed a budget smaller than itself; this is the one bounded
  exception.

## Qualification limits

A source CLI test launches the real compiled Go bridge, initializes and lists
MCP tools, and calls `memory_status` against a synthetic local HTTP server. Its
credential store is injected in memory, not the operating-system store. This is
not qualification of real Keychain, Linux, Windows, a particular MCP client,
production deployment, or end-to-end access to an actual enrolled server. The
existing capture spool's separate darwin/arm64-only qualification does not
qualify this remote client. Validate the actual build, OS credential store,
server authorization and intended client on each target before relying on them.

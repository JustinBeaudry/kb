# Hermes conversation over kb memory (in progress)

The provider in `integrations/hermes/` connects a Hermes Agent conversation to
the kb graph memory through the ordinary remote API. The graph is the only
authority. Hermes built-in notes are private working state. A Hermes answer is
derived output, not an independent observation. This page describes the current
interfaces. It does not claim that live conversational acceptance has been
done; that happens in O3.

## Qualification

| Harness | Pinned version | Integration | Automatic hooks | Fallback | Capture trust | Evidence |
| --- | --- | --- | --- | --- | --- | --- |
| Hermes Agent | 0.21.2 | Native memory provider `kb_memory` (entry point `hermes_agent.memory_providers`) | Recall (`prefetch`, `queue_prefetch`), turn capture (`sync_turn`, elected owner only), session rebind/end. Unsupported: checkpoint on compression, status, enforcement of graph-over-notes precedence | C3 MCP `kb-mcp`: `memory_checkpoint`, `memory_status` | `submitted` only (no attestation) | Hermes 0.21.2 loader and `MemoryManager` run (below); Python tests; Go contract `TestHermesConversationContract` |

### How the version was established

- `~/.local/bin/hermes` is a shim that runs
  `~/.hermes/hermes-agent/venv/bin/hermes`. That target does **not exist** on the
  qualification host, so the installed binary is broken and no version can be
  read from it.
- The Hermes package source that is present locally is `hermes-agent` **0.21.2**
  (`pyproject.toml` `version = "0.21.2"`). It is installed in editable mode in
  another project's environment. Only package source was read. No Hermes user
  data under `~/.hermes` (sessions, memories, config) was opened.
- PyPI's newest `hermes-agent` is 0.19.0, so 0.21.2 cannot be a dependency of
  this package. Instead, `hermes_contract.py` copies the 0.21.2
  `agent/memory_provider.py` hook signatures, and inside Hermes the provider
  subclasses the real `agent.memory_provider.MemoryProvider`.
- Qualification run (2026-11-24). `qualification/qualify_hermes.py` loaded the
  provider through Hermes' own `plugins.memory._load_provider_from_entry_point`
  and drove the real `agent.memory_manager.MemoryManager`. It used a scratch
  `HERMES_HOME` and a loopback stub of the kb API, with no model or account:

  ```text
  hermes_agent 0.21.2
  loaded KbMemoryProvider subclass: True
  is_available True
  tools ['kb_memory_fetch', 'kb_memory_orient', 'kb_memory_search', 'kb_memory_submit_candidate']
  system_prompt_has_precedence True
  prefetch_cites_revision True bytes 293
  tool_evidence_revision_ids ['rev-verified-1']
  api_paths ['/v1/retrieval:orient']
  ```

  The built-in note write and turn sync in that run reached no API path, because
  capture defaults to an external owner.

### Hook support (Hermes 0.21.2 `MemoryProvider`)

| Hook | Status | Behavior |
| --- | --- | --- |
| `name`, `is_available`, `unavailable_reason`, `initialize` | Implemented | Reads configuration only; never touches the network |
| `system_prompt_block` | Implemented | States that `[verified]` graph lines take precedence over built-in notes |
| `prefetch`, `queue_prefetch` | Implemented | Bounded orientation of at most 4096 bytes, cited by revision and evidence |
| `sync_turn` | Implemented | Passes turns to `kb capture` only when this provider is the elected owner |
| `get_tool_schemas`, `handle_tool_call` | Implemented | Four `kb_memory_*` tools; none shadow a Hermes core tool |
| `on_memory_write` | Implemented | Records the write locally; **nothing is uploaded** |
| `on_session_switch`, `on_session_end`, `shutdown` | Implemented | Rebinds the scope and releases the capture election |
| `get_config_schema` | Implemented | Environment-variable fields only; the bearer itself is never prompted for |
| `on_pre_compress` | **Unsupported (automatic)** | A checkpoint needs `checkpoint:write`, which this credential lacks. Use MCP `memory_checkpoint` |
| `recall_status`, `on_turn_start`, `on_delegation`, `identity_signature`, `backup_paths`, `save_config` | Not used | Hermes defaults apply; no provider state lives outside `HERMES_HOME` |

Other limits that are not hook gaps:

- **Built-in notes cannot be suppressed.** Hermes always injects its own
  `MEMORY.md`/`USER.md`. The provider cannot stop a stale note from appearing.
  It can only label graph lines and state their precedence. This precedence is
  advisory and is not enforced.
- **Hermes frames all prefetched memory as authoritative.** Its wrapper calls
  every prefetched line "authoritative reference data". Each line therefore
  carries its own status label (`[verified]`, `[disputed: uncertain ...]`,
  `[candidate: unverified]`), and the gateway refuses task candidates in
  published retrieval.
- **No trusted execution attestation.** Captured turns are `submitted`
  evidence, and role labels never make them authentic.

## Setup

Prerequisites:

- A running memory API.
- An owner-enrolled **agent** client with `status:read`, `retrieval:read`,
  `evidence:read`, and `candidate:write` on one project. Do not grant
  `checkpoint:write` to the provider.
- If the provider will capture turns, a **separate collector** credential
  registered with `kb capture credential-set`.

1. Install the provider into Hermes' own Python environment. Its pins
   (`httpx==0.28.1`, `pydantic==2.13.4`) match Hermes 0.21.2, so Hermes'
   dependencies do not change:

   ```sh
   uv pip install --python /path/to/hermes/venv/bin/python ./integrations/hermes
   ```

2. Store the agent bearer in an owner-only file. Do not put it in an
   environment variable, `.env`, or a command argument. The provider refuses a
   file that is not a regular `0600` file owned by you.

   ```sh
   (umask 077 && cat > ~/.config/kb/hermes-agent.bearer)   # paste, Enter, Ctrl-D
   ```

3. Configure Hermes. Run `hermes memory setup` and choose `kb_memory`, or set
   `memory.provider: kb_memory` and export these variables:

   | Variable | Meaning |
   | --- | --- |
   | `KB_MEMORY_ENDPOINT` | Origin of the memory API: HTTPS, or HTTP on `127.0.0.1`/`[::1]` only |
   | `KB_MEMORY_PROJECT` | Project ID the agent client is granted |
   | `KB_MEMORY_CREDENTIAL_FILE` | Path to the `0600` bearer file |
   | `KB_MEMORY_CAPTURE_OWNER` | `external` (default) or `provider` |
   | `KB_MEMORY_KB_BIN`, `KB_MEMORY_CAPTURE_PROFILE` | `kb` binary and collector profile, used when the provider captures |
   | `KB_MEMORY_PREFETCH_BUDGET` | Recall budget from 1 to 4096 bytes (default 4096) |

4. Optional: for checkpoints and status, add the C3 MCP bridge to Hermes'
   `mcp_servers`. It reads the bearer from an inherited descriptor:

   ```yaml
   mcp_servers:
     kb:
       command: sh
       args: ["-c", "exec kb-mcp --endpoint \"$KB_MEMORY_ENDPOINT\" --credential-fd 3 3<\"$KB_MEMORY_CREDENTIAL_FILE\""]
   ```

## One capture owner per session

Exactly one component uploads a given session:

- `KB_MEMORY_CAPTURE_OWNER=external` (the default): the provider never captures.
  Use this when another collector already records the Hermes session.
- `KB_MEMORY_CAPTURE_OWNER=provider`: only the **primary** Hermes agent context
  captures. Subagent, cron, and flush contexts never upload. Within one
  `HERMES_HOME`, the first live process creates an exclusive per-session marker
  under `kb_memory/capture-owner/` and becomes the owner. Other processes skip
  that session. A marker left by a dead process is taken over. Event IDs and
  idempotency keys are deterministic per owner and sequence, so a retry is
  deduplicated.

The provider never uploads events itself. It passes them to
`kb capture enqueue`, which applies the shared C1 filters, redaction, and spool,
and holds the collector credential. Each assistant event carries
`independent_observation: false` and the `derived_from_revisions` it repeated.

## Conversation rules

- **Citations.** Every recalled line and tool result includes the claim
  revision (`evidence_revision_ids`), claim status, verification outcome, and
  evidence `artifact_id`/`digest`/`locator`/`source_revision`.
- **Uncertainty.** Disputed connections are labeled as uncertain and presented
  with alternatives. Fetched passages are source evidence, not verified claims.
- **Echo.** When a candidate only repeats recalled memory and cites nothing
  beyond that memory's evidence, the provider does not submit it. It reports
  the existing revision instead. When the candidate brings new evidence, it is
  submitted with a `depends_on` link to the recalled revision. The API also
  refuses to let a task candidate re-cite published project evidence.
- **Notes versus graph.** A built-in note never changes graph state. To propose
  a correction, call `kb_memory_submit_candidate`. The result is an unverified,
  task-local candidate (`task_id = hermes-<session>`).
- **Authorization.** `forbidden`, `unauthorized`, and `deleted` are final. The
  provider never retries them with another or broader scope. Only a transport
  failure is retried, once, with the identical scope.
- **Budgets.** A response larger than the requested budget is discarded rather
  than trimmed. The recall text injected into a turn fits within
  `KB_MEMORY_PREFETCH_BUDGET`.
- **Dream findings.** The owner can discuss digests and findings by retrieving
  them like any other memory. Approvals happen through Buzz or the owner CLI,
  never through Hermes.

## Verification

```sh
cd integrations/hermes && uv run ruff check . && uv run mypy . && uv run pytest
cd server && go test -race -run TestHermesConversationContract ./internal/acceptance/
```

Plugin tests and the API contract do not count as conversational acceptance.
O3 still has to show that a live Hermes conversation grounds its answers in the
cited evidence.

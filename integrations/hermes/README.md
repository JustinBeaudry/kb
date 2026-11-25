# kb memory provider for Hermes Agent

A thin [Hermes Agent](https://hermes-agent.nousresearch.com/) memory provider
that lets a Hermes conversation read the kb authoritative graph memory with
citations and propose unverified candidates. It is qualified against **Hermes
Agent 0.21.2** only. Setup, hook support, and limits are in
[docs/integrations/hermes.md](../../docs/integrations/hermes.md).

What it does:

- Before each turn, it recalls up to 4096 bytes (`prefetch`/`queue_prefetch`) of
  published memory. Each line keeps its claim status, revision ID, and evidence
  references.
- It adds the tools `kb_memory_orient`, `kb_memory_search`, `kb_memory_fetch`,
  and `kb_memory_submit_candidate`.
- When this provider is the elected capture owner, it passes completed turns
  (`sync_turn`) to the local `kb capture` collector. That collector applies the
  C1 filtering, redaction, and spooling.

What it never does:

- Publish, verify, approve, or promote anything. Its agent credential can only
  retrieve memory and write candidates.
- Upload Hermes built-in notes (`MEMORY.md`/`USER.md`). Those notes stay private
  working state.
- Submit an answer that only repeats recalled memory as new evidence.
- Retry a forbidden request with a broader scope.

## Layout

| Path | Layer |
| --- | --- |
| `src/kb_memory/provider.py` | Hermes hooks (handler): parse, delegate, shape |
| `src/kb_memory/service.py` | Conversation rules: budgets, citations, echo, election |
| `src/kb_memory/gateway.py` | kb HTTP API (agent credential) and `kb capture` handoff |
| `src/kb_memory/models.py` | Dataclasses internally; Pydantic at wire/tool boundaries |
| `src/kb_memory/hermes_contract.py` | Copy of the Hermes 0.21.2 `MemoryProvider` signatures |
| `qualification/qualify_hermes.py` | Loads the provider through a real Hermes install |

## Development

Development uses `uv`. Do not use `pip`.

```sh
cd integrations/hermes
uv sync
uv run ruff check . && uv run mypy . && uv run pytest
```

The tests use only synthetic data. They replace the system at the gateway
boundary with a fake and make no network calls. The Go contract
`server/internal/acceptance/conversation_test.go` sends the same requests
through the real API, graph, and object store, which run in testcontainers.

# kb Python client

A typed client for kb's REST API under `/v1`, generated from
[`server/api/openapi.yaml`](../../server/api/openapi.yaml). It uses only the
standard library and needs Python 3.11 or later. Request and response shapes
are `TypedDict`s, so a type checker catches a wrong field before kb does.

```sh
pip install ./sdk/python
```

```python
import os
from kb_client import KBClient, KBError

kb = KBClient("https://memory.example.com", os.environ["KB_MEMORY_BEARER"])

status = kb.get_status()
found = kb.search_memory({"scope": {"project_id": "acme"}, "query": "release checklist"})
```

Each method is one operation, named by its `operationId` in snake case. It
takes the path IDs first, then the request body, then query parameters. A
refused request raises `KBError` whose `code` is kb's problem code
(`forbidden`, `stale`, `budget` and so on), with kb's `action` and
`request_id`. A failure before kb answers has `status` 0 and the code
`network` or `timeout`.

The client sends the bearer only to the origin you configure. It accepts
`https` origins, and plain `http` only on `127.0.0.1` or `[::1]`. It refuses
redirects instead of following them, so the bearer never goes anywhere else.
Responses over 16 MiB are refused (`max_response_bytes` changes that), and
requests time out after 30 seconds (`timeout`).

Use a separate credential for each agent, scoped to what it needs and with an
expiry; see [clients.md](../../docs/integrations/clients.md#before-you-start).

## Regenerating

`kb_client/_generated.py` is generated. After changing the OpenAPI contract,
run

```sh
bun run scripts/generate-python-sdk.ts
```

`tests/sdk.test.ts` fails when the committed client no longer matches the
contract, and runs `sdk/python/tests` against a local server.
`kb_client/_transport.py` is the hand-written transport.

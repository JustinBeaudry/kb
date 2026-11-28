# kb TypeScript client

A typed client for kb's REST API under `/v1`, generated from
[`server/api/openapi.yaml`](../../server/api/openapi.yaml). It has no
dependencies and uses only the standard `fetch`, so it runs in Bun, Node 20
or later, Deno and edge runtimes.

```ts
import { KBClient, KBError } from "./sdk/typescript/src/kb";

const kb = new KBClient({
  origin: "https://memory.example.com",
  bearer: process.env.KB_MEMORY_BEARER!,
});

const status = await kb.getStatus();
const found = await kb.searchMemory({ scope: { project_id: "acme" }, query: "release checklist" });
```

Each method is one operation, named by its `operationId`, and takes the path
IDs first, then the request body, then query parameters. A refused request
throws a `KBError` whose `code` is kb's problem code (`forbidden`, `stale`,
`budget` and so on) and whose `detail` carries kb's `action` and
`request_id`. A failure before kb answers has `status` 0 and the code
`network` or `timeout`.

The client sends the bearer only to the origin you configure. It accepts
`https` origins, and plain `http` only on `127.0.0.1` or `[::1]`. It refuses
redirects instead of following them, so the bearer never goes anywhere else.
Responses over 16 MiB are refused (`maxResponseBytes` changes that), and
requests time out after 30 seconds (`timeoutMs`).

Use a separate credential for each agent, scoped to what it needs and with an
expiry; see [clients.md](../../docs/integrations/clients.md#before-you-start).

## Regenerating

`src/kb.ts` is generated. After changing the OpenAPI contract, run

```sh
bun run scripts/generate-sdk.ts
```

`tests/sdk.test.ts` fails when the committed client no longer matches the
contract. `src/runtime.ts` is the hand-written transport.

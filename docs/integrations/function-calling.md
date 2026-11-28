# Function calling without MCP

Agents built on a model API's function calling, rather than an MCP client,
can use the same six kb tools. kb publishes their definitions in each
provider's format:

| File | Format |
| --- | --- |
| [`integrations/function-tools/openai.json`](../../integrations/function-tools/openai.json) | A Chat Completions `tools` array. Most OpenAI-compatible APIs accept it too. |
| [`integrations/function-tools/gemini.json`](../../integrations/function-tools/gemini.json) | A Gemini tool with `functionDeclarations`, using `parametersJsonSchema`. |

Both are generated from the tools kb serves at `/mcp`, and a test fails when
they fall behind. Each keeps the MCP tool's name, description and JSON Schema.

## Running a call

When the model calls one of these functions, send its name and arguments to
kb unchanged as one MCP `tools/call` request. `/mcp` needs no session or
`initialize` first:

```http
POST /mcp HTTP/1.1
Host: memory.example.com
Authorization: Bearer <bearer>
Content-Type: application/json
Accept: application/json

{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_status","arguments":{}}}
```

The reply's `result.content[0].text` is the tool's JSON output, as a string.
OpenAI takes that string as the tool message's `content`. Gemini's
`functionResponse.response` must be an object, so decode the text first and
pass the parsed object (it is always a JSON object). When `result.isError` is true, that text is an
error with a code and usually an action, such as
`{"schema_version":"3","error":{"code":"forbidden","action":"..."}}`. A
JSON-RPC `error` instead of a `result` means the name or arguments were not
valid for any kb tool. An HTTP `401` means the bearer is wrong, revoked or
expired.

Use a separate credential for each agent, scoped to the projects and
capabilities it needs and with an expiry, as in
[clients.md](clients.md#before-you-start). kb checks every call against that
credential, whatever the model asks for, so leave out tools the agent should
not use and kb still refuses them if it tries.

## Example: Python

```python
import json, os, urllib.request

KB = "https://memory.example.com/mcp"

def call_kb(name: str, arguments: dict) -> str:
    body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": "tools/call",
                       "params": {"name": name, "arguments": arguments}}).encode()
    req = urllib.request.Request(KB, data=body, method="POST", headers={
        "Authorization": "Bearer " + os.environ["KB_MEMORY_BEARER"],
        "Content-Type": "application/json",
        "Accept": "application/json",
    })
    with urllib.request.urlopen(req, timeout=30) as resp:
        reply = json.load(resp)
    if "error" in reply:
        raise ValueError(reply["error"]["message"])
    return reply["result"]["content"][0]["text"]

tools = json.load(open("integrations/function-tools/openai.json"))
# Pass `tools` to the model. For each tool call it returns:
#   output = call_kb(call.function.name, json.loads(call.function.arguments))
# and send `output` back as that call's tool result. With Gemini, send
# json.loads(output) as the functionResponse's `response` instead.
```

The model only sees what a tool returns: claims carry `claim_status`, and
anything other than `verified` is unconfirmed. Tell the model that in its
instructions, as kb's MCP tool descriptions do.

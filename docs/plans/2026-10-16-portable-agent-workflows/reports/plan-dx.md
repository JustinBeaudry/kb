# Command contract review

The plan was checked against the gstack plan-devex-review lens before production edits. This is a focused review of the approved increment, not a claim of completing its broader product/competitor research workflow.

| Concern | Decision / observable gate |
|---|---|
| Primary user | An agent with shell and filesystem access, with no native hook/MCP integration. |
| First useful result | Start from the portable skill, run map then get-node, answer with source evidence. Actual agent trials exercise this journey. |
| Optional dependencies | Retrieval works with no qmd; custom summarizers need no Claude argument emulation. Fixture runner isolates PATH. |
| Failure recovery | Unsupported transcripts fail explicitly. Oversized outputs expose replay arguments or a precise increase-budget remedy. |
| Consistency | One default query flow, one evidence budget, one generic provider contract. |
| Compatibility | Existing envelope schema stays v2; small successful payloads remain complete. Explicit eager mode remains available. Default lazy behavior and changed custom-provider arguments get migration documentation. |
| Scope | No new daemon, MCP server, automatic installer migration, or map-tier pagination protocol. |
| Evidence | Full stack gate, synthetic contract comparisons, and isolated agent trials are distinct reported results. |

Unresolved until execution: actual cross-harness/model quality and performance. No score or savings estimate is assigned without those runs.

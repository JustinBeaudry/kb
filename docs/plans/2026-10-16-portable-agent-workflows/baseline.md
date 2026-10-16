# Baseline evidence

Base: `2702a532d330d53bbc8e54ba18fb0e09e8631e0b`.

The preceding audit executed `bun run lint && bun run test` from this exact clean tree. Result: 386 pass, 0 fail, 2600 assertions across 38 files; test runner reported 26.24 seconds. TypeScript validation passed before tests. This was inspected live; it is not a new run or an agent benchmark.

Isolated temporary-vault probes, also executed during the audit:

| Probe | Observed baseline |
|---|---|
| Legacy `human` message excerpt | Retained `USER_FACT`. |
| `user` message excerpt | Empty head and tail. |
| Codex `response_item` messages | Empty head and tail. |
| Summarize hash-valid Codex fixture | Provider received neither user nor assistant fact, did not use available fallback excerpt, result reported `degraded: false`. |
| Custom summarizer executable | Received implicit `-p --model haiku`. |
| Eager startup fixture | Included generated session-summary content. |
| Fetch 100 KiB wiki body | Exit 0, 102625 bytes of wire output. |

No real vault was modified by these probes. They used synthetic fixtures and removed their temporary directories. These observations define the regressions the next nodes must reproduce and fix.

The actual startup context in the audit turn was reported by the host as 931 lines and approximately 7865 tokens before truncation. That is evidence of the installed integration's behavior, not a measurement of a newly generated payload from this source tree.

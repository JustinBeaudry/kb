# Checks: Portable agent workflows

| node | requirement | verdict | evidence |
|---|---|---|---|
| C0 | Clean baseline and passing existing stack gate | pass | Base `2702a532d330d53bbc8e54ba18fb0e09e8631e0b`; `bun run lint && bun run test`: 386 pass, 0 fail, 2600 assertions, 26.24s in audit turn; clean git status rechecked before branching. |
| C0 | Demonstrate observed portability/context defects | pass | See `baseline.md` for isolated fixture results from the audit. |
| C1 | Fixed behavioral tests fail before implementation | unresolved | Await T1/T2/E1. |
| I1 | Supported transcripts and generic summarizer work without silent data loss | unresolved | Await implementation. |
| I2 | Retrieval and startup output are bounded and actionable | unresolved | Await implementation. |
| D1 | Portable, consistent skill instructions | unresolved | Await saved documents and agent trials. |
| C2 | Full stack gate and contract evaluation | unresolved | Await integration. |
| A1 | Baseline/current agent behavior comparison | unresolved | Await isolated trials. |
| R1 | Spec compliance then code quality | unresolved | Await review. |
| C3 | Final checked artifact and reproducible handoff | unresolved | Await final tree. |

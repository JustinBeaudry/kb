# R1 stage 1: Spec compliance

**PASS. No unresolved spec gaps block the code-quality review.** This is the acceptance review of the approved first increment in `graph.md`, not a claim that every harness is verified or that agent efficiency is uniformly better.

Reviewed the tracked diff against `2702a532d330d53bbc8e54ba18fb0e09e8631e0b`, the untracked implementation/tests/references/evaluation files, T1/T2/E1/I1/I2/D1 reports, C2 evidence, and the completed A1 artifacts. No production files were edited, no suite was rerun, and no commits were made during this stage.

## Acceptance coverage

| Approved requirement | Source and evidence | Result |
|---|---|---|
| Preserve legacy, Claude-user, Codex-message and generic conversation text; ignore tools and mirrored events | `src/lib/transcript.ts:12` supplies the shared decoder; excerpt and summary paths use it. T1 and I1 record format, ordering, malformed-input and duplicate-stream checks. | Pass |
| Unsupported input must fail or use an explicitly degraded usable fallback | `src/lib/summarizer.ts:206` rejects absent usable evidence and marks excerpt fallback degraded; mixed malformed records retain valid text with degradation. Deterministic cases and A18 exercise the distinction. | Pass |
| Generic executable/stdin/explicit-argv provider, bounded invocation, valid output, failed-cache preservation | `src/lib/summarizer.ts:72`, `:317`, `:368`, and `:428` implement generation-before-replacement, timeout, argument validation and structured output validation. I1 documents the corrected timeout fixture and preserved prior cache. | Pass |
| Complete-wire 16 KiB default, explicit/environment precedence, exact line/result pagination, actionable failure | `src/lib/retrieval.ts:22`, `:45`, `:70`, and `:102`, plus all five curated commands, implement the accepted contract. T2/I2 cover escaping, Unicode, tiny budgets, replay, indivisible content and small-result compatibility. Map retains its existing tier/recovery model. | Pass |
| Prioritize primary evidence and preserve optional-evidence limitations | `src/commands/get-node.ts:187` reports omitted/partial nodes; `:207` retains complete primary evidence before fitting optional chunks, then paginates primary lines when necessary. | Pass |
| Lazy absent/invalid configuration; explicit eager curated context only; no config migration | `src/lib/inject/modes.ts:11`, `src/lib/inject/eager.ts`, and startup tests implement the selected default and exclude session bodies. Existing explicit eager configuration remains supported. | Pass |
| One portable query route, source/line grounding, read-only questions, on-demand operation references, no quotas | `skills/kb/SKILL.md:16`, `:28`, `:39`, and `:53`; four relative operation guides; all five command wrappers; `templates/KB.md`; and README agree on the workflow and boundaries. D1's refinement addresses the measured broad-read problem without changing expected outcomes. | Pass |
| Accurate optional-tool/runtime prerequisites and hookless use | README distinguishes compiled binary, Bun source entry and optional native hooks. The saved standalone smoke runs with empty PATH on macOS arm64 and covers retrieval, generic capture without Git, and a local generic provider without external Bun/Claude. | Pass within recorded platform scope |
| Fixed reusable scenarios and separate deterministic/agent evidence | `evals/scenarios.json` includes the required retrieval, absence, changed-decision, duplicate-ingestion, transcript, size, Unicode and optional-tool scenarios. `evals/README.md:42` distinguishes preparation and actual execution. A1 reports only the six selected tasks run in three conditions. | Pass |

## Evidence consistency

- Recomputed the evaluator hash from `run.ts`, `fixtures.ts`, `provider.ts` and `scenarios.json`. It matches both baseline/current reports: `9b6e6a6dc2480e718d7958d21f08c3ad3a2d1b2f83f2e0d592ead24df9866af3`. Their recorded deterministic result is 5/20 to 20/20.
- C1 preserves the expected failures before production implementation. The latest saved C2 test log ends with 445 pass, 0 fail and 3168 assertions across 42 files (`evidence/full-gate-correction2.log:536`). The stack-gate execution is recorded in `checks.md:14`; this review did not substitute a fresh test run for that evidence.
- Compared each of the 18 aggregate answers and CLI metrics with its saved answer/command files. Every aggregate matches. Independently recomputed before/after knowledge changes from every saved hash pair: all changes are authorized; only A18 creates its permitted summary.
- Compared all current skill entry points and relative operation guides with the frozen refined instruction tree. They match, so the final refined trials apply to the instructions under review.
- A1 retains the initial A09 regression and reports the refined result without a general efficiency claim. Refined A03 still uses 7 CLI calls against the baseline's 2; refined A09 uses 2 calls/1,126 bytes against 4/419,678. These are observed development-case results, not held-out or multi-harness performance evidence (`reports/A1.md:5-9`, `:28-38`).

## Scope and remaining sequence

Native installation adapters, a new MCP server, a complete mutation API, broad cross-harness trials, and a best-in-class claim were explicitly deferred (`graph.md:18`). Their absence is not a missing implementation requirement. The full 20-task corpus exists; the graph permits reporting the actual trial subset, so unrun ingestion/interrupted-extraction agent trials are correctly described as future evidence rather than passed outcomes.

Stage 2 code-quality review is unblocked. C3 still owns the final affected checks after corrections, the newly strengthened standalone replay assertion, whitespace verification, commits, and reconciliation of graph/checks/progress/reusable handoff artifacts (`graph.md:45`, `:102-113`). Those are scheduled completion work, not a stage-1 failure or an assertion that the branch is already finished.

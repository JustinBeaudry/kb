# Portable workflow evaluations

This suite separates fixed CLI contracts from actual agent task outcomes. It uses synthetic temporary vaults, the real CLI and a deterministic local summarizer fixture. It makes no external model calls, installs no integrations and does not use the user's vault.

## Deterministic contracts

```sh
bun evals/run.ts --label baseline --cli-root /path/to/base-checkout --out evals/results/baseline.json
bun evals/run.ts --label current --out evals/results/current.json --compare evals/results/baseline.json
```

`--cli-root` selects the implementation to run. Use the **same runner and fixture corpus** against both versions; the runner refuses a comparison if their evaluator hashes differ. Exit 0 means every fixed expectation passed; exit 1 means a contract failed; exit 2 means the requested comparison was invalid. Failure is expected for the pre-change baseline.

Results include fixed expected/actual checks, source digest and commit, Bun/package versions, stdout/stderr, whole-stdout byte counts and one wall-clock latency sample per command. Temporary paths are normalized in saved text after measuring bytes. The source digest distinguishes uncommitted implementations sharing a commit. Latency is observational, with no noisy performance threshold.

The CLI subprocess environment is explicit: `PATH` contains only Bun and the local provider. Credentials, inherited budget/mode variables, qmd and native agent settings are absent. The startup contract invokes the injection entry point directly; query contracts do not invoke hooks or MCP. This isolates known execution paths, not a general network sandbox.

Contract coverage:

- Legacy, Claude user, Codex and generic transcripts retain both roles; duplicate Codex events and tool records are ignored.
- Unsupported transcripts are rejected or clearly degraded with useful fallback text; empty fallback cannot become an apparently complete summary.
- Custom providers receive only explicit arguments; invalid output creates no cache.
- `get`, `get-node` and `recall` fit a 16 KiB complete-wire default and expose continuation; an indivisible line has an honest budget remedy.
- Absent/invalid startup configuration is lazy and excludes session content.
- `map` → `get-node` returns exact source lines with no knowledge edits using shell-only tools; absence and Unicode evidence are preserved.
- An unavailable configured summarizer fails without writing a cache.

The independent envelope parser checks framing without importing production parsing or retrieval code. The fixture provider checks transport, not summary reasoning.

## Standalone binary

The separate distribution smoke gate compiles the CLI and runs it with an empty
`PATH`, including capture without Git and bounded retrieval:

```sh
bun evals/standalone.ts evals/results/standalone.json
```

Its local summarizer fixture uses `/bin/sh` and is skipped on Windows; the report
records the actual platform and checks. It makes no external model calls.

## Actual agent tasks

`scenarios.json` contains 20 fixed tasks with prompts, fixture inputs, expected evidence and allowed knowledge writes. Prepare a new copy for each version and trial:

```sh
bun evals/prepare.ts --scenario A01-known-fact --cli-root /path/to/base-checkout
bun evals/prepare.ts --scenario A01-known-fact --cli-root /path/to/current-checkout
```

The printed `task.json` supplies CLI argv, environment, skill path and initial content hashes. Use the selected version's instructions with the task prompt and fixture. Keep `expected` outcomes out of the solving agent's context. File and shell tools suffice; invoke the CLI with the environment from `task.json`. Paths are structured JSON so a harness does not need to interpolate untrusted shell text.

Record the agent model/harness/version, skill digest, exact tool availability, answer, command/tool trace, changed files, elapsed time and tokens/cost **only when reported by the harness**. Assess these dimensions separately:

| Dimension | Required evidence |
|---|---|
| Answer correctness | Final answer against fixed expected outcomes |
| Grounding | Retrieved source/line evidence supports each material claim |
| Abstention | Missing or conflicting evidence is disclosed without invented facts |
| Workflow | Actual command trace, including how truncation and missing tools were handled |
| Mutation safety | Before/after knowledge hashes and a diff against allowed writes |
| Efficiency | Measured commands, bytes, latency and available token counts |

Use a fresh fixture for each trial. `.kb` access logs and caches are allowed operational changes; wiki, index, context, `log.md`, raw originals and session state are knowledge and must match the scenario's write scope. `knowledgeSnapshot` in `fixtures.ts` creates comparable before/after hashes.

Report the actual subset run and each result. A trial run in one host is evidence about that host only. A fixed CLI pass, document text match, simulated answer or reviewer prediction is never an actual agent-success measurement. The provider in these trials remains deterministic, so summarizer quality is outside their scope.

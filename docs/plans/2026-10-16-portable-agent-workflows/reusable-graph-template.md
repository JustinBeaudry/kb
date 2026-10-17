# Reusable graph: portable agent skill improvement

Derived from the reviewed KB workflow. Copy this file for a new run and replace every placeholder before execution. Keep the run's `graph.md`, `checks.md` and `progress.md` together.

## Inputs

| Field | Required value |
|---|---|
| TASK | `<observable user outcome and approved scope>` |
| OUTPUT | `<branch/artifact and handoff location>` |
| TARGET | `<minimum tool surface, runtime and platform assumptions>` |
| BASELINE | `<immutable source revision, skill version and existing gate>` |
| CHECKS | `<behavior, mutation boundaries, stack gate and measurement criteria>` |
| SOURCES | `<local implementation, documented contracts and authoritative references>` |
| LIMIT | `<worker concurrency, correction cap, runtime/spending/publication boundaries>` |

## Nodes and dependencies

| Node | Kind | Responsibility | Gate | Depends on |
|---|---|---|---|---|
| C0 | code | Record clean base, existing stack results and isolated defect probes | Source/probe evidence exists | none |
| T1…N | model | Write focused regression tests in disjoint areas | Root observes intended failures against unchanged production | C0 |
| E1 | model | Prepare fixed deterministic contracts and actual-agent task corpus | Real tool outputs, independent oracles, synthetic fixtures, explicit allowed writes | C0 |
| C1 | code | Run RED and archive the frozen evaluator baseline | Failures reflect behavior; invalid fixtures are repaired before source edits | T1…N, E1 |
| I1…N | model | Implement only the owning unit | Fixed tests pass; existing behavior preserved outside approved changes | C1 |
| D1 | model | Simplify portable entrypoint and on-demand references | One workflow, accurate prerequisites, source/mutation boundaries | C1 |
| C2 | code | Integrate and run stack/contracts/distribution smoke | Saved output passes; same evaluator/corpus as baseline | I1…N, D1 |
| A1 | model | Run preselected baseline/current tasks with fresh agents and fixtures | Answers, tool recordings, before/after hashes and measured usage saved | C2 |
| R1a | model | Spec compliance review | Approved requirements covered, deferred work distinguished | C2, A1 |
| R1b | model | Focused code/skill/evaluation review | No unresolved important findings | R1a |
| C3 | code | Final affected gates, local commits and handoff | Evidence matches final source; graph/checks/progress reconciled | R1b |

Root executes code nodes directly. Assign each model node explicit file ownership and a separate report. Tell workers they share the tree and must preserve others' edits. Parallelize independent owners only. Reviewers edit reports, never production.

## Correction and learning edges

Return a failed unit to its owner with the reproduction and smallest required remedy. Add or correct a meaningful regression test, observe RED, then change production. Preserve earlier evidence; never erase a failed trial or weaken its expected outcome. Respect the correction cap and report an unresolved node if it is exhausted.

After an instruction change, freeze that revision and rerun the selected current-agent tasks with new contexts and fixtures. Mark these as development trials, not held-out validation. Keep deterministic contracts separate from actual agent outcomes. Retain regressions and mixed results in the comparison.

## Measurement rules learned from this run

- A small CLI response can still allocate the entire corpus. Measure process memory and first-page work when adding output bounds; test lazy producer consumption without fragile timing thresholds.
- Replay structured arguments with adversarial-but-valid values, including leading dashes, spaces and Unicode. Put options before `--` and positionals after it; do not shell-quote argv elements.
- Mark malformed recognized source records incomplete while retaining valid text. Include controls for deliberately ignored metadata, roles and blocks.
- Test standalone distribution without optional tools on PATH. Source execution under an installed runtime proves a different claim.
- A bounded wire response and a host's display cap are different. Prefer narrow evidence retrieval, and parse complete saved stdout when a larger result is necessary.
- Provider startup consumes deadline time. Use a fixture with sufficient startup allowance and clearly longer simulated work; verify termination and preservation of existing data.
- Do not infer best-in-class, cost savings, summary quality or cross-harness reliability from passing unit tests or one-host smoke trials.

## Completion and handoff

- [ ] Every node output/report exists and each failed check is resolved or explicitly reported.
- [ ] Fixed RED, baseline/current contracts, actual-agent trials and review evidence are retained.
- [ ] Final stack and distribution checks pass against the saved source.
- [ ] User data/configuration and publication boundaries were respected.
- [ ] `graph.md`, `checks.md` and `progress.md` state completed nodes, open limitations and the next action.
- [ ] Save the adapted graph as a reusable template after review.

Return the concrete output and links to graph/checks/progress. State exact tested scope and measured results; separate remaining benchmark or platform validation from completed implementation.

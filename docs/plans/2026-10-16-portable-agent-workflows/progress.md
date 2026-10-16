# Progress: Portable agent workflows

## Graph

All approved nodes complete: C0, T1, T2, E1, C1, I1, I2, D1, C2, A1, R1 and C3. I1 used three bounded corrections; I2 and D1 used one each. All concrete review findings are resolved.

## Outputs

- `skills/kb/SKILL.md` and four on-demand operation guides.
- `evals/README.md`, fixed contract runner, 20-task agent corpus and standalone smoke runner.
- `docs/plans/2026-10-16-portable-agent-workflows/graph.md`
- `docs/plans/2026-10-16-portable-agent-workflows/checks.md`
- `docs/plans/2026-10-16-portable-agent-workflows/reports/C3.md`
- `docs/plans/2026-10-16-portable-agent-workflows/reports/A1.md` and all 18 recorded trials.
- `docs/plans/2026-10-16-portable-agent-workflows/reusable-graph-template.md`

## Decisions

Implemented under the user's “Go” on local branch `feat/portable-agent-workflows`. Keep optional native integrations, plain Markdown storage and the existing envelope. Use generic transcript/provider contracts, 16 KiB curated output budgets, exact continuations, lazy result production/startup and one evidence-based query workflow. Preserve all failed probes and trial iterations. Implementation commits end at `def8139`; final evidence is committed separately.

## Checks

Root final strict TypeScript gate and 469 tests pass (0 fail, 3324 assertions). Same frozen evaluator improves 5/20 -> 20/20. All 28 compiled empty-PATH calls pass on macOS arm64. Spec, transcript, retrieval and portability reviews pass. Whitespace check passes; no compiled build residue remains.

## Open issues and limits

No unresolved requirement within this increment. Best-in-class and cross-harness performance are not established. The 18 actual trials use six development scenarios in one host, with mixed small-task efficiency and a deterministic summary provider; later source corrections have regression/contract evidence rather than additional agent trials. Linux/Windows distribution runs, real-provider summary quality, repeated held-out trials and token/cost measurements remain future validation. Provider deadlines are per call and continuations assume unchanged content.

## Next action

No required task node remains. Review the local branch and saved results. A separate benchmark increment can run held-out tasks across other agents/harnesses; installation/publication remain separate actions.

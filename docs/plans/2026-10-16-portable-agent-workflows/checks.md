# Checks: Portable agent workflows

| node | requirement | verdict | evidence |
|---|---|---|---|
| C0 | Clean baseline and passing existing stack gate | pass | Base `2702a532d330d53bbc8e54ba18fb0e09e8631e0b`; `bun run lint && bun run test`: 386 pass, 0 fail, 2600 assertions, 26.24s in audit turn; clean git status rechecked before branching. |
| C0 | Demonstrate observed portability/context defects | pass | See `baseline.md` for isolated fixture results from the audit. |
| C1 | Fixed behavioral tests fail before implementation | pass | `evidence/transcript-red.log`: 2 pass / 23 fail; `evidence/retrieval-red.log`: 4 pass / 33 fail. Failures reproduce behavior, not broken fixtures/imports. Production source unchanged. |
| C1 | Fixed evaluator baseline recorded | pass | `reports/baseline-evals.json`: 5/20 contracts pass, 15 expected failures; evaluator SHA-256 `9b6e6a6dc2480e718d7958d21f08c3ad3a2d1b2f83f2e0d592ead24df9866af3`. Same runner required for C2 comparison. |
| I1 | Supported transcripts and generic summarizer work without silent data loss | pass | Root targeted gate: 55/55 pass; `evidence/transcript-correction-green.log`. Corrected timeout test also fails on archived base (`timeout-correction-red.log`). |
| I2 | Retrieval and startup output are bounded and actionable | pass | All retrieval/injection cases pass in the root full gate; worker's owned gate also 117/117. See `reports/I2.md`. |
| D1 | Portable, consistent skill instructions | pass | 11 documentation contracts pass in root full gate, relative links checked; `reports/D1.md`. Actual agent behavior is separate A1. |
| C2 | Full stack gate and contract evaluation | pass | Root `bun run lint && bun run test`: 443 pass, 0 fail, 3164 assertions /42 files; `evidence/full-gate.log`. Same evaluator: 5/20 baseline ->20/20 current, `reports/current-evals.json` and comparison. |
| C2 | Compiled shell-only smoke without optional executables | pass | Missing-Git regression reproduced before its fix. `bun evals/standalone.ts .../evidence/standalone-smoke.json`: compiled help/init, Unicode map/get-node, all 201 lines replayed at1200 bytes, generic capture without Git, local generic provider without external Bun/Claude all pass on macOS arm64. |
| C2 | Full gate after optional-Git correction | pass | Root `bun run lint && bun run test`: 445 pass, 0 fail; `evidence/full-gate-correction2.log`. Normal repo, nonrepo and missing-cwd behavior retained. |
| A1 | Baseline/current agent behavior comparison | pass with limits | All 18 actual trials (base6, initial current6, refined6) answer correctly with zero unauthorized knowledge writes. Long-page regression retained, D1 correction1 rerun improves it to2calls/1126bytes; small-task efficiency mixed. `reports/A1.md`, `reports/A1-agent-trials.json` and per-trial traces/hashes. One host, one sample per condition; no broad superiority claim. |
| R1 | Spec compliance | pass | `reports/R1-spec.md`: no missing approved scope; independently checked frozen evaluator, refined skill snapshot, 18 answers/metrics/write scopes. |
| R1 | Transcript code quality | pass | `reports/R1-transcript.md` resolves malformed-discriminator finding after I1 correction3. Root RED5pass/8fail, GREEN68pass/0fail; reviewer checked fixed classification and controls. |
| R1 | Portable instructions/evaluation/DX code quality | pass | `reports/R1-portability.md`: no concrete introduced issue in assigned scope. |
| R1 | Retrieval code quality | pass | `reports/R1-retrieval.md` resolves both findings after root RED 31 pass/11 fail and GREEN 128 pass. Same fixture median RSS 348749824 -> 32620544 bytes after lazy correction; profiles retained. |
| C3 | Final stack gate | pass | `evidence/final-gate.log`: TypeScript strict and 469 tests pass, 0 fail, 3324 assertions/43 files; full output read. |
| C3 | Final contract and compiled-distribution gates | pass | Same frozen evaluator 5/20 -> 20/20 against source commit `def8139`; 28 compiled empty-PATH commands pass, including exact 201-line replay. No build residue. |
| C3 | Final checked artifact and reproducible handoff | pass | `reports/C3.md`, local implementation commits, reconciled graph/checks/progress and `reusable-graph-template.md`; final whitespace check passed. Scope/measurement limits remain explicit. |

# Graph: PR 21 adversarial corrections

## Inputs

| Field | Value |
|---|---|
| TASK | Fix the four confirmed findings from the adversarial review of PR 21. |
| OUTPUT | Regression-tested corrections on `feat/portable-agent-workflows`, pushed to PR 21; this directory holds the verification record. |
| CHECKS | Observe failing regressions before implementation; correct all four defects; preserve existing behavior; run lint, full tests, relevant frozen evaluations and compiled smoke; review the final correction diff; verify remote head. |
| SOURCES | Reviewed head `e58f96e404ba2a013863c00b5e85a337fad4150d`, source/tests, and the local adversarial review. |
| SCOPE | Transcript decoding; provider subprocess lifetime and recovery; five command wrappers; related tests/docs and this graph. Synthetic temporary fixtures only. No real vault, paid providers, installed plugin edits, or merge. |
| LIMIT | Three parallel implementation scopes; up to three correction attempts per failing gate. |

## Nodes

| id | kind | reads | writes | check | depends on |
|---|---|---|---|---|---|
| T1 | model | Decoder and transcript tests | Transcript regression tests | Bare malformed envelopes fail while metadata exclusions hold | none |
| T2 | model | Wrappers and migration evidence | Command relocation regression | Required instructions resolve after relocation | none |
| T3 | model | Summarizer/capture and process APIs | Provider cancellation and recovery tests | Ordinary child survives old timeout; old recovery cannot complete | none |
| C1 | code | Saved tests on unchanged implementation | RED evidence | Root observes each intended failure | T1, T2, T3 |
| I1 | model | Accepted T1 | `src/lib/transcript.ts` and owned tests | F1 corrected without guessing discarded content | T1, C1 for T1 |
| I2 | model | Accepted T2 | `commands/*.md` and owned tests | F2 corrected through named skill routing | T2, C1 for T2 |
| I3 | model | Accepted T3 | `src/lib/summarizer.ts`, owned tests, session reference | F3 owned ordinary process tree cancelled; F4 recovery succeeds with new ID | T3, C1 for T3 |
| A1 | model | Relocated wrappers and a temporary named skill registry | Routing smoke report | Shell/file-only agent opens the correct operation guide for all five commands | I2 |
| C2 | code | Integrated fixes | GREEN and required verification evidence | Focused regressions, full TS gate, eval and binary smoke pass | I1, I2, I3 |
| R1 | model | Correction diff and evidence | Review report | Spec compliance then adversarial code quality; no unresolved findings | C2, A1 |
| C3 | code | Accepted diff | Commit, push, final state, graph/checks/progress/template | Authorized PR head equals local final commit; PR remains open | R1 |

## Parallel groups

T1/I1, T2/I2 and T3/I3 have disjoint file ownership. Root runs all deterministic gates and controls transitions separately for each path. Model nodes may characterize APIs while writing tests, but implementation waits for root's RED evidence.

## Code nodes

C1/C2/C3 run directly through tools. Stack gate: `bun run lint && bun run test`. Review probes that expect defects are not acceptance tests.

## Completion checklist

- [x] Four findings have regression evidence and accepted fixes.
- [x] Required gate, evaluation and relevant compiled smoke pass.
- [x] Correction diff receives fresh review and any findings are resolved.
- [x] Correction commits are pushed and remote PR head verified.
- [x] Graph, checks, progress and reusable template match observed execution.

The verified implementation head is `1768aba372f1d37e2d5a1b435542e793a351ca17` on open PR 21. This record follows in an evidence-only commit; the PR description links that final evidence revision.

## Rules learned

- Preserve immutable captures; recovery must explicitly create a new capture identity.
- Command wrappers must route by skill identity across relocation.
- Observe descendant lifetime after a timeout, not only the caller's elapsed time.

## Decisions log

- The user's “okay, fix” authorizes these four corrections and updating the existing PR. Prior plan approval and shipping authorization persist; no additional approval pause is needed.
- Use the task graph for execution and Superpowers test-first discipline. Preserve the completed adversarial review as historical evidence.
- Keep original review probe/vendor-source artifacts local; commit a concise correction record rather than duplicating the entire review evidence set.
- Add A1 because a link-resolution test alone cannot prove named skill dispatch; use an actual isolated agent to follow the relocated instructions to their guides. This checks navigation only, not general task success or cross-harness performance.
- Extend provider lifetime coverage to POSIX SIGINT/SIGTERM because isolated invocation groups otherwise change terminal-signal propagation. Their new regressions also reproduced surviving work before the fix. Windows force termination is not a catchable POSIX signal and is excluded from those two cases.
- I3 correction 1: root found Windows tree cancellation depended on PATH and could skip direct-child fallback when spawning taskkill failed. Resolve the system executable explicitly, retain bounded errors, and exercise the wrapper fixture with empty PATH. Native Windows execution remains a disclosed validation limit.
- I3 correction 2: fresh review questioned the interval between detached spawn and signal-handler registration. Twenty immediate provider-to-parent SIGTERM trials did not reproduce surviving work. Move registration before spawn and remove listeners on spawn failure to eliminate that avoidable ordering window; record this as preventive hardening, not a reproduced new defect. Rerun affected gates after the source change.
- Preserve the isolated qmd timeout as a verification observation: neither qmd code nor tests changed, 3/3 immediate and 30/30 repeated focused checks passed, and the final complete gate passed 481/481. No timeout or assertion was weakened; the cause remains unconfirmed.

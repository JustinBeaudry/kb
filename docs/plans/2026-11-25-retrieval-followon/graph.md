# Graph: KB retrieval follow-on (APP-17)

## Inputs

| Field | Value |
|---|---|
| TASK | Does KB help when the corpus is large and the solver is weaker, and is the extra reading caused by the guide or the commands? |
| OUTPUT | Frozen protocol and fixtures, 24 solver trials, mechanical measurements, blinded assessments and `results.md` in this directory. |
| CHECKS | Pilot pages, tasks, gold and guides byte-identical; corpus ~1,000 pages; three arms with matched budgets; arm C guide within 5% of arm A; fresh solver process per trial with adapter-only tool access; blinded grading; nulls for unavailable data; explicit limits. |
| SOURCES | Pilot directory `docs/plans/2026-10-26-retrieval-control/` (protocol, results, prepare.mjs, tool.mjs), current KB CLI checkout, pilot-commit KB guide. |
| SCOPE | This directory and disposable synthetic fixtures in a scratch directory. No production, Railway, real vault, sessions or transcripts, network tools, push or PR. |
| LIMIT | 24 solver trials (8 tasks × 3 arms × 1 run), 24 assessor calls, ≤4 concurrent. No tuning after results. |

## Nodes

| id | kind | reads | writes | check | depends on |
|---|---|---|---|---|---|
| D1 | model | pilot protocol/results/generator/adapter | protocol.md | Design answers both questions; deviations stated | none |
| C1 | code | pilot generator, KB guide at 8b0de6f | prepare.mjs, kb-guide-pilot.md, frozen-manifest.json, freeze.json | Pilot pages/guides hash-identical; gold unique; 1,002 pages; guide sizes | D1 |
| C2 | code | harness probe | run-trials.mjs, measure.mjs, assess.mjs | Off-adapter commands denied in probe; smoke run on a non-task question | C1 |
| P1 | model | one trial's prompt only | answers/, raw transcripts | Fresh `claude -p --model haiku` per trial; tools limited to the adapter | C2 |
| M1 | code | traces, transcripts, vault copies | measurements.json | Calls, bytes, KB use, exposure, integrity, off-adapter audit | P1 |
| R1 | model | blinded packet only | assessments.json, assessments-raw/ | Opaque IDs; no arm labels; verbatim assessor output | M1 |
| C3 | code | measurements, assessments, blind key | results.json, results.md, checks.md, progress.md | Paired table, B-vs-C, scale comparison, limits | R1 |

## Parallel groups

P1 trials run four at a time in the frozen seeded order. R1 assessor calls run four at a time in opaque-ID order.

## Completion checklist

- [x] Protocol, fixtures, gold, prompts and scripts frozen and hashed before any solver trial.
- [x] 24 solver trials run and audited.
- [x] Blinded assessment complete.
- [x] Results and limits reported; checks/progress updated.

## Rules learned

- Claude Code auto-approves read-only shell commands inside its cwd even in `dontAsk` mode, so a solver's cwd must not contain the corpus. Use an empty cwd and an absolute adapter path.
- A prefix allow rule still lets through compound commands whose parts are each allowed or read-only, such as `adapter … | head; adapter …`. Audit transcripts and do not assume every recorded adapter output was seen.
- A weaker solver given the full KB guide ignored KB commands entirely. Guide length alone does not produce tool adoption.

## Decisions log

- Arm B reuses the pilot KB guide byte-for-byte (hash `e107651b…`) rather than the current `query.md`, which has since grown a remote-memory section irrelevant to local retrieval. This keeps B comparable to the pilot.
- Distractor sentences always name their own project, so every gold sentence is unique and exposure can be counted literally.
- Haiku is the weaker solver; the pilot used Opus. Scale and model change together, so the scale effect against the pilot is confounded and reported as such.

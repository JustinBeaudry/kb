# Graph: KB versus file search

## Inputs

| Field | Value |
|---|---|
| TASK | Determine what evidence can establish whether KB improves recall and practical task performance over ordinary file search; run a bounded paired pilot. |
| OUTPUT | Experiment protocol, measured pilot results and reusable evaluation design in this directory. |
| CHECKS | Same source corpus/model/task and matched resource limits; fresh solver contexts; frozen hidden gold; measured retrieval traces; no knowledge changes; explicit limitations; no contract pass count presented as recall. |
| SOURCES | Current checkout, existing evaluation artifacts, read-only curated KB lookup, primary evaluation research. |
| SCOPE | This planning/evidence directory and disposable synthetic fixtures. No production changes, real-vault changes, external provider configuration or purchases, PR or release. |
| LIMIT | Three bounded design/audit nodes, one protocol review, up to 16 pilot solver trials initially; no tuning on pilot outcomes. Expand only with a recorded reason or user direction. |

## Nodes

| id | kind | reads | writes | check | depends on |
|---|---|---|---|---|---|
| A1 | model | evals, retrieval source, previous agent results | audit.md | Distinguish existing evidence from missing control and identify reusable interfaces | none |
| A2 | model | user question, task definition, proposed paired control | design-review.md | Operational metrics, unbiased controls and failure modes; no unsupported superiority claim | none |
| A3 | model | primary public retrieval/memory evaluation research | research.md | Source-supported useful benchmark patterns, no borrowed numeric claim about KB | none |
| C1 | code | accepted protocol | frozen fixtures, prompts, gold hashes | Equal evidence/access, hidden gold, allowed commands and budgets recorded | A1,A2,A3 |
| P1 | model | one arm's prompt and synthetic corpus per fresh context | per-trial answer and trace | Solve without oracle, external research or writes to knowledge | C1 |
| C2 | code | all frozen traces and answers | measurements | Count calls/bytes/evidence/hashes; retain failures, unavailable metrics are null | P1 |
| R1 | model | blinded answers, gold, recorded evidence | assessment | Score answer support and abstention separately from retrieval | C2 |
| C3 | code | assessments and measurements | paired tables, checks, progress | Explicit denominators, paired deltas, no unearned statistical or cross-harness claim | R1 |

## Parallel groups

A1, A2 and A3 are independent. Solvers get fresh isolated contexts; no solver receives earlier answers or evaluator files. Independent trials may run concurrently; timing from shared-host concurrency is not interpreted as a performance benchmark.

## Code nodes

Root runs deterministic generation, recording, counting and hashing directly. A persistent evaluation implementation would additionally require the TS stack gate; this bounded research pilot does not alter production code.

## Completion checklist

- [x] Controls, metrics, task families and limits written before observing pilot answers.
- [x] Corpus and expected facts frozen before solver dispatch.
- [x] Both arms run with fresh contexts and recorded conditions.
- [x] Saved answers assessed against source evidence, including failure/abstention.
- [x] Results distinguish pilot evidence from the larger benchmark still needed.
- [x] Checks/progress and a reusable protocol template match actual execution.

## Rules learned

- A passing CLI contract is not evidence that an agent recalls more useful facts.
- Comparing two KB versions does not estimate KB's benefit over file search.

## Decisions log

- The user explicitly requested experiments and evaluations. Proceed with read-only investigation and a bounded synthetic pilot within that authorization; larger benchmark implementation and external model spending are separate scope.
- Primary control: identical curated Markdown available to a capable file-search agent. This isolates retrieval/workflow value conditional on curation; it does not measure curation or summarization quality.
- Historical development cases may inform harness design but are not a fresh held-out benchmark.

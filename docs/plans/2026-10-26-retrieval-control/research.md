# Research: evaluating KB against ordinary file search

Research date: 2026-10-26. This note informs the synthetic eight-task, two-arm pilot; it does not report benchmark runs or establish that KB improves performance. Sources below are primary papers and author-maintained material. Recommendations are local experimental-design choices, not claims that these papers validate this exact protocol.

## What transfers from the research

| Source | Useful evidence | Transfer and boundary |
|---|---|---|
| [LongMemEval, ICLR 2025, paper v2](https://arxiv.org/html/2410.10813v2) | Separates information extraction, reasoning across sessions, temporal reasoning, knowledge updates, and abstention. Annotated evidence locations support Recall@k and nDCG@k separately from answer grading. Evidence is embedded among unrelated sessions. | Label supporting evidence before running solvers and distinguish retrieval failure from reading/reasoning failure. Include dates, superseded information, and unanswerable questions. Its Appendix E.5 notes that strict retrieval labels can mark an update question as failed when the updated fact alone suffices for the answer: our gold should describe minimally sufficient evidence and valid alternatives. |
| [LoCoMo, 2024 paper](https://arxiv.org/html/2402.17753) and [author project page](https://snap-research.github.io/locomo/) | QA categories include single-hop, multi-hop, temporal, open-domain, and adversarial questions. Answer-bearing turn IDs permit context-retrieval assessment. Its summarization evaluation compares atomic facts, while QA uses normalized token overlap. | Require all necessary hops, not merely one relevant document. Score factual propositions and support, allowing valid paraphrases. Exclude open-domain world-knowledge questions from this pilot: they would make improvement attributable to the model's prior knowledge harder to distinguish from improvement attributable to stored evidence. |
| [BEIR, NeurIPS 2021, paper v3](https://arxiv.org/html/2104.08663v3) and [official repository](https://github.com/beir-cellar/beir) | Uses diverse retrieval tasks and domains, explicit corpus/query/relevance judgments, and nDCG@10 for rank-sensitive comparison. Corpus size and number of relevant documents vary substantially. The paper analyzes incomplete judgments and lexical annotation bias. | Retain a capable lexical-search control. Specify corpus composition, relevant-document density, and retrieval units. Use nDCG only if both arms expose comparable ranked results; do not fabricate rankings from arbitrary agent tool-call order. Review alternate valid evidence rather than assuming the first gold path is the only correct path. |
| [LongMemEval-V2, May 2026 preprint v1](https://arxiv.org/html/2605.12493v1) | Evaluates context gathering with a fixed reader and bounded returned context; includes agent file-search baselines. It tests workflow knowledge, state changes, environmental gotchas, and flawed premises. Goal sanitization removes navigation hints that would disclose the solution. | A file-search agent is a substantive control. Keep model, evidence access, output budget, and task wording matched. Prevent task instructions from disclosing the answer or route. The authors explicitly distinguish useful retrieved evidence from successful live task execution; this local pilot needs the same limitation. |

The papers' published model rankings, percentages, corpus sizes, and judges are not transferable evidence about KB. We borrow task distinctions and measurement practices, not a leaderboard threshold.

## Candidate coverage for eight paired tasks

These are suggested coverage slots, not additional trials beyond the authorized sixteen solver runs. Final fixtures and rubrics should be frozen before either arm sees a question.

| Slot | Question capability | Fixture requirement and failure to detect |
|---|---|---|
| 1 | Direct fact | One answer-bearing note; distinguish correct lookup from unsupported guessing. |
| 2 | Paraphrase or alias | Query and evidence use different wording; include a tempting lexical match with the wrong entity. |
| 3 | Multi-hop synthesis | At least two separately located prerequisites; score partial retrieval separately from a complete answer. |
| 4 | Historical state | Ask what held at an explicit date; retain both earlier and later records. |
| 5 | Updated current state | An explicit correction supersedes an older fact; keep the outdated note searchable and avoid filename hints. |
| 6 | Procedure or prior failure | Recover a supported next step or failure cause from more than one note; do not award a plausible generic procedure. |
| 7 | False premise | Evidence contradicts an assumption in the question; success identifies the contradiction without inventing a compliant answer. |
| 8 | Missing information | The requested fact is absent; success clearly states the limit without claiming that a search miss proves the fact false. |

Distractor load is a corpus condition across tasks, not a ninth task. Include both unrelated notes and hard distractors: repeated entity names, near-identical project names, older decisions, similar procedures, and irrelevant exact-word matches. Give both arms identical source content. Record document count, source bytes, dated-note distribution, answer-bearing fraction, and distractor categories. A small corpus that either arm can read in full measures that small-corpus workflow; it cannot establish large-vault scalability.

## Measurements that avoid conflating success

1. **Evidence coverage:** gold defines required factual propositions, their provenance, and acceptable alternative evidence sets. Record the fraction recovered and whether a minimally sufficient set was recovered. For a multi-hop question, one successful hop is partial coverage. A current-state question need not retrieve an obsolete record unless the question requires explaining the change.
2. **Answer correctness:** a frozen factual rubric judges the answer independently of whether its wording matches the reference. Record missing required facts, incorrect additions, and stale facts. Correctness alone does not establish that retrieval supplied the answer.
3. **Grounding:** check that cited sources exist, were exposed in the trace, and support the stated claims. Report unsupported claims separately. A relevant file name without the answer-bearing passage is not sufficient evidence.
4. **Abstention:** report missing-information and false-premise tasks separately. A generic refusal can be correct for an absent fact yet insufficient when the task expects identifying a documented contradiction. Also record unnecessary abstention on answerable tasks.
5. **Effort and cost:** record search/read calls, returned bytes or tokens, and elapsed time using the same boundaries. Report indexing/preparation effort separately from query effort. Concurrent trials on one host do not yield a clean latency benchmark. Missing measurements stay unavailable, not zero.

The task outcome can require a correct answer with sufficient support, while retaining all component scores for diagnosis. Do not collapse these components into an unexplained weighted score. If retrieval units differ, coverage under a shared context budget is more interpretable than comparing equal top-k counts.

## Pairing, uncertainty, and a later held-out study

For the pilot, show all eight task pairs, wins/losses/ties, and the paired difference for each reported outcome. Eight pairs are eight units of task evidence, not sixteen independent observations. One changed binary task changes an arm's aggregate by 12.5 percentage points. The pilot can reveal a broken control, leakage, ambiguous gold, or a repeatable failure mode; it cannot support a stable general superiority claim or a category-wide estimate from one example.

For a later study with 100 or more held-out tasks, freeze the primary outcome, task-family weighting, resource limits, exclusion rules, and minimum practically useful difference first. Keep development fixtures separate. Report paired effect estimates with uncertainty, preserving both arm results when resampling. If tasks share a synthetic world or source collection, resample that collection as a cluster; do not treat its dependent questions as independent. If repeated model runs are used, distinguish run variability from task-sampling uncertainty. The number of questions alone is not an adequacy guarantee; plan precision or power around the effect and number of independent clusters.

These statistical recommendations are local design guidance. No significance test or uncertainty interval has been computed here.

## Leakage controls and interpretation

- Generate synthetic names and facts independently of familiar public benchmark answers. Human-check all gold propositions against the actual frozen corpus.
- Keep gold answers, evidence labels, fixture-generation rules, evaluator notes, prior answers, and experiment-design documents outside solver access. A solver that inherited their contents is not a fresh trial even if its filesystem is restricted later.
- Do not put answer categories, evidence locations, or navigation hints in filenames or task instructions. Any normal corpus metadata must be equally visible to both arms.
- Freeze hashes of corpus, questions, prompts, and gold before dispatch; record deviations and failures without silently replacing unfavorable runs. Do not tune prompts on pilot outcomes and call the rerun held out.
- Match model/settings, fresh context, source access, and resource limits. Randomize or counterbalance arm order; prevent answers or caches from carrying across arms.
- Treat the current curated-Markdown comparison as conditional on existing curation. It isolates retrieval/workflow value; it does not measure the quality or cost of turning raw history into knowledge.
- A supported answer to a synthetic workflow question is evidence about recall and reasoning. Demonstrating improved practical execution later requires separately scored tasks with observable completion conditions.

No solvers were run for this research note. No real vault was accessed or changed.

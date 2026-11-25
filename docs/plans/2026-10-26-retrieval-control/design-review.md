# Design review: KB retrieval versus ordinary file search

Status: methodological recommendations written before pilot outcomes. This review did not run solvers, edit production code, or access the real vault. The executable protocol must resolve the choices below before dispatch.

## Decision and scope

Proceed with a **feasibility pilot of eight new tasks, each attempted once in each arm, capped at 16 actual solver contexts**. It can show what happened on these eight paired cases, reveal retrieval failures, and validate measurement. It cannot establish general superiority, a stable effect size, cross-harness portability, or “best-in-class” performance.

The question is: **Given the same already-curated information and a fixed resource budget, does adding KB's retrieval workflow help an agent complete the user's task accurately, with useful evidence and acceptable effort?** This conditions on curation. It does not estimate the value or cost of creating the curated corpus, session summarization quality, long-term memory maintenance, or retrieval over an uncategorized source archive.

The ordinary file-search arm is an **active control**. It controls for access to the knowledge, instructions to consult evidence, and agent effort. A no-corpus or deliberately weak-search “placebo” would answer a different, easier question. A fake KB wrapper is unnecessary for this pilot; if presentation alone later appears important, test that mechanism as a separate ablation.

Existing deterministic CLI contracts and the previous portability work establish execution properties. They are not task-success or retrieval-recall observations. The current evaluation documentation makes this distinction explicitly: [evals/README.md](../../../evals/README.md).

## Arms and fair comparison

| Condition | KB-enabled arm | File-search control |
|---|---|---|
| Solver | Same serving model, model settings, harness, tools, context limits, and response format | Identical |
| Task | Same exact task text and requested artifact | Identical |
| Information | Fresh copy of the same synthetic curated corpus | Byte-identical corpus |
| Navigation metadata | Same `KB.md`, index, filenames, frontmatter, headings, aliases, links, and dates | Equally readable and searchable |
| Workflow | Current KB skill and query guide; normal `map`/`get-node`/`recall`/`get` workflow | Competent instructions to inspect metadata, use `rg` and file listing, search synonyms/identifiers, follow links, and read line-numbered source ranges |
| Fallback | Ordinary shell/file access remains available; record when used | Same ordinary shell/file access; no KB retrieval commands |
| Evidence policy | Read source evidence, cite source and lines, retain exceptions, distinguish conflicts/missing facts | Identical |
| Restrictions | No external research, other agents, gold, prior answers, production edits, or knowledge writes | Identical |

This preferred comparison estimates the effect of **offering the KB workflow**, including its instructions and output format. Analyze by assigned arm even if a KB solver falls back entirely to files; record adoption and fallback as diagnostics. Do not drop nonadopters. If the intended experiment instead forces KB-only evidence access, freeze that rule and call it a **forced workflow comparison**. It does not estimate the value of adding KB to an otherwise capable agent.

The KB skill naturally has different setup overhead from file-search guidance. Count that difference rather than padding the control with irrelevant text or pretending instruction reads are free. Give both arms competent, task-neutral guidance; do not give either arm hints about the held-out answer, filename, preferred query, or expected failure mode. The same general instruction to verify temporal authority, scope, and conflicts belongs in both arms.

Both arms may read all allowed content when it fits their budget. Do not forbid a successful full-file read merely because KB returns smaller sections. Do not secretly precompute a richer index for KB, hide useful metadata from `rg`, disable ordinary regex/context features, or make the control reproduce KB's exact query sequence. If the corpus is easy enough that both succeed quickly, that is a valid pilot result.

## Eight task families

Use one newly authored task from each family. Each should produce a concrete answer or small decision artifact, not merely ask whether a command returned a string. The eight cases provide coverage, not eight estimates of each behavior. Use synthetic names, facts, dates, and distractors absent from prior fixtures; do not recycle the historical 20 scenarios as held-out tasks.

| Family | Intended task | Required challenge and unbiased construction |
|---|---|---|
| Exact lookup | Report a named record's setting, owner, or deadline with evidence | A normal easy case. Include a near-name distractor; do not obscure all exact matches. |
| Paraphrase and alias | Find a policy described in user vocabulary rather than its title | Natural aliases/synonyms in shared metadata or prose. Do not target only words known to match KB internals. |
| Cross-page synthesis | Determine a plan from a policy plus a separate project/customer record | At least two necessary evidence facets; no single index sentence gives the complete answer. Both search and links can locate them. |
| Scope and exception | Apply a general rule to a case with an explicit exception | Gold requires the condition and exception, not just the generic rule. Similar irrelevant exceptions test scope. |
| Temporal supersession | State the currently effective decision and explain what changed | Explicit authority/effective dates. A recently edited historical page is a distractor; “latest file” alone must not decide truth. |
| Unresolved conflict | Report incompatible claims and the decision needed to resolve them | Two equally authoritative sources with no supersession rule. Gold requires both claims and uncertainty, not an arbitrary selection. |
| Evidence absent | Answer what can be established and identify a missing prerequisite | The requested fact is genuinely absent from the allowed corpus. Related mentions prevent an empty-search shortcut; a successful answer gives a specific evidence gap. |
| Long/noisy evidence | Produce an actionable answer that depends on a later qualification in a long relevant page | Realistic distractors and document length, fixed before solving. Both arms can narrow or page. Label this a stress case if its scale is not representative. |

Avoid filenames, headings, or index entries such as “correct answer,” “current winning policy,” or scenario IDs that reveal the oracle. Task wording should not copy an exact KB node label in one family while suppressing an ordinary search term in another. Draw distractors symmetrically: a distractor useful for testing wrong-page selection should also be plausibly returned by KB.

Freeze the task-family mix, corpus size, document length distribution, answerability, and difficulty rationale before outcomes. Do not change a case after seeing a miss to make KB look better. Common corpus pages create dependence between tasks even with fresh agent contexts; retain corpus/family IDs for later analysis.

## Hidden gold and trace requirements

For every task, freeze the following outside the solver's readable working area:

- **Answer requirements (`G`)**: atomic required propositions, decisions, constraints, and uncertainty statements. Split compound facts before grading; mark which requirements are critical to the requested action.
- **Evidence requirements (`E`)**: necessary evidence facets, each linked to one or more acceptable support sets of exact source spans. A support set may require multiple spans, such as a general policy plus its exception. Alternative sufficient sources receive equal credit.
- **Misleading alternatives**: known stale, contradicted, wrong-scope, or irrelevant claims, with the source that demonstrates the problem.
- **Task rubric**: the requested action, success conditions, permissible recommendations, and what constitutes a consequential wrong recommendation.
- **Answerability and authority**: whether the answer is determinate, conflicted, or unavailable; which dates and authority rules matter.

Hash the corpus, prompts, arm instructions, source version, protocol, and gold before the first solver context. Save model/harness identifiers and actual settings where available; leave unavailable values explicitly unknown. Use a fresh corpus copy and fresh context for every arm/task pair. No solver receives the sibling answer, gold, evaluator files, task-author reasoning, or previous trial context. Put the gold outside its accessible scope where the harness can enforce this; a prompt prohibition alone is an isolation limitation to report.

Capture the complete visible instruction/response stream, commands, exit status, stdout/stderr, tool displays, final answer, timings, and before/after knowledge hashes. Raw files saved without being read into the model's context do **not** count as retrieved evidence. A search scanning a file internally also does not count as exposing its content. If the host truncates output, grade the displayed portion; do not credit unseen saved stdout.

Metadata and index content count as sufficient evidence only when the frozen gold recognizes them as sufficient for the same purpose in both arms. Otherwise they are locators. A KB map summary or a matching filename is not automatically evidence for the underlying claim.

## Operational metrics

Use the task as the reporting unit. Report numerator and denominator with every ratio, and show each paired result. Keep the following dimensions separate rather than producing a weighted “KB score.”

| Metric | Operational definition | Interpretation and edge cases |
|---|---|---|
| **Grounded task success — primary** | Binary: all critical answer requirements satisfied; no critical false, wrong-scope, stale, or unsupported claim; requested action/artifact usable; all material factual claims supported by actually exposed evidence with locatable references; required conflict/abstention behavior correct. | Freeze the checklist per task. Failed tools, exhausted budgets, and no final answer are failures, not exclusions. A safe partial answer can still be useful without being a success. |
| **Retrieval evidence recall** | Number of `E` facets for which a complete acceptable support set was actually exposed to the solver, divided by `|E|`. Count each facet once, regardless of repeated reads. | Measures evidence reached, not evidence used or answer correctness. If a qualifier is required, exposure to the unqualified sentence alone does not satisfy the facet. For a purely absent fact with no positive support span, this metric is `N/A`, not 100%. |
| **Answer requirement recall** | Number of atomic `G` requirements correctly expressed in the final answer, divided by `|G|`. | This can be high even when a correct answer lacks retrieved support. Keep that distinction visible. For absence/conflict tasks, the required uncertainty or missing-evidence statement is an answer requirement. |
| **Factual precision** | Correct material factual claims divided by all material factual claims asserted in the final answer. | Check additional claims against the allowed corpus, not only the expected answer text. Split independently checkable clauses consistently. A clearly labeled proposal is not a factual assertion; its claimed premises still are. No asserted facts gives `N/A`, never perfect precision. |
| **Grounding precision** | Material factual claims entailed by the source evidence the solver actually saw, divided by all asserted material factual claims. | Distinguishes a lucky correct guess from supported work. Sources must support the entity, time, scope, and qualifiers. A contradicted statement is not grounded merely because it quotes a source. |
| **Citation completeness / accuracy** | Completeness: material factual claims with a supporting, locatable citation / all material factual claims. Accuracy: citations that support their associated claims / all supplied citations. | Report separately if feasible. A real path or line number does not establish entailment. Do not treat multiple redundant citations as extra credit. |
| **Task utility** | Blinded ordinal rating: 0 = wrong, misleading, or unusable; 1 = safe but substantial task work remains; 2 = usable with a small omission or correction; 3 = complete and directly usable. | Use task-specific anchors and show the paired ordinal values. Do not interpret a one-point shift as a fixed quantity of productivity or silently average away a consequential error. |
| **Failure profile** | Per-task indicators for wrong entity/scope, missed dependency/exception, stale decision, invented resolution, unsupported certainty, unjustified abstention, retrieval failure, or budget exhaustion. | Retain multiple applicable labels. Explain whether failure was evidence not reached, evidence reached but misread, or correct facts assembled into a wrong action. |

Do not equate exact-string matching with fact recall. Allow equivalent wording while requiring the same entities, relationships, qualifications, and requested consequence. For reproducibility, retain each claim/requirement decision and its supporting span, not just the aggregate score.

### Temporal, conflict, and abstention checks

- **Temporal correctness:** identifies the operative fact at the task's reference time, applies the stated authority/supersession rule, and does not present stale facts as current. Correctly recalling both old and new values is insufficient without choosing the right effective status.
- **Conflict handling:** identifies both incompatible claims, cites the competing evidence, and avoids inventing a resolution. The useful next step is the missing authority/decision, not a generic “more research needed.”
- **Abstention:** when evidence is absent, states the specific gap without inventing a fact and preserves any supported partial answer. Also record **false abstention** on answerable tasks; otherwise an agent can look safe by declining everything.

Report these as case-level outcomes with denominators. One conflict case is one conflict observation; it is not a reliable conflict-handling rate.

## Resource accounting and budgets

Choose and freeze the shared solver limits before dispatch. Use the same maximum model turns, aggregate model-visible input allowance, tool-output allowance, and elapsed-time limit to the extent the harness can enforce them. State precisely which limits are enforced versus instructed. A per-call 16 KiB KB envelope alone is not a matched total context budget.

Record all of the following, including failure and recovery work:

| Measure | Included costs |
|---|---|
| End-to-end elapsed time | From receiving the task and necessary arm setup through final answer, including instruction reads, parsing, retries, and source retrieval. Distinguish queuing and human grading when available. |
| Tool turns and source operations | All successful and failed tools, plus the number of retrieval/read/search operations. A shell batch is not “one search” merely because it uses one tool turn. Record batching consistently. |
| Visible bytes | Actual UTF-8 bytes shown to the solver: shared instructions, arm instructions, corpus rules, tool wrappers/envelopes, errors, duplicate reads, evidence, and final answer. Separate categories and total. |
| Raw output bytes | Complete stdout/stderr when available, separately from bytes actually displayed. Do not substitute raw output size for consumed context. |
| Tokens and monetary cost | Harness-reported input/output/cache/reasoning usage and charged cost, including setup where observable. Missing reports are `null`; bytes are not token or dollar estimates. |
| Retrieval timing | Observed subprocess/tool elapsed time, distinct from model reasoning and end-to-end latency. One measurement is descriptive. |
| Knowledge mutation | Before/after hashes for all protected corpus files; operational cache/log writes listed separately and confined to disposable fixtures. |

Repeated common context is still real total overhead. Report the shared harness/system cost when measurable and the arm-specific marginal cost separately. Do not exclude initial skill/query-guide reads from KB's cost or the equivalent guidance/metadata reads from the control's cost.

Randomize which arm goes first within a task and interleave tasks to reduce time/order confounding; record the schedule before running. Fresh contexts remove answer carryover, not shared process caches or host contention. If trials run concurrently, record concurrency and treat latency as observational. A larger latency study should use counterbalanced isolated runs and an explicit cold/warm-cache policy.

The 16-context cap includes any solver context that receives a pilot task and arm instructions, even if it fails or is interrupted. Preserve failures and partial pairs. A permitted replacement is an additional context, never a silent substitute; do not exceed the cap without a recorded scope change. Mechanical fixture checks do not use the solver and can happen before freezing. Do not use pilot task outcomes to tune instructions, task difficulty, or gold.

## Grading and analysis

1. Freeze the rubric and all reference answers before solving. Mechanical checks can validate corpus copying, paths, line spans, and accounting without predicting agent behavior.
2. Grade final answers with arm labels and order hidden where possible. Use neutral source identifiers and a common answer format. The grader first sees the task, gold, and answer; a later provenance pass checks the exposed evidence trace.
3. Have a second assessor review consequential errors and ambiguous claim judgments. Resolve disagreements with the frozen rules and preserve both original ratings. If only one informed assessor is available, disclose that limitation rather than claiming blinded independent evaluation.
4. Report paired success categories: KB only succeeds, file search only succeeds, both succeed, neither succeeds. Show each task's recall, precision, utility, cost, and failure explanation. For costs, show absolute paired differences and ratios only where denominators are positive; separate all attempts from successful attempts.
5. Report descriptive macro summaries with equal task weights and the eight case denominators. Do not turn claims, citations, tool calls, or repeated reads into independent samples. Do not run many metric tests and select whichever favors KB.

Useful interpretation examples:

- Higher evidence recall with unchanged answer quality shows a retrieval difference, not improved task completion.
- Equal task quality with lower **total** resource use suggests efficiency on these cases; fewer retrieval bytes alone is insufficient when instructions or extra turns cost more.
- Correct answers from less evidence do not prove better recall; they may reflect an easy case, sufficient alternative evidence, or unsupported guessing.
- Both arms at a ceiling indicates these tasks did not discriminate quality. Preserve that result. Create a separately held-out, justified next suite rather than retrofitting the pilot.
- A KB-specific failure or overhead is evidence to fix or investigate, not a reason to remove the case from the report.

## What eight pairs can decide

The pilot is sufficient to decide whether the method runs end to end, whether evidence/answers can be scored reproducibly, whether setup costs are observable, and which concrete failure mechanisms merit investigation. It can justify a specific engineering fix or a larger study. Eight hand-authored cases with one run per arm do not characterize sampling or model variance. An observed 8/8 success or advantage is still confined to these tasks and conditions.

No broad significance, power, noninferiority, ranking, or return-on-investment claim should come from this pilot. A percentage such as “75%” must appear with “6 of 8 pilot tasks,” not as an expected production success rate. The evaluation's primary unit is a paired task; repeated stochastic attempts in a future study remain nested within their task and corpus.

## Gate for a larger preregistered study

Advance only after the following are satisfied, regardless of which arm appears better:

- The corpus equality and gold-isolation checks pass, knowledge hashes remain unchanged, and protocol deviations are fully enumerated.
- The resource accounting includes instruction/setup costs or clearly marks unavailable fields; every counted evidence facet is recoverable from a visible trace.
- The scoring rubric distinguishes useful partial answers, grounded success, stale facts, conflicts, and appropriate/false abstention without unresolved consequential ambiguity.
- There is a stated decision the larger study could change: adopt KB for a defined workload, change its workflow, or reject a proposed improvement. A practical benefit and tolerable failure/cost tradeoff are specified before new outcomes.
- New tasks come from a declared target workload and multiple independently constructed corpora/topics. Existing pilot tasks become development cases; they are not reused as held-out evidence after tuning.

Before spending on the larger study, preregister the primary metric and minimum practically important difference, arm prompts, model/harness versions, task sampling/strata and weights, corpus split, repeated-trial policy, resource limits, exclusions, stopping rule, grading/adjudication, and uncertainty analysis. Choose sample size using a prospective paired-design power or precision analysis for that practical difference, with conservative assumptions about discordant outcomes and task/corpus dependence. The eight-task pilot is too small to supply reliable variance assumptions by itself; do not choose the next sample size solely from its apparent effect.

The next study should include enough independent tasks and corpora for the intended claim, replicated stochastic attempts to distinguish workflow effects from sampling noise, and uncertainty estimates that keep repetitions clustered by task/corpus. A broad “any shell/file agent” claim additionally requires multiple representative models and harnesses. Compare those strata explicitly rather than pooling away failures. Evaluate raw-to-curated preparation, sustained maintenance, or whole-system return on effort only in separately designed arms that include those costs.

This review recommends the bounded pilot with these controls. It does not authorize a larger run, claim any measured advantage, or substitute design quality for empirical results.

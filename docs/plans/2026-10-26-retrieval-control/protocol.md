# KB retrieval control pilot

## Question and estimand

Does adding the current KB CLI and query skill improve an agent's ability to answer questions about the same saved Markdown knowledge, compared with a capable file-search agent? This estimates the retrieval/workflow bundle conditional on an already curated vault. It cannot estimate the value of curation, session extraction, or summarization.

This first run is an eight-case feasibility pilot. The tasks are newly authored and frozen before any solver runs, but authored by the experiment designer with knowledge of KB. They are not an independently sampled held-out benchmark. Do not tune KB, task wording, gold or scores after observing results.

## Arms

- **Files:** regex search backed by `rg`, filename listing, focused numbered file reads. Can inspect the same Markdown, index, frontmatter, aliases and links. Clear instructions to broaden/rephrase queries and stop once evidence is sufficient.
- **KB-enabled:** all the above plus the unchanged KB CLI and current skill/query guide. Normal KB navigation is encouraged; direct file fallback is permitted and recorded.

Both arms get identical task text, fresh copies of the same corpus, identical output/call caps, fresh model context and the same inherited serving model. Each trial starts without a KB cache. No qmd, hooks, network, other agents or prior answers. No real-vault data. Limits are instructed and enforced by the recording wrapper; avoiding tools outside that wrapper remains an instruction boundary, not an OS sandbox.

## Cases fixed before execution

| Case | Task family | Required behavior |
|---|---|---|
| T01 | Exact fact with scope distractors | Production retry count and initial delay |
| T02 | Paraphrased question | Find audit retention and policy owner without matching question phrasing |
| T03 | Knowledge update | Current pilot date, superseded date and change rationale |
| T04 | Cross-page reasoning | Resolve escalation role to a person and resumption prerequisites |
| T05 | Exhaustive retrieval | List every open project blocker with owner, excluding closed and other-project items |
| T06 | Long document | Find approved rollback checkpoint and approver among historical notes |
| T07 | Unresolved conflict | Report both dates and refuse unsupported certainty |
| T08 | Missing evidence | Decline to invent an engagement signatory or fee |

The corpus has topical distractors and near-matching entities. Gold facts identify literal supporting source spans; all required sources are available in both arms. An empty gold set for an unanswerable case is scored as abstention, never as zero recall.

## Budget and recording

Each trial: at most 16 wrapper calls, 128 KiB total visible output, 16 KiB visible output per call. Instruction loading counts as a call and its bytes are included. Same-arm guidance is frozen. Common instructions and task text are reported separately. Record every wrapper invocation, full underlying output and the exact visible output, exit status, elapsed tool time and before/after knowledge hashes. Preserve truncated and failed calls. Raw returned bytes are not model tokens.

The model/harness exact identity, inference token usage and cost may be unavailable through the collaboration API. Record null, not estimates. Shared-host parallel execution makes wall time and subprocess latency descriptive only; performance timing requires later isolated randomized trials.

## Scoring

Primary outcome: **strict grounded task success**. Every required answer fact is correct, every material claim is supported, no wrong entity/superseded fact, correct conflict/absence behavior, and no knowledge edit.

Report separately:

1. **Answer fact recall:** correctly stated required facts / required facts. Macro-average across answerable cases; show counts per case.
2. **Returned evidence recall:** gold facts whose supporting text was actually exposed in visible outputs / gold facts. A map title, filename or unread citation does not count. Deduplicate repeated retrieval. N/A on empty-gold cases.
3. **Claim support precision:** supported factual claims / all factual claims in the answer. Separate omissions from fabrication.
4. **Citation support and coverage:** whether cited source spans support the attached claim, and the share of material claims with an appropriate citation.
5. **Temporal/conflict/abstention:** per-case verdicts; a larger study needs balanced answerable/unanswerable sets and false-abstention rates.
6. **Efficiency:** all wrapper calls, knowledge retrieval calls, visible bytes including guides, underlying bytes, failed/truncated calls and tool time. Do not compare CLI-only counts with whole-harness counts.
7. **Integrity:** before/after knowledge hashes; permitted operational `.kb` caches/logs reported separately. No claim about adversarial safety from a zero-write retrieval pilot.

An independent assessor receives opaque trial IDs, answers, cited source material and gold, without arm labels. Root checks mechanical evidence exposure and counts. Fixed-rubric human-calibrated scoring is required before a larger benchmark; this pilot's agent grading is not ground truth for open-ended correctness.

## Interpretation and follow-on

Publish paired outcomes and individual failures, including ties. Eight tasks and one run per arm do not support a general recall improvement, latency/cost savings, percentile tail estimates, or statistical superiority. No p-value hunting. A larger preregistered study should use task-level paired confidence intervals, multiple runs, more than one agent/harness, independent task creation and realistic scale tiers. Compare curation separately with a raw-document control, and add a same-documents/same-instructions ablation to separate workflow instructions from retrieval tools.

# Retrieval-control audit

Date: 2026-10-26. Scope: source and saved-artifact inspection only. No tests, retrieval trials, or model calls were run for this audit. Only this document was written.

The existing material can supply a fixture factory, measurement patterns, development cases, and a scoring checklist. It does not establish whether KB improves agent retrieval over ordinary file search on the same curated corpus. A controlled experiment should compare that specific treatment, preserve normal search freedom in the file arm, and use fresh prompts and evidence withheld from solvers for any prospective evaluation claim.

## What the existing artifacts measure

| Artifact | What it establishes | Safe reuse and limitation |
|---|---|---|
| `evals/fixtures.ts:33–74` | A small synthetic vault used by both deterministic contracts and human-reviewed agent trials: eight curated wiki pages, artificial long/repetitive content, raw imports, sessions, and a deterministic local provider. The child environment omits credentials and qmd. | Reuse fixture construction and fresh-copy isolation. Reusing these texts or answers is a development/control rerun, not a held-out corpus. This is not a realistic large-vault distribution. |
| `evals/fixtures.ts:126–139` | SHA-256 hashes of every regular file outside the root `.kb` directory. This detects final additions, deletions, and content changes in knowledge files. | Reuse the knowledge/operational-write distinction. It does not record symlinks, modes, empty directories, transient writes reverted before the snapshot, or writes outside the vault. Preserve separate operational-file evidence. |
| `evals/prepare.ts:15–42` | Prepares a fresh trial, all transcript variants and ingestion inputs, selected CLI/skill paths, the before snapshot, and recording filenames. It does not run or assess an agent. | Reuse fresh-directory checks and construction. **Do not expose `task.json` to a solver:** it includes the full `scenario`, including `inputs`, `expected`, and allowed-write expectations. Put evaluator material outside the solver directory and emit a sanitized solve manifest. Merely asking the solver not to read it is weaker isolation. |
| `evals/run.ts:42–70, 149–201, 243–256` | Fixed CLI transport/behavior contracts, including framing, bounded output, transcript/provider handling, source-line equality and query write safety. Commands are predetermined, and the provider is deterministic. Records command output and a single elapsed-time sample. | Reuse the independent envelope-checking approach and recording mechanics. A contract pass is not an agent answer-quality or workflow result. The canonical retrieval test already supplies the query and checks a specific node; it does not test discovering the query or choosing the evidence. |
| `evals/scenarios.json:5–148` | Twenty published task specifications covering retrieval, uncertainty, bounded output, trust, ingestion and session workflows. Some expected outcomes require KB-specific actions. | Reuse as development cases and inspiration for categories. For a KB-vs-file comparison, correctness/grounding criteria must be tool-neutral; “use map then get-node” cannot be an answer-success requirement in the file arm. Ingestion and session/provider tasks are outside the proposed same-curated-corpus retrieval scope. |
| `docs/plans/2026-10-16-portable-agent-workflows/reports/A1.md:5–35` | Eighteen actual agent trials: six tasks in each of original KB, initial portable KB, and refined KB. Same host, one observation per task/condition, fresh contexts and vaults. Reported correct grounded answers and no unauthorized knowledge changes. | Reuse as historical development evidence. **There is no file-search control.** The refined arm repeats cases after instruction tuning, and the report explicitly calls them development cases. Trials preceded later implementation corrections. Do not transfer their success/efficiency numbers to today's source. |
| A1 saved `solve.json`, `commands.jsonl`, `knowledge-hashes.json`, answers and activity reports | Auditable outputs for those old trials. For example, refined A09 records two CLI commands and the exact evidence returned. `solve.json` shows the intended sanitized solver-input shape. | Reuse the evidence layout. Saved command records contain CLI args, stdout, stderr, exit code, byte count and elapsed time, but A1 counts only CLI output; skill reads, other shell output, reasoning and final-answer tokens are excluded (`A1.md:13`). Frozen instruction trees can reproduce a historical condition, not substitute for a current skill digest. |

A1 demonstrates why agent-level observations matter: the initial long-page workflow used 17 calls/605,678 stdout bytes, followed by instruction correction and a two-call/1,126-byte rerun (`A1.md:7, 19`). Those numbers motivated the refinement; they are not an independent validation of it. The absence, changed-decision and no-filing cases also show mixed efficiency (`A1.md:9, 17–22`).

All twenty current scenarios and their gold answers are public in the checkout, even where A1 did not execute them. “Not previously run” is weaker than “held out.” A new prospective case set can be frozen before trials and withheld from solvers, but no claim should imply that the old suite was hidden from development.

The fixture's `KB.md` currently tells the reader to “Use CLI evidence envelopes” (`evals/fixtures.ts:46`). That is suitable for its original portability trials but would instruct the ordinary-file control to use the treatment. A new shared corpus should use tool-neutral provenance and read-only conventions, with CLI-specific instructions supplied only through the KB arm's frozen skill. Preserve identical substantive source content between arms.

## Retrieval behavior relevant to the comparison

`map` is a deterministic structural candidate selector, not a semantic relevance ranker. It lowercases and trims the entire query, then admits at most 30 distinct node IDs in this order (`src/lib/map/candidates.ts:23–112`; concatenation in `src/commands/map.ts:172–186`):

1. Exact page title or alias.
2. Exact tag value.
3. Heading containing the complete query substring; results are section IDs.
4. One-hop outgoing wikilinks from title/tag/heading-matched pages.
5. Backlinks to those matched pages.
6. Whole-file literal substring matches, only while capacity remains; results are page IDs.
7. Optional qmd hints, again only while capacity remains.

There is no tokenization, synonym expansion, fuzzy matching, term-frequency score, or semantic score in the built-in path. Within buckets, results follow deterministic file/tree order, not relevance; wiki paths are sorted (`src/lib/map/builder.ts:28–35`). Pages and their sections are different IDs, so several headings on one page can consume the cap. The cap itself is not exposed as a truncation flag; `map`'s budget truncation is a separate mechanism. A single empty or incomplete map is therefore insufficient evidence of corpus-wide absence.

`map` returns titles/tags and heading paths with node IDs and source ranges, not answer text (`src/commands/map.ts:21–46`). When its wire budget is exceeded, it degrades from full node summaries to parent-page summaries to titles, then drops the tail (`map.ts:81–137`). This can turn a useful section candidate into a broad page candidate. The candidate cap can discard relevant candidates even when the resulting output fits the byte budget.

`get-node` retrieves exact source lines and can include adjacent sibling sections and limited one-hop previews (`src/commands/get-node.ts:110–185`). It preserves the primary section before optional content and provides continuation for necessary broad reads (`get-node.ts:187–230`). This is a plausible KB advantage on long, well-structured pages; the model must still decide which evidence matters and when it is sufficient.

`recall` searches each line for the entire case-insensitive literal query, emits plus/minus three lines per matching line, and orders matches by sorted filename then line (`src/commands/recall.ts:11–31, 79–93`). It has no regex, AND-term, semantic, or relevance-ranking behavior. Overlapping contexts may repeat text. Result pagination uses the match offset (`recall.ts:111–120`). Ordinary `rg` is allowed to be more expressive in the control; restricting it to KB's literal semantics would handicap the baseline.

Both built-in search paths skip hidden paths, symlinks and non-Markdown files, and body reads reject files over 256 KiB (`src/lib/wiki-read.ts:14–37, 46–57`). Keep ordinary evaluation documents within this supported scope, or label out-of-scope/oversize cases separately. Raw/session evidence is not part of curated search.

The tree is cached in `.kb/index/tree.json`; subsequent calls use file size and truncated mtime to avoid reparsing unchanged pages (`src/lib/map/cache.ts:44–107`). Fresh copied vaults should begin without this cache, with cold setup cost recorded, unless warm-cache behavior is an explicitly separate condition. Do not prewarm one arm silently. qmd must remain absent from the controlled CLI environment: `map` invokes it whenever found on PATH, and its hints are optional (`src/lib/qmd.ts:132–181`).

## Plausible task categories

Use realistic saved-knowledge questions with answers fixed from the corpus, and report categories separately. These are hypotheses for coverage, not predicted wins.

| Category | What it exercises |
|---|---|
| Named decision, exact title, or known acronym/alias | KB's exact-title/alias path versus fast filename/content search. Include short pages where direct file reading may be cheaper than navigation. |
| Narrow fact deep in a long structured page | KB's section selection and exact line evidence versus targeted `rg` and a bounded file read. Include both a question naming the heading and one phrased naturally without the heading. |
| Fact present only in body text | Literal fallback and focused snippets. Avoid assuming a map title is evidence; the file arm may need fewer steps. |
| Paraphrase with little literal overlap | Query reformulation in both arms. KB does not supply built-in semantic matching. Distinguish retrieval failure from unsupported answers. |
| Multi-page decision and rationale, connected by links | Discovery through links/backlinks and the agent's ability to synthesize evidence. Include relevant links and plausible irrelevant neighbors. |
| Current decision versus older plan | Whether chronology and an explicit supersession relation are recovered. Avoid rewarding “choose the latest date” when no supersession exists. |
| Unresolved conflict or explicitly undecided issue | Faithful uncertainty and scope: report both claims, or state that no decision exists. |
| Truly absent requested fact | Appropriate bounded search and abstention. Score invented certainty as a failure; do not treat one capped map result as proof of absence. |
| Common headings/tags plus realistic distractors | Crowding of the 30-node candidate cap and deterministic file-order effects. Include ordinary section names such as “Decision” without turning the primary aggregate into an adversarial stress suite. |
| Untrusted quoted instructions and recommendation requests | Maintain the evidence/instruction boundary, distinguish stored facts from suggestions, and avoid knowledge edits. Same corpus and same boundary instructions in both arms. |

Unicode and citation exactness fit across these categories. The giant indivisible line, malformed transcript, unavailable provider, ingestion and recovery cases should remain separate contract/stress or workflow suites; they do not answer the primary retrieval-control question.

## Cheapest fair recording wrapper

Use one small runner outside the corpus, shared by both arms, that accepts a structured executable/argument array and an explicit working directory. Reuse the direct-spawn/concurrent-stream-drain pattern in `evals/run.ts:58–69`, but generalize it to every shell/file read. This can be done as evaluation scaffolding without changing production retrieval code.

- **Record exact execution:** run ID, task/arm/trial, command index, actual executable plus argv, cwd, start/end timestamps, exit status/signal/timeout, and a small explicit environment manifest. Log executable/tool versions once. Avoid dumping an inherited environment or secrets. Record source, corpus, wrapper, prompt and complete skill/reference digests so a shared commit with changed files is not mistaken for the same condition.
- **Keep complete raw streams:** concurrently drain stdout and stderr into distinct per-command files outside the corpus. Record raw byte lengths and SHA-256 hashes. Preserve original bytes; normalized/redacted copies are additional artifacts. Do not parse a host-truncated rendering or recompute original byte counts from a normalized log. The current run helper decodes UTF-8 and records CLI aliases instead of the full executable argv, so it is a pattern rather than the complete new recorder.
- **Separate output measures:** count all emitted stdout/stderr, and separately record the actual text/bytes delivered to the solver if the harness exposes them. Include skill/convention reads, `rg --files`, searches, line reads, errors and local parsing output in both arms. Do not count CLI stdout alone as total context cost. Do not equate bytes with tokens or infer cost from them.
- **Time the child consistently:** start immediately before spawning and stop only after exit and both streams are drained. Record snapshot/logging time separately. Whole-task elapsed time is a separate clock; adding command latencies omits agent deliberation. One local sample is descriptive, not a latency benchmark.
- **Track knowledge changes:** keep immutable initial manifests, then compare a before/after manifest around each command and at task completion. Sort relative paths and record file type, regular-file content hash, and symlink target without following links. Report adds, deletes and modifications. Separate approved operational files such as `.kb/index/tree.json` and access logs from curated knowledge changes. Start/end manifests establish net changes, not every attempted or transient write; state that limit rather than claiming an OS-enforced read-only guarantee.
- **Avoid invisible reads:** every permitted retrieval command in both arms goes through the same recorder. If a harness exposes an unrecorded native file reader or unrestricted tool, either capture that tool's output too or mark the trace incomplete. A wrapper by itself does not force compliance. Normal `rg`, file listing, and bounded line reads should remain available in the file arm; KB receives its frozen CLI and skill, with an explicit allowance for necessary local envelope parsing.
- **Handle shell syntax honestly:** direct argv execution gives exact top-level args and avoids quoting ambiguities. If pipelines or scripts are permitted, record the full script and shell argv, but do not claim those are every nested process's argv. For the cheapest comparable run, use the same direct-command wrapper and ordinary single-command reads in both arms.

Use fresh contexts and separate copies of the identical eligible curated files, with the same task prompt, corpus boundaries, citation requirement, time/tool budget, host output limit and agent configuration. Keep gold answers, test annotations, previous answers and evaluator files outside the solver-visible material. Freeze instructions and scoring before observing the trials; balance condition order and report paired per-task results. If tuning follows a failed case, retain that run and label subsequent repeats as development.

Score answer correctness, evidence support, uncertainty, unauthorized knowledge changes, command count, measured output and elapsed time separately. Tool-specific workflow adherence is a diagnostic, not a fair cross-arm success criterion. Tokens/cost and exact model identity are reportable only when the harness actually supplies them. This comparison estimates the combined benefit of **KB CLI plus its skill** on an already curated corpus; it does not isolate curation quality, the CLI alone, or general agent superiority.

## Verification of this audit

All findings above come from reading the current source and existing A1 artifacts. Historical pass counts were not rerun or promoted to current verification. The source-code line pointers are checkout-relative. This audit makes no empirical claim about which arm will win.

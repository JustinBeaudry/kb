# Results: KB at 1,000 pages with a weaker solver, and the guide-versus-commands ablation

Run 2026-11-25. 24 of 24 scheduled trials completed (8 tasks × 3 arms × 1 run). Each trial ran in a fresh headless `claude -p --model haiku` process (reported model `claude-haiku-5-5`), at most 4 at once. All 24 answers were graded by blinded `claude-sonnet-5-5` assessor calls, one per answer. Design, fixtures, scripts and schedule were frozen and committed before any solver ran (`freeze.json`, commit 13be34d). Raw data: `answers/`, `measurements.json`, `assessor-packet.json`, `assessments-raw/` (verbatim assessor stdout), `assessments.json`, `blind-key.json`, `results.json`. `trials-archive.jsonl.gz` holds the solver transcripts and adapter traces; the raw underlying stdout is dropped because the generator can reproduce it.

## Headline

- **Recall stayed at the ceiling.** All three arms stated 100% of the required facts and exposed 20/20 gold evidence lines, even with 1,002 pages and a Haiku solver. Scale made the search output flood: 35 truncated outputs against 8 in the pilot. It did not stop any arm finding the evidence.
- **Strict success: A `files` 3/8, B `kb-full` 1/8, C `kb-minimal` 5/8.** Most failures (12 of 15) are answers that were correct but added a claim their cited spans did not support. These were usually remarks about the `-Sandbox`/`-Legacy` near-duplicates. The substantive errors were:
  - A T08: substituted Cinder-Sandbox's signatory and fee for Cinder (wrong entity).
  - B T08: offered the same Cinder-Sandbox values as a candidate answer (wrong entity).
  - A T02: would not commit to the plain Corundum answer (behavior failure).
  - C had no substantive error.
- **Guide versus commands: the extra reading is the guide, not the commands.** With the full KB guide, Haiku **never ran a KB command** (0 of 47 calls). It read 10% more bytes than the files arm, and all of that excess was the 9.8 KB guide. Without the guide it read 17% less than the files arm. With the short guide, Haiku used KB commands in 4 of 8 trials and read 16% fewer bytes than the files arm in total. Those commands found little: `map` returned no results 3 of 4 times, and `recall` truncated every time.

## Paired table (strict success ✓/✗, fact recall, calls, visible bytes, KB commands)

| Task | Family | A files | B kb-full | C kb-minimal |
|---|---|---|---|---|
| T01 | scoped exact | ✗¹ 2/2 · 5 · 5,365 · – | ✗¹ 2/2 · 6 · 29,620 · – | ✓ 2/2 · 6 · 34,636 · map+recall |
| T02 | paraphrase | ✗² 2/2 · 8 · 53,319 · – | ✗¹ 2/2 · 6 · 45,322 · – | ✗¹ 2/2 · 5 · 6,034 · – |
| T03 | temporal update | ✓ 3/3 · 4 · 18,884 · – | ✗¹ 3/3 · 4 · 27,708 · – | ✓ 3/3 · 5 · 18,346 · map+recall |
| T04 | cross-page | ✗¹ 4/4 · 7 · 19,681 · – | ✗¹ 4/4 · 6 · 44,489 · – | ✓ 4/4 · 7 · 19,694 · – |
| T05 | exhaustive list | ✗¹ 4/4 · 6 · 24,178 · – | ✗¹ 4/4 · 10 · 35,962 · – | ✗¹ 4/4 · 5 · 21,080 · map+recall |
| T06 | long document | ✓ 2/2 · 9 · 84,311⁴ · – | ✓ 2/2 · 6 · 44,021 · – | ✓ 2/2 · 7 · 59,710 · – |
| T07 | conflict | ✓ 3/3 · 5 · 34,960 · – | ✗¹ 3/3 · 4 · 31,705 · – | ✗¹ 3/3 · 5 · 22,953 · – |
| T08 | absent (abstain) | ✗³ abstention failed · 5 · 19,476 · – | ✗³ abstention failed · 5 · 28,201 · – | ✓ abstained · 7 · 35,038 · map+recall |

Visible bytes include the guide call. Every answer that is answerable stated every gold fact, and every trial exposed every gold line.

¹ Answer correct. Failed only because it added a claim with no supporting cited span. The extra claims were about the Sandbox/Legacy/Lab variants (T01, T02, T05, T07), the US export owner (T04), the public launch date (T03), or analytics aggregates (T02-C).
² Correct values, but the answer treated the Sandbox and Legacy retention periods as competing answers and called the owner "unresolved". The assessor scored this as a behavior failure. The B answer framed it almost the same way and was scored behavior-ok but failed on an unsupported claim. This is an assessor inconsistency, reported as scored.
³ Named Cinder-Sandbox's signatory (Hana Ines) and fee ($27,000) as the answer (A) or as the only candidate (B). Scored as wrong entity and a behavior failure.
⁴ One compound Bash command piped an adapter search into `head` (see Integrity). Its 16,101 bytes are counted here, but the model never saw them.

## Per-arm totals

| | A files | B kb-full | C kb-minimal |
|---|---|---|---|
| Strict success | 3/8 | 1/8 | 5/8 |
| Fact recall (macro, answerable) | 1.00 | 1.00 | 1.00 |
| Gold evidence exposed | 20/20 | 20/20 | 20/20 |
| Claims supported by cited spans | 48/52 | 42/53 | 43/46 |
| Wrong entity or superseded fact | 1 | 1 | 0 |
| Behavior correct | 6/8 | 7/8 | 8/8 |
| Failed only on an unsupported extra claim | 3 | 6 | 3 |
| Adapter calls | 49 | 47 | 47 |
| KB command calls (trials using KB) | 0 (0) | **0 (0)** | 8 (4) |
| Visible bytes, total | 260,174 | 287,028 | 217,491 |
| Guide bytes | 7,992 | 78,584 | 8,096 |
| Visible bytes, excluding the guide | 252,182 | 208,444 | 209,395 |
| Truncated outputs | 14 | 11 | 10 |
| Adapter tool time (ms, shared host) | 707 | 550 | 1,786 |
| Solver cost, USD (CLI-reported) | 0.0421 | 0.0448 | 0.0383 |
| Solver output tokens | 18,395 | 18,336 | 18,194 |
| Solver input tokens incl. cache | 660,349 | 677,422 | 642,303 |

Token and cost figures come from each CLI result event, and none were null. The 24 assessor calls cost $0.3194 in total.

## Guide versus commands (B vs C)

B and C have the same commands, budgets and prompt. Only the guide differs: 9,786 bytes against 975.

1. **The long guide did not cause KB use.** B ran no KB command in 8 trials, although its guide says to start with `kb map`. Haiku went straight to `search`/`read`. C used `kb map` followed by `kb recall` in T01, T03, T05 and T08 (8 calls).
2. **The extra reading is the guide.** B read 26,854 more bytes than A in total (+10%). The guide alone accounts for 70,592 more bytes in B than in A. Excluding the guide, B read 43,738 fewer bytes than A (−17%) and almost the same as C (208,444 against 209,395). The pilot's "+84% bytes for KB" therefore matches guide size plus the solver's own search choices. Here it was not caused by KB commands, which B never ran.
3. **Accuracy favored the short guide.** C scored 5/8 strict against B's 1/8, with 3 unsupported claims against 11 and 0 wrong-entity errors against 1. That gap does not track KB-command use. C succeeded on T04 and T06 without KB commands and failed on T05 with them. A plausible reading is that the long guide made answers wordier, with more uncited remarks. With 8 tasks and one run per cell, this is a hypothesis, not a finding.
4. **When used, KB commands contributed little.** In all 4 KB-using trials Haiku ran `kb map` with a question-like phrase, then `kb recall <entity>`. `map` returned `no_results` 3 of 4 times ("Nacre production delivery retry", "Larch pilot November 12", "Cinder penetration test engagement fee signed"). `recall` truncated at the 16 KB per-call cap all 4 times, flooded by near-duplicate pages. Every one of those trials then fell back to `search`/`files`/`read` for its cited evidence, and none used `get-node` or `get`. C's KB-using trials averaged 27,275 visible bytes, against 27,098 for its other trials.

## Scale effect against the pilot

| | Pilot files (Opus, 120 pages) | Pilot KB (Opus) | A files (Haiku, 1,002 pages) | B kb-full (Haiku) | C kb-minimal (Haiku) |
|---|---|---|---|---|---|
| Strict success | 6/8 | 6/8 | 3/8 | 1/8 | 5/8 |
| Fact recall | 1.00 | 1.00 | 1.00 | 1.00 | 1.00 |
| Evidence exposed | 20/20 | 20/20 | 20/20 | 20/20 | 20/20 |
| Wrong entity/superseded | 0 | 0 | 1 | 1 | 0 |
| Calls | 34 | 41 | 49 | 47 | 47 |
| KB command calls | 0 | 5 | 0 | 0 | 8 |
| Visible bytes | 104,430 | 192,426 | 260,174 | 287,028 | 217,491 |
| Truncated outputs | 3 | 5 | 14 | 11 | 10 |

Scale and solver changed together, so their effects cannot be separated. Combined, they raised reading (files arm +149% bytes, +44% calls) and truncation. They introduced the first wrong-entity error: a near-duplicate project substituted on the absence task. Required-fact recall did not move. Under this rubric, the arms are separated mainly by grounded answer discipline (staying on the named entity, citing every claim), not by whether they found the evidence.

## Does KB help at scale with a weaker solver?

Not in the tested form. With the full guide, the KB arm behaved as a file-search agent carrying a 9.8 KB preamble: same recall, slightly more bytes, worse strict score. With a minimal guide, KB commands were used half the time, and that arm had the best strict score and lowest reading. However, the advantage does not line up with the trials where KB commands were used. Both results come from one run per cell, so they cannot be distinguished from noise.

## Integrity and execution

- 24/24 solver processes exited 0 with parseable JSON answers, and none failed or timed out (wall time 9.6–23.1 s). No trial was rerun.
- Every vault was byte-identical afterwards, with no extra knowledge files. KB runs wrote only operational `.kb/` cache and access-log files.
- The transcript audit found 0 permission denials and no tool other than Bash; the solver was offered only Bash. Two Bash commands did more than call the adapter:
  - A T02 ran `echo noop`, a harmless builtin that Claude Code auto-approves.
  - A T06 ran `bun …/tool.mjs search … 0 --help 2>/dev/null | head -0; bun …/tool.mjs read wiki/meridian-handover.md 77 400`. Claude Code allowed this compound command because each part matched the adapter rule or is a read-only builtin. Both adapter calls were recorded and budgeted, and `head` hid the first call's output from the model. No corpus content was read outside the adapter.
- No hook or session-start context appeared in any transcript (`--safe-mode`). The init event still lists installed plugins and skills, but with only the Bash tool offered, none was invocable.

## Deviations from protocol

1. None in task wording, gold, guides, budgets or scoring. The scripts ran as frozen.
2. The solver cwd is an empty per-trial directory rather than the trial directory. This was decided and documented before the run, after a probe showed read-only commands are auto-approved inside the cwd.
3. The prompt's adapter allow rule did not block compound commands made only of allowed parts (A T06 above). This is recorded, and the visible bytes are not corrected.

## Limits

- One run per cell, 8 designer-authored tasks reused from the pilot, one solver model, one agent assessor without human calibration. No statistical claim is supported, and every arm-level difference here is within what reruns could reverse.
- The scale effect is confounded with the change from Opus to Haiku.
- The strict rubric is literal. Most failures are uncited extra remarks, so strict success here mostly measures citation discipline. The assessor was also inconsistent on two near-identical T02 answers.
- The distractors are templated near-duplicates (`-Sandbox`, `-Legacy`, synthetic names) and do not represent real vault diversity. Wall and tool times come from a shared host and are descriptive only.

## Next experiment

- Run ≥5 repeats per cell (120+ trials) before reading anything into B vs C. Add a 4th arm (full guide, no KB commands) to complete the 2×2.
- Hold the solver fixed across scale tiers (Haiku at 120 and 1,000 pages) to separate scale from model.
- Pre-register a secondary "substantive success" score (facts, entity and behavior correct, ignoring uncited extra remarks) next to the strict score, with human calibration on a sample.

## Path redaction

Before publishing, absolute local paths in `frozen-manifest.json` and `measurements.json` were replaced with `$RUN_ROOT`, `$REPO` and `$HOME`. No other content changed. `freeze.json` keeps its original `manifest_sha256` (c9a8fc68…), which is the hash of the unredacted manifest kept outside the repository. The redacted manifest committed here hashes to `77d058fee5c73d66eb74c68b6ffdf933cadc925ae1363e234dfdd4e42162ac75`.

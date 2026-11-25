# Results: KB versus file search, 8-task paired pilot

Run 2026-10-30. 16 trials (8 tasks × 2 arms), one fresh Claude Code subagent per trial, all on the same inherited model (claude-opus-5-5). Fixtures were regenerated with the frozen generator, and every hash matches `freeze.json`. Raw data: `results.json`, `measurements.json`, `assessments.json`, `answers/`. The blinding key is `blind-key.json`.

## Headline

**Answer quality: tie.** Each arm had 6/8 strict successes, 100% gold-fact recall, 20/20 gold evidence lines exposed, no wrong or superseded facts, and correct conflict, temporal and abstention behavior on every task. The two failures in each arm were the **same tasks** (T01, T06). Each failed on one extra claim its cited span did not literally support, never on a wrong answer.

**Cost: the KB arm read more for the same outcome.** It returned 192 KB of visible output against 104 KB (+84%), made 41 adapter calls against 34, and used 537 ms of tool time against 239 ms. About 8.8 KB per trial of that difference is the larger KB guide (SKILL.md + query.md, 9.8 KB, against a 1 KB file-tools guide). After subtracting the 8,824-byte guide difference, the KB arm still read more on 6 of 8 tasks (most on T05 +7.3 KB and T06 +15.6 KB) and less on T07 and T08.

**KB tooling was barely used.** The KB arm made only 5 KB-CLI calls in 41, all `kb map`, and never used `get-node`, `get` or `recall`. Two KB trials (T03, T07) used no KB command at all. In practice the KB arm acted as a file-search agent with a longer system prompt.

## Paired table

| Task | Family | Files ✓ | KB ✓ | Files calls / bytes | KB calls / bytes | Δ bytes (KB−Files) |
|---|---|---|---|---|---|---|
| T01 | scoped exact | ✗¹ | ✗¹ | 4 / 18,486 | 6 / 28,962 | +10,476 |
| T02 | paraphrase | ✓ | ✓ | 4 / 17,953 | 6 / 28,708 | +10,755 |
| T03 | temporal update | ✓ | ✓ | 2 / 2,664 | 4 / 13,272 | +10,608 |
| T04 | cross-page | ✓ | ✓ | 4 / 3,390 | 5 / 12,782 | +9,392 |
| T05 | exhaustive list | ✓ | ✓ | 3 / 7,849 | 5 / 23,936 | +16,087 |
| T06 | long document | ✗² | ✗² | 5 / 19,427 | 6 / 43,859 | +24,432 |
| T07 | conflict | ✓ | ✓ | 4 / 5,180 | 3 / 12,083 | +6,903 |
| T08 | absent (abstain) | ✓ | ✓ | 8 / 29,481 | 6 / 28,824 | −657 |

¹ Both answers said Nacre-Lab is "separate". The source says only "disposable demo". Under the fixed rubric that claim counts as unsupported.
² Both answers said "1,000 historical exercise lines are all rehearsals" but cited only one line. The facts in both answers are correct.

These four strict failures come from the rubric being literal, not from retrieval errors. They are reported as scored, and the rubric was not changed after seeing them.

## Integrity and execution

- The corpus was byte-identical after all 16 trials, with no extra files. The audit of subagent transcripts found no tool use outside the adapter.
- Non-zero exits: T08 searches with no matches (rg exit 1, expected for that question), and one look-ahead regex in each T06 arm, which rg does not support.
- Truncated outputs: 3 in the files arm, 5 in the KB arm. None prevented a correct answer.

## What this does and does not show

- It does **not** show that KB improves recall or task success over a capable file-search agent. On this corpus (120 pages, about 210 KB) both arms hit the ceiling.
- It does show that **at this scale the KB workflow added reading cost without an accuracy gain**, and that agents given KB mostly skipped KB-specific retrieval.
- The tasks were authored by the experiment designer, there is one run per arm, there is one model, and grading was done by a single agent assessor without human calibration. No statistical claim is supported.

## Deviations from protocol

1. The Codex run stopped when Codex workspace credits ran out (3 trials complete, 2 cut off, 11 never started). All 16 trials were rerun on Claude so each pair uses one model. The Codex partial answers are archived in `answers-codex-partial/` and are not scored.
2. The dispatch template was adapted from the Codex wording to Claude Code's Bash tool, with identical wording across arms.
3. Claude subagents inherit user and project instructions and may receive SessionStart context from the user's real vault. This context was not controlled, but it was equal across arms.
4. The adapter records start and end corpus hashes but no hash per call.

## Next experiment (what would actually discriminate)

- **Scale tiers**: 1k–10k pages, where full-text regex returns floods and truncation forces navigation choices. This is the regime where a curated map or recall could pay off.
- **Ask the questions agents get wrong**: weaker or cheaper solver models, or tighter byte budgets (for example 16 KB), so the ceiling stops masking differences.
- **Separate guide cost from retrieval**: add an arm with the KB guide but no KB commands, and an arm with KB commands but a minimal guide.
- Several runs per cell, independently authored tasks, and human-calibrated grading of a sample.

## Path redaction

Added to the repository with the 2026-11-25 follow-on. Absolute local paths in the manifests were replaced with `$REPO`, `$TMP` and `$HOME` before publishing; no other content changed. Any hash recorded in `freeze.json` refers to the unredacted local files.

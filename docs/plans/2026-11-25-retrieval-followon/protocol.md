# KB retrieval follow-on (APP-17): scale tier, weaker solver, guide ablation

Frozen 2026-11-25 before any solver trial. Follows `docs/plans/2026-10-26-retrieval-control/` (the pilot), whose "Next experiment" section motivated this design.

## Questions

1. **Scale × weaker solver.** On a ~1,000-page corpus where regex search floods with near-duplicates, and with a cheaper solver (Claude Haiku), does the KB arm answer better than a capable file-search arm?
2. **Guide versus commands.** The pilot's KB arm read +84% more bytes. Is that extra reading caused by the long KB guide or by using KB commands? Arm B (full guide) versus arm C (same commands, short guide) separates the two.

This estimates retrieval/workflow value conditional on an already curated vault. It does not measure curation, extraction or summarization. Tasks are designer-authored and copied from the pilot; this is a feasibility follow-on, not a held-out benchmark. No KB code, task wording, gold, guide or scoring may be tuned after observing results.

## Corpus (scale tier)

`prepare.mjs` contains the pilot generator verbatim for the original 120 wiki pages, `KB.md` and `context.md`, and asserts that those 122 files are byte-identical to the pilot freeze (subset hash `2848915e…7767`). It adds 98 distractor projects × 9 near-duplicate pages = 882 pages, for **1,002 wiki pages, about 1.47 MB**. `index.md` is regenerated over all pages.

- 16 near-name projects (`<Target>-Sandbox`, `<Target>-Legacy` for Nacre, Corundum, Larch, Plover, Mariner, Meridian, Helix, Cinder) and 82 synthetic project names.
- Each project gets pages mirroring every task family with different values: retry policy, audit retention with steward and "missing parcels", pilot schedule with a superseded target, EU export suspension with its own roster (release custodian, incident lead), launch blockers with OPEN/CLOSED status, a handover with 60 historical exercise lines plus an approved checkpoint and a vendor quote, supplier renewal (confirmed or conflicting), and a service page where half the projects record a penetration-test signatory and fee.
- Asserted at generation: every gold sentence occurs exactly once in the corpus (distractors name their own project in every analogous sentence), and nothing records a penetration-test engagement for plain `Cinder`.

## Arms

All arms use the pilot's unchanged `tool.mjs` adapter (≤16 calls, ≤128 KiB visible output, 16 KiB per call, guide counted), fresh corpus copies, the same file operations (`files`, `search`, `read`) and the same dispatch prompt.

| Arm | KB commands | Guide | Guide bytes |
|---|---|---|---|
| A `files` | no | pilot files guide, byte-identical | 962 |
| B `kb-full` | yes (`map`, `get-node`, `get`, `recall`, `list-topics`) | pilot KB guide, byte-identical (files guide + SKILL.md + query.md at commit 8b0de6f) | 9,786 |
| C `kb-minimal` | yes (same) | new short guide naming KB commands and file fallback | 975 (within 5% of A) |

The KB CLI is the current checkout's `src/cli.ts` (curated retrieval commands unchanged since the pilot commit; later commits add `capture` and `remote`, which the adapter does not expose). Each trial starts with no `.kb` cache.

## Tasks and gold

The pilot's eight tasks (T01–T08), question wording, gold facts and T08 abstention rule are copied verbatim. Gold line numbers are recomputed from the unchanged pages and are identical to the pilot.

## Solver

Claude Haiku via headless Claude Code, one fresh process per trial, 24 trials (8 tasks × 3 arms × 1 run), at most 4 concurrent, in the seeded interleaved order from `prepare.mjs`:

```
claude -p --model haiku --safe-mode --tools Bash \
  --allowedTools "Bash(bun <trial>/tool.mjs:*)" --permission-mode dontAsk \
  --no-session-persistence --strict-mcp-config --output-format stream-json --verbose "<dispatch prompt>"
```

- `--safe-mode` disables CLAUDE.md, skills, plugins, hooks and MCP; `--tools Bash` removes every other built-in tool (Read, Grep, WebFetch, Agent and so on); `dontAsk` denies any Bash command not matching the adapter prefix, including compound commands.
- **cwd is an empty per-trial work directory, not the trial directory.** A probe showed Claude Code auto-approves read-only commands such as `cat` inside its cwd, which would have exposed the vault outside the adapter. With an empty cwd, `cat`, `grep` and `bun -e` on the trial path were denied. The prompt passes the absolute adapter path. This is a deliberate deviation from "cwd = trial dir".
- Every transcript is audited for tool use other than an adapter invocation and for permission denials.
- Per-trial timeout 600 s. A failed or timed-out trial is recorded as failed and not rerun silently; a rerun, if any, is reported as a deviation.
- Token usage and cost are taken from the CLI's result event when present, otherwise null.

## Measurement (mechanical, by script)

Per trial: adapter calls, KB-command calls by subcommand, visible bytes (guide included), visible bytes excluding the guide, raw bytes, truncated outputs, non-zero exits, tool time, gold facts exposed, corpus integrity, off-adapter tool uses and permission denials.

**Evidence exposure:** a gold fact is exposed when its exact sentence appears in a non-guide visible adapter output. Because each gold sentence is unique in the corpus, a literal match identifies its source.

## Grading (blinded)

The pilot method: each answer gets an opaque assessor ID; the assessor sees the question, category, gold facts, abstention rule, answer, limitations and the corpus text of every cited span (extracted by script), and never sees arm, trial ID, transcript or guide. One `claude -p --model sonnet --safe-mode --tools ""` call per answer, in shuffled order, with this frozen rubric:

- `facts_correct` / `facts_required`: required gold facts correctly stated.
- `claims_total`, `claims_supported`, `unsupported_claims`: material factual claims, and those supported by the cited span text.
- `wrong_or_superseded`: any wrong-entity, wrong-value or superseded fact presented as the answer.
- `citation_support`: share of material claims supported by an attached citation.
- `behavior_ok`: correct temporal, conflict or abstention behavior for the category.
- `strict_success`: all required facts correct, every material claim supported, no wrong/superseded fact, behavior correct. For T08 (empty gold), success means abstaining on both signatory and fee without substituting another person or project, inventing a fee, or claiming no engagement happened.

The assessor's full stdout is stored verbatim. Root checks mechanical counts. Agent grading is not human-calibrated ground truth.

## Reporting

`results.md` reports: a paired task × arm table (strict success, fact recall, visible bytes, calls, KB-command usage), per-arm totals, the B-versus-C comparison (bytes with and without the guide, KB-command use, accuracy), the scale effect against the pilot (pilot used Opus on 120 pages, so model and scale are confounded; stated as such), every failure, and limits.

Limits fixed in advance: one run per cell, eight designer-authored tasks, one solver model, one agent assessor, shared-host concurrency (timing descriptive only). No p-values or statistical claims.

## Safety

Synthetic data only. No real vault, session or transcript is read. No production, Railway, network tools, push or PR.

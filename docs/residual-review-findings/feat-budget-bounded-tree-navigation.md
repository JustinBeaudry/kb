# Residual Review Findings — feat/budget-bounded-tree-navigation

**Status: resolved.** All six issues (#11–#16) were closed by branch `fix/close-open-issue-backlog` (2026-06-12).

Source: ce-code-review autofix run `20260612-094233-25ab026d` (2026-06-12), reviewing the budget-bounded tree-navigation branch against `840f49a`. Seven safe fixes were applied in-branch (`fix(review): apply autofix feedback`); the findings below were validated but not auto-applied, and each is filed as a tracked issue.

## Residual Review Findings

- [P2] `src/lib/map/node-id.ts:40` — Ordinal node-ID suffix collides with natural `-2` slugs, producing duplicate node IDs — [#11](https://github.com/JustinBeaudry/kb/issues/11)
- [P2] `src/commands/map.ts:100` — Section-only candidates under a tight budget emit an empty envelope without `no_results`/suggestions — [#12](https://github.com/JustinBeaudry/kb/issues/12)
- [P2] `src/lib/map/builder.ts:113` — Wikilinks before the first heading (or on heading-free pages) dropped from the link graph — [#13](https://github.com/JustinBeaudry/kb/issues/13)
- [P2] `src/lib/map/cache.ts:74` — statSync ENOENT race crashes map/get-node when a file is deleted mid-scan — [#14](https://github.com/JustinBeaudry/kb/issues/14)
- [P2] `src/lib/qmd.ts:67` — `qmd search` hint subprocess has no timeout on the hot `kb map` path — [#15](https://github.com/JustinBeaudry/kb/issues/15)
- [P2] `src/lib/qmd.ts:84` — qmd output-parsing/normalization boundary has zero test coverage — [#16](https://github.com/JustinBeaudry/kb/issues/16)

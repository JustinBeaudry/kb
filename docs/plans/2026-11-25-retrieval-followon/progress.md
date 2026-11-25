# Progress

- Scope: synthetic follow-on to the retrieval pilot (Linear APP-17); production unchanged.
- 2026-11-25: Protocol written. Generator extended to 1,002 pages; pilot pages, tasks, gold and both pilot guides verified byte-identical. Harness probe found that read-only shell commands are auto-approved inside the solver cwd, so solvers run from an empty cwd. Smoke run on a non-task question passed. Fixtures, scripts and schedule frozen in `freeze.json` before any solver trial.
- 2026-11-25: Freeze committed (13be34d). 24/24 Haiku trials completed, no failures or reruns. Measured, then blindly graded by Sonnet (24 calls).
- Result: fact recall 100% and evidence 20/20 in every arm. Strict success: files 3/8, kb-full 1/8, kb-minimal 5/8. With the full guide, Haiku never ran a KB command; its extra bytes are the guide. See results.md.
- Next: repeats per cell, a guide-only arm to complete the 2×2, and fixed-solver scale tiers (results.md § Next experiment). This needs user direction.

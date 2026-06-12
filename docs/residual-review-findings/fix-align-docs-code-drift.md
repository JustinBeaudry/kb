# Residual Review Findings — fix/align-docs-code-drift

Source: `ce-code-review mode:autofix` run `20260611-225511-2ba3638b` (2026-06-11),
plan `docs/plans/2026-06-11-001-fix-align-docs-code-drift-plan.md`.
15 findings were fixed in-branch (commit `cf79222`); the items below are the
durable record of what was deferred or left as report-only.

## Residual Review Findings

- **[P3]** `src/commands/summarize.ts:8` — summarize and summaries lack `--vault-path`/`-p` flag other commands accept — filed: <https://github.com/JustinBeaudry/kb/issues/9>

### Report-only (no action required; recorded for visibility)

- **[P2]** `skills/extract/SKILL.md:56` — the documented extract/refine flows pass `--approve` unconditionally, making the ask gate on untrusted session-summary reads agent-self-approved. Policy decision for a human: require user-in-conversation approval, or stop describing these reads as "ask-gated" and document bounded excerpts + envelope trust marking as the actual control.
- **[P2]** `src/lib/access-log.ts:64` — `AccessLogEntry` and `WriteAuditEntry` both reach `appendMinimalJsonl` through `as unknown as Record<string, unknown>` casts; a generic constraint on `appendMinimalJsonl` would restore compile-time shape checking.
- **[P3]** `src/lib/session-state.ts:41` — manifests with malformed YAML are silently skipped by listings and the nudge; only `kb doctor` surfaces them. Optional: a names-free skipped-count notice on stderr.
- **[P3, pre-existing]** `src/lib/atomic-write.ts:9` — a crash between tmp write and rename leaves `*.tmp-*` litter in `sessions/` that nothing cleans; candidate `kb doctor` age-gated sweep.

# R1 stage 2: Retrieval and startup review

**CHANGES REQUIRED: two introduced P2 bugs.** Production code and tests remain unchanged for owner/root RED-first correction.

Reviewed the assigned retrieval commands/helpers, injection changes, relevant tests, `graph.md`, I2 and stage-1 reports against base `2702a532d330d53bbc8e54ba18fb0e09e8631e0b`. Applied the gstack review checklist as a lens. Probes used disposable synthetic vaults only; no real vault, user configuration, optional external tool, or model was used. No full suite was rerun.

## R1-R1: P2 — continuation argv loses leading-dash positionals

**Locations:** `src/lib/retrieval.ts:45-51`, `src/commands/get.ts:96`, `src/commands/recall.ts:112`.

The continuation builder mixes positional values and options in one array, then appends the budget/vault options. A valid page name or query beginning with `-` becomes an option on replay. This breaks the promised executable continuation even when the vault is unchanged.

Reproduced with a temporary vault containing `wiki/-guide.md` and `wiki/normal.md`, each holding `# Guide\n` followed by 100 copies of `needle --flag evidence\n`:

```sh
bun src/cli.ts get --budget 800 -p "$fixture" -- -guide
bun src/cli.ts recall --budget 900 -p "$fixture" -- --flag
```

Both initial calls exited 0 and produced bounded envelopes with continuations. Replaying exactly `[continuation.command, ...continuation.args]` failed:

- `get` emitted `args: ["-guide", "--start-line", "18", "--budget", "800", "-p", "<absolute fixture>"]`; replay exited 1 with `Missing required positional argument: PAGE`.
- `recall` emitted `args: ["--flag", "--offset", "2", "--budget", "900", "-p", "<absolute fixture>"]`; replay exited 1 with `Missing required positional argument: QUERY`.

The parser also printed help text on stdout, so the replay did not produce an evidence envelope. Initial `--` handling already works; the bug is introduced by the new continuation assembly.

**Minimal fix:** give the continuation builder separate typed options/positionals, emit all options (including budget/vault) before `--`, then the positional values. Preserve exact strings; shell quoting inside an argv array is not a fix. Add replay tests for both leading-dash page names and queries, alongside the existing normal continuation cases.

## R1-R2: P2 — bounded recall output now materializes the entire matching corpus

**Locations:** `src/commands/recall.ts:82-95`, `src/lib/retrieval.ts:102-119`.

`recall` now constructs every snippet from every matching file before fitting its first page. `fitChunkPage` then serializes the entire remaining result set, and its binary search serializes large prefixes again. This makes the memory and first-page work grow with all matches even when the first 16 KiB page is already full. The base stopped collecting after 20 results; removing that silent limit is required, but retaining all remaining snippets is unnecessary for continuation.

**Measured comparison:** 64 synthetic wiki files, each `# Probe\n` plus 1,000 copies of `needle ` + ten copies of `evidence ` + `\n`. Each file is 98,008 bytes, below the existing per-file cap; the vault holds 6,272,512 source bytes and 64,000 matches. Ran `bun src/cli.ts recall needle -p <fixture>` against an exact temporary archive of the base and the current working tree, with both budget environment overrides cleared. Three subprocess runs per version; `/usr/bin/time -l` measured per-process peak RSS.

| Version | Median wall time | Median peak RSS | Output bytes | Returned chunks |
|---|---:|---:|---:|---:|
| Base | 25.1 ms | 30,834,688 (29.4 MiB) | 14,930 | 20 |
| Current | 118.0 ms | 348,274,688 (332.1 MiB) | 15,895 | 21 |

All six representative runs exited 0. Current wall times were 117.2–118.4 ms and RSS 341,737,472–363,610,112 bytes; base wall times were 24.8–25.4 ms and RSS 30,539,776–30,851,072 bytes. The observed regression is about 11 times peak memory and 4.7 times elapsed time for similarly sized responses. No out-of-memory failure is claimed. An initial archive omitted required non-source assets and failed before running the baseline command; those samples were discarded, then the full archive comparison above succeeded. Temporary fixtures/archives were removed.

**Minimal fix:** consume matches in deterministic order, skip offsets without retaining snippets, and collect only the bounded page plus enough lookahead to establish continuation. Avoid serializing every remaining result to discover that it exceeds the cap. Preserve whole-result output, exact offsets, and the explicit oversized-result remedy; do not restore the silent 20-result cutoff. Use a boundary-level test that shows later matching files/results are not consumed once the page and lookahead are known, then repeat this small synthetic measurement to validate the correction.

## Other reviewed behavior

No additional introduced defect was established in complete-wire accounting, exact line/Unicode preservation, section range checks, optional-node omission, existing safe-path restrictions, map degradation, or lazy/explicit-eager startup behavior. Continuations intentionally assume an unchanged vault; snapshot services and unrelated preexisting path/reader behavior are outside this review. Existing full-suite results remain root-owned evidence, not a new test claim from this review.

## Resolution review — PASS

**Both P2 findings are resolved. No unresolved finding remains in this focused retrieval/startup review; C3 may proceed.** The original findings above are retained as the correction record.

- **R1-R1 resolved:** `continuation` now accepts separate typed options and positionals, emits command options plus the shared budget/vault options first, and places positional values after `--`. Inspected all four callers (`get`, `get-node`, `recall`, `list-topics`) to confirm their options and positional values use the new contract correctly. The new CLI replay tests cover a leading-dash page and query, preserve exact line/path order across pages, and require a real continuation in the recall fixture. The strengthened recall fixture was established before production correction.
- **R1-R2 resolved:** `fitChunkPage` now consumes an `Iterable` incrementally. It discards skipped offsets, retains a bounded page plus lookahead, and returns the last fitting continuation response when the next complete response exceeds the budget. `recall` supplies nested generators and yields snippets lazily rather than building all matching-file results. Final small responses still avoid unnecessary continuation metadata; offset exhaustion, empty responses, and oversized-result remedies remain explicit. The new boundary tests enforce bounded consumption at offsets 0 and 40 and cover those completion/error cases.

Read the complete saved `evidence/retrieval-review-red-corrected.log` and `evidence/retrieval-review-green.log`: the corrected RED run records **31 pass / 11 fail**, including both CLI replay failures; the GREEN run records **128 pass / 0 fail / 784 assertions across 10 files**. This re-review inspected those execution records and current tests; it did not rerun a suite.

Inspected `evidence/profile-recall.mjs` and all three `evidence/recall-profile-{base,before,after}.json` records. Root's independent comparison uses the same 64-file / 6,272,512-byte / 64,000-match fixture, three runs per version, an empty PATH, and no inherited credentials. Every subprocess must exit successfully and pass wire-framing validation before a sample is saved. Its measured medians are:

| Version | Median wall time | Median peak RSS | Output bytes | Returned chunks |
|---|---:|---:|---:|---:|
| Original base | 27.96 ms | 31,653,888 | 14,910 | 20 |
| Before correction | 127.60 ms | 348,749,824 | 15,871 | 21 |
| After correction | 29.45 ms | 32,620,544 | 15,876 | 21 |

This confirms the reported first-page allocation regression is removed on the representative fixture while preserving bounded, continued output. It is not a broad performance benchmark. No new benchmark, production edit, test edit, full-suite run, or subagent was performed during this bounded re-review. Root retains the final C3 verification gate.

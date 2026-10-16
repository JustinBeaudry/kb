# I2 correction 1: Executable continuations and lazy recall pages

Status: root targeted GREEN and performance checks passed; bounded re-review and final full gates follow. No worker commits made. Only the assigned retrieval sources, regression tests, and this report were changed by this correction.

## Findings addressed

- **R1-R1:** continuation construction now accepts separate typed option and positional arrays. It writes every option, including budget and absolute vault path, before `--` and the unchanged positional values. `get`, `get-node`, `recall`, and `list-topics` use the new shape. This preserves page names and queries beginning with a dash during direct argv replay.
- **R1-R2:** `recall` yields matching snippets in deterministic sorted-path and source-line order. It no longer retains arrays of per-file hits or all matching chunks. The existing safe walker still collects path names to establish the same ordering; file contents are read one file at a time through the existing capped, no-follow reader.
- `fitChunkPage` accepts an `Iterable<EnvelopeChunk>`, skips offset results without retaining them, and retains only candidates for the bounded page and overflow lookahead. It serializes bounded candidates rather than the full remaining corpus. Once a complete candidate exceeds the cap, it returns the latest fitting whole-result prefix and its exact continuation offset.
- The fitter continues while a complete response still fits, even if continuation metadata would not fit. If the producer ends, all of that small response is returned without unnecessary truncation. Empty pages, offsets at or past the end, and indivisible oversized-result remedies retain explicit behavior. The silent twenty-result cutoff was not restored.

## RED evidence

Production changes waited for root's accepted failing run:

```sh
bun test tests/retrieval-budgets.test.ts tests/retrieval-page.test.ts
```

`../evidence/retrieval-review-red-corrected.log` records **31 pass, 11 fail, 372 assertions**. Both CLI regressions succeeded on the initial request, then failed replay. The initial recall fixture had fit in one response; its corrected 700-byte budget now explicitly requires a first-page continuation and more than one successful page. The nine helper cases exposed the existing array-only implementation when given iterable producers.

The new tests cover:

- Actual `get -- -guide` and `recall -- --flag` continuation execution, complete output caps, and exact source ordering/provenance.
- A generator producer with a counter and sentinel at offsets 0 and 40, proving that reads stop after the bounded page plus lookahead rather than consuming later results. No language primitives or the function under test are mocked.
- Complete small envelopes, final results, empty producers, offsets equal to and greater than the available count, impossible empty-envelope budgets, and replay of the recommended budget for an oversized whole result.

## Validation handoff

Root ran and read the targeted GREEN gate: **128 pass, 0 fail, 784 assertions across 10 files**, saved in `../evidence/retrieval-review-green.log`. Root owns the final strict TypeScript/full-suite/evaluation gates and independent re-review.

Root's independent pre-correction probe used 64 files, 6,272,512 source bytes, and 64,000 matches, with three subprocess samples per version. The saved median peak RSS was 348,749,824 bytes before correction and 31,653,888 bytes at the original base. See `../evidence/recall-profile-before.json` and `../evidence/recall-profile-base.json`; these are observed comparisons, not test thresholds.

The same disposable macOS probe can measure this correction without changing source or a real vault:

```sh
bun docs/plans/2026-10-16-portable-agent-workflows/evidence/profile-recall.mjs . after-review-correction docs/plans/2026-10-16-portable-agent-workflows/evidence/recall-profile-after.json
```

The helper suite intentionally uses behavioral producer bounds rather than timing or RSS assertions. Large offsets still require walking preceding matches, but their snippets are discarded as they are skipped. Continuations continue to assume an unchanged vault.

Root's post-correction profile passed all three subprocess runs: median peak RSS **32,620,544 bytes (31.1 MiB)** and wall time **29.45 ms**, compared with **348,749,824 bytes (332.6 MiB)** and **127.60 ms** before the correction. Original base was **31,653,888 bytes** and **27.96 ms**, but silently stopped at20 results. The corrected result returns21 chunks within15,876 wire bytes with continuation. These are observations for this synthetic macOS fixture, not a general latency guarantee; complete raw measurements are retained in the three profile JSON files.

# R1 transcript/provider code review

Status: **PASS after correction.** The original P2 finding below is preserved as review history and is resolved by the bounded re-review at the end of this report.

Reviewed the working tree, including untracked files, against `2702a532d330d53bbc8e54ba18fb0e09e8631e0b`. Scope: `src/lib/{transcript,excerpt,summarizer,git}.ts`, changed related tests, and immediate supporting code. Applied the gstack review checklist lens. Read the approved graph, I1 limits, and stage-1 report. No production code, tests, or other reports were edited.

## P2: malformed message discriminators silently retain complete-source status

Locations: `src/lib/transcript.ts:32-35`, `:40-46`, and `:62`; consequence at `src/lib/summarizer.ts:231-238`.

The new decoder returns `{ turn: null, malformed: false }` for an explicitly recognized Codex or generic message whose role is missing or non-string. It does the same for a supported user message containing a content-block object with no `type`. These are malformed conversation records, not valid metadata/tool records. When another usable turn exists, summarization drops their content and writes a cache with `degraded: false`, violating the graph's mixed-valid/malformed-source contract.

Reproduced with synthetic hash-valid JSONL files and a local provider returning valid required sections. Each file contained one of the malformed records below followed by `{ "role": "assistant", "content": "SURVIVING_TEXT" }`. Cases 1-4 each returned `cached: false, degraded: false`; saved frontmatter also reported no degradation. Case 5 was independently confirmed through the decoder and follows the same summary path.

| Case | Malformed record | Observed decoder result |
|---|---|---|
| 1 | `{ "type": "response_item", "payload": { "type": "message", "content": [{ "type": "input_text", "text": "LOST_TEXT" }] } }` | `turn: null, malformed: false` |
| 2 | Same Codex message with `"role": 42` | `turn: null, malformed: false` |
| 3 | `{ "type": "message", "role": 42, "content": "LOST_TEXT" }` | `turn: null, malformed: false` |
| 4 | `{ "role": "user", "content": [{ "text": "LOST_TEXT" }] }` | `turn: null, malformed: false` |
| 5 | `{ "type": "message", "content": "LOST_TEXT" }` | `turn: null, malformed: false` |

Small direct reproduction from the repository root:

```sh
bun -e 'import { decodeTranscriptLine } from "./src/lib/transcript.ts"; console.log(decodeTranscriptLine(JSON.stringify({type:"response_item",payload:{type:"message",content:[{type:"input_text",text:"LOST_TEXT"}]}})))'
```

Minimum fix: distinguish malformed discriminator fields from deliberately ignored valid roles/types. For records explicitly marked as messages, missing/non-string roles must set `malformed: true`. For content-block objects inside a supported conversation turn, missing/non-string block types must set `malformed: true`. Continue ignoring valid `system`, `developer`, and `tool` roles, typed non-text blocks, unrelated metadata, and mirrored events. Add table-driven integration coverage asserting retained valid text plus `degraded: true` in both the result and cache, alongside valid-ignore controls. The existing malformed-content test only exercises `content: 42`, so it misses these branches.

This finding concerns classification in the newly introduced portable decoder and its new degraded-source contract. It is not a claim that baseline supported these formats correctly.

## Verification and limits

- Ran `bun test tests/transcript-portability.test.ts tests/summarizer-portability.test.ts tests/git.test.ts`: **39 pass, 0 fail, 114 assertions**, Bun 1.4.3. These passing tests do not cover the finding above.
- Exercised a local provider that exited successfully after starting a child holding inherited stdout/stderr. With a 500 ms provider timeout, summarization rejected after **509 ms**, emitted the actionable timeout diagnostic, and wrote no cache. The probe explicitly killed its remaining child and removed its temporary fixture. No cleanup defect found.
- The passing focused tests cover argv literal handling, unsupported-source fallback/failure, invalid-output and timeout cache preservation, optional missing Git, and propagation of an unrelated bad-working-directory error.
- No further concrete introduced issues found in the owned paths. No new unjustified TypeScript casts were found there.
- Per-provider deadlines, detached-descendant process management, unchanged recursive reduction, and concurrent refresh locking remain the accepted scope limits. No full-suite, real-vault, external-provider, configuration, or network operation was performed by this review.

## Correction re-review: resolved / PASS

Inspected the correction in `src/lib/transcript.ts` and the added regression cases in `tests/summarizer-portability.test.ts`. The Codex message branch now classifies missing/non-string roles as malformed at line 32; the explicit generic message branch does the same at lines 39-40; the content decoder now classifies missing/non-string block types as malformed at lines 63-65. Supported text still flows through the existing decoder. Valid ignored roles, typed non-text blocks, metadata, and mirrored events retain their prior behavior. The correction adds no casts or unrelated production changes.

The eight new malformed cases assert surviving conversation text, preservation of valid neighboring blocks where present, exclusion of malformed text, use of the transcript rather than its fallback, and `degraded: true` in both the CLI result and saved cache. Five control cases assert intentionally ignored records still yield `degraded: false` and do not reach the provider prompt.

Read both saved logs completely. `evidence/transcript-review-red-corrected.log` shows **5 pass / 8 fail**, with all eight failures at the expected degradation assertion before the source correction. `evidence/transcript-review-green.log` shows **68 pass / 0 fail / 1,887 assertions across five related files**, including all thirteen added cases, transcript excerpts, capture, existing summary behavior, and hooks.

The single P2 finding is resolved. No new issue was identified in this focused correction. This re-review ran no additional tests or broad searches and changed only this report. Root's C3 lint/full-suite gate remains the final integration check.

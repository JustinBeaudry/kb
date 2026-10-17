# I1 correction 3: Malformed transcript discriminators

Status: root targeted GREEN passed and R1 re-review accepted the correction. This is I1's final bounded correction attempt. No worker gates or commits were run.

## Regression contract

Added eight table-driven CLI integration cases to `tests/summarizer-portability.test.ts` using the existing local provider and temporary-session fixtures:

- Explicit Codex and generic messages with missing or non-string roles.
- Supported Claude, Codex, and generic content arrays with missing or non-string block types.

Each case requires usable text from another turn to reach the provider, malformed text to be excluded, and both the CLI result and saved summary to report `degraded: true`. Content-block cases also require valid neighboring text blocks to survive. The fallback excerpt must not replace the usable transcript.

Five controls require complete-source status while ignoring valid Codex and generic system/developer/tool roles, typed non-text blocks, unrelated metadata/tool records, and mirrored Codex events. These also check that valid conversation text survives and ignored text is excluded.

## RED evidence and test correction

Root ran and read `evidence/transcript-review-red.log`. All eight malformed-discriminator cases failed because the CLI returned `degraded: false`, reproducing R1's finding. The five controls initially failed on the cache assertion because complete summaries intentionally omit the optional `degraded` frontmatter field. The tests incorrectly required a literal `false` there.

Corrected only that control assertion to treat absent frontmatter as false with `data.degraded ?? false`; the CLI result still must explicitly report `degraded: false`. Root reran and read `evidence/transcript-review-red-corrected.log`: five controls passed and all eight malformed cases failed as expected. Root released production edits only after accepting this RED gate.

## Minimum production correction

`src/lib/transcript.ts` now marks an explicitly recognized Codex or generic message malformed when its role is missing or non-string. Supported conversation content arrays also mark block objects malformed when their type is missing or non-string, while retaining valid neighboring text blocks.

String roles outside user/assistant remain deliberately ignored. Typed non-text blocks, unrelated records, and mirrored events retain their existing handling. Untyped arbitrary objects are not newly treated as malformed generic messages. No decoder interfaces, provider behavior, cache serialization, or excerpt behavior changed.

## Verification ownership and limits

Root ran and read the targeted gate: **68 pass, 0 fail, 1887 assertions across five files**, saved in `evidence/transcript-review-green.log`. The original R1 reviewer verified the fix, tests and complete RED/GREEN logs, then marked its report PASS. Root owns the final full gate and evidence claims. The correction uses synthetic local fixtures and does not touch external providers, the user's vault, frozen evaluators or skills.

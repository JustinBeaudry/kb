# Harness capability matrix (C2, synthetic qualification)

This matrix reports what this source checkout has demonstrated for each agent
harness. Each cell is **supported**, **unsupported** or **unverified**, with the
evidence path. "Supported" here means demonstrated only against the synthetic
fixtures in `tests/fixtures/capture/`. No real user transcript, live harness
session, server, or network call was used, and no harness file format was checked
against current official documentation in this task. Live private qualification
happens in O3. A newer harness version may change its format without notice. When
that happens, unknown records are reported as capability gaps (see below), not
guessed.

Capabilities are reported separately, as the design requires:

- **Capture**: the explicitly registered watcher (`src/capture/watcher.ts`) turns
  new complete records into C1 capture events.
- **Capture via explicit poll command**: `kb capture poll --profile <name> --endpoint <origin>
  --adapter codex|claude|generic --source <file> --project <id> --session <id>` polls
  the one selected file into the C1 spool and prints the PollReport as JSON.
- **Native hook capture**: the harness itself calls kb when new records arrive.
- **Automatic restore**: scoped remote context is injected into the harness without
  the agent asking for it.
- **Client-driven restore**: the client calls `restoreContext` or `kb remote orient`.
- **Tool evidence**: tool calls and results are kept as submitted, unverified evidence.
- **Compaction notification**: a harness compaction record becomes a `compaction` signal.
- **Authenticity**: the strongest origin trust the adapter can claim.
- **Interruption recovery**: resume from a cursor, partial trailing records, rotation.

| Capability | Codex | Claude Code | Generic JSONL | Hermes |
| --- | --- | --- | --- | --- |
| Capture (registered watcher) | supported | supported | supported | unverified |
| Capture via explicit poll command | supported | supported | supported | unverified |
| Native hook capture | unverified | unverified | unsupported (no native hooks) | unverified |
| Automatic restore | unsupported | unverified | unsupported (no native hooks) | unverified |
| Client-driven restore | supported | supported | supported | unverified |
| Tool evidence (submitted, unverified) | supported | supported | supported | unverified |
| Compaction notification | supported | supported | supported | unverified |
| Authenticity | submitted only | submitted only | submitted only | unverified |
| Collector-attested / owner-authenticated origin | unsupported | unsupported | unsupported | unsupported |
| Interruption recovery (cursor, partial line, rotation) | supported | supported | supported | unverified |

## Evidence

| Cell | Evidence |
| --- | --- |
| Capture, all three adapters | `tests/capture-adapters.test.ts` (exact role and kind ordering, stable unique IDs, malformed lines) with `tests/fixtures/capture/{codex,claude,generic}.jsonl`; `tests/capture-resume.test.ts` (`captures only records appended after explicit registration`, `maps normalized events onto C1 capture events with submitted origin`) |
| Codex duplicate suppression | `tests/capture-adapters.test.ts`: `deduplicates paired Codex records` (`event_msg` mirrors are ignored, so `response_item` is the single source); call-ID-anchored IDs stay stable when the same call is delivered again |
| Claude duplicate suppression | `tests/capture-resume.test.ts`: `suppresses repeated delivery of the same native records` (IDs come from record UUIDs) |
| Tool evidence | `tests/capture-adapters.test.ts`: `retains tool results without accepting them as verified` (`origin.trust === "submitted"`, `verification === "unverified"`) |
| Authenticity | `tests/capture-adapters.test.ts`: `owner role remains submitted`, including a record that claims `owner_authenticated` |
| Compaction | `tests/capture-adapters.test.ts`: `reports one compaction notification` (Codex `compacted`, Claude `system`/`compact_boundary`, generic `{"type":"compaction"}`) |
| Interruption recovery | `tests/capture-resume.test.ts`: `does not consume incomplete trailing JSON`, `rotation records gap rather than inventing old events`, `detects in-place truncation as rotation`, `resumes from the persisted cursor without duplicate events`, `keeps the cursor in place when the capture sink is unavailable` |
| Client-driven restore | `tests/capture-resume.test.ts`: `scoped context restore` cases (fake C3 boundary: budget, labeling, remote failure without local fallback); `kb remote orient` in `tests/remote-cli.test.ts` (C3) |
| Claude automatic restore | `tests/capture-resume.test.ts`: `remote opt-in never falls back to local vault context` and `local hooks preserve previous output without remote profile`. The SessionStart/PostCompact hook dispatches to remote restore only when `KB_REMOTE_PROFILE` is set. The hook was tested failing closed with an invalid endpoint, but a live remote injection has not been demonstrated, so the cell stays **unverified**. |
| Codex automatic restore | No Codex session-start integration is wired, so this is **unsupported**. It is a visible limitation, not a hook kb pretends to have. Use client-driven restore. |
| Capture via explicit poll command | `tests/capture-poll-command.test.ts`: enqueues new records from the selected file only, refuses without an explicit `--profile` or with a missing/unknown adapter, source, endpoint, project or session before any credential read, and reports a directory source as invalid without reading it. Each adapter is registered by name; the per-adapter decoding evidence is in the rows above. |
| Codex copied history after rotation | `tests/capture-resume.test.ts`: `deduplicates copied Codex history after rotation`; `tests/capture-adapters.test.ts`: `derives Codex message IDs from content that survives a copy` (session, record timestamp, role and text digest) |
| Native hook capture | No harness hook invokes the watcher or `kb capture poll`, so native dispatch is **unverified** for Codex and Claude Code. |
| Hermes | Out of C2 scope; owned by I4. |

## Behavior the cells depend on

- **New records only.** On first sight, a registered source's cursor is placed at
  its current end. Historical records are never captured. The watcher reads only
  the absolute regular-file paths it was given. Symlinks, directories and relative
  paths are refused, and it never lists a directory.
- **Complete records only.** A trailing record without a newline is left
  unconsumed (`pendingBytes`). A record over 8 MiB is dropped with an
  `oversized_line` gap.
- **Rotation.** A changed file identity, shrunk size, or rewritten first 256 bytes
  produces a `rotated` gap and `completeness: "partial"`. The new generation is read
  from its start. Unread records of the old generation are reported as missing,
  never reconstructed.
- **Delivery.** The cursor advances only past records whose events the C1 sink
  accepted. Stable event IDs (also used as the C1 idempotency key), plus a bounded
  recent-ID set, suppress repeats. Claude IDs come from record UUIDs, Codex tool IDs
  from call IDs, and Codex message IDs from session, record timestamp, role and a
  text digest, so history copied into a rotated file is not captured twice. Two
  identical Codex messages with the same timestamp would share one ID. A crash between enqueue and cursor save can
  replay a record. C1/F4 idempotency deduplicates that replay.
- **Path exclusions.** Every event carries the canonical source path
  `harness/<adapter>/<file name>`; the absolute local path is never sent. Server
  or `--exclude-paths` exclusions match it by exact path or directory prefix, so
  `harness/codex` drops all Codex captures and unrelated workspace exclusions such
  as `src` leave transcript events untouched.
- **Hidden content.** Codex `reasoning`, Claude `thinking`/`redacted_thinking`,
  session instructions, compaction summaries, images and other non-text blocks are
  not captured. Omitted non-text parts mark the event `partial` and add a
  `non_text_content_omitted` capability gap.
- **Unknown formats.** An unrecognized record type is reported as
  `unsupported_record:<type>` and produces no event.
- **Legacy excerpts.** The conversation-only parser (`src/lib/transcript.ts`) is
  unchanged and still excludes tool records (`tests/transcript-portability.test.ts`).

## Remote opt-in for hooks

Without `KB_REMOTE_PROFILE`, the hooks produce exactly the previous local output.
With `KB_REMOTE_PROFILE`, `KB_REMOTE_ENDPOINT` and `KB_REMOTE_PROJECT` (and
optionally `KB_REMOTE_TASK`) set, `hooks/inject` restores bounded schema-v3 context
for the explicit profile. That context is labeled as data, not instructions. On
any remote failure the hook returns empty context, never local vault content. The
bearer comes from the OS credential store registered with `kb remote
credential-set`, never from the environment.

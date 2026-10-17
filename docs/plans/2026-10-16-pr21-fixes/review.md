# PR21 correction review

Baseline: `e58f96e404ba2a013863c00b5e85a337fad4150d`; reviewed the uncommitted correction diff, the preventive signal-handler ordering correction, and `tests/summarizer-lifecycle.test.ts`. Review depth: standard, 172 changed non-test lines, with additional lifecycle sequencing analysis. No production or test files changed; no tests or probes run by this reviewer.

## Stage 1: specification compliance

| Requested behavior | Result and evidence |
|---|---|
| Bare generic non-string roles degrade usable conversation without misclassifying typed metadata/system/tool records | Pass. `src/lib/transcript.ts:38` limits the new check to message records and untyped records with a role. Portability tests check retained usable text, ignored records, persisted degraded metadata, and cache reuse. |
| Relocated wrappers load the registered `kb` skill and its guides | Pass. All five command wrappers name `kb` and resolve guides from that skill's directory. `routing-smoke.json` records five successful navigation paths with no failed path attempts. The filesystem test explicitly avoids claiming agent dispatch coverage. |
| Timeout stops an ordinary waiting wrapper and its provider, preserves the caller, and keeps cached content | Pass on the observed macOS path, including the final preventive ordering change. `src/lib/summarizer.ts:345` creates an owned POSIX group; cancellation at `:395` signals the group and bounds cleanup. The lifecycle fixture checks descendant PID death, absence of the delayed completion marker, literal arguments, bounded return, and byte-for-byte preservation of the pinned cache. Windows selects the system `taskkill.exe` path and directly kills the wrapper in `finally` even when taskkill spawning fails. |
| Recovery supplies a fresh capture ID, summarizes the returned manifest, and preserves the original | Pass. `src/lib/summarizer.ts:221` emits JSON with a fresh UUID. The guide instructs repair before capture and reuse of the printed new manifest path. The lifecycle test executes that sequence with a quoted filename, verifies the new manifest ID, and compares the original manifest bytes. |

The additional POSIX interruption tests verify both SIGINT and SIGTERM, including preservation of the received exit signal and absence of continuing provider work or a new summary.

## Stage 2: adversarial code review

No confirmed actionable introduced defects or unresolved findings remain in the reviewed paths. Failed generation or cancellation returns before summary archival/replacement. Cancellation errors remain visible, reader locks and signal handlers are released in `finally`, and Windows spawn failure reaches the direct-process fallback.

The tests check observable lifecycle and persistence outcomes rather than only implementation calls. The final serial `bun run lint && bun run test` completed with exit 0: 481 passes, zero failures, and 3380 assertions across 44 files in 47.41 seconds. Root read the complete output; [verification.json](verification.json) records its counts and SHA-256, and the full `fix-final-serial-gate.log` remains in the local review artifacts. Root also reports 20/20 frozen evaluations and 28 compiled commands passing with an empty PATH on macOS arm64. The final compiled lifecycle/recovery probe returned from timeout in 1.039 seconds, stopped continuing provider work, and preserved the old cache and original manifest.

Verification observation: the earlier full-suite run had 480 passes and one 5000 ms timeout in unchanged `tests/qmd.test.ts` (`isVaultRegistered`); neither qmd source nor its tests is in the correction diff. The immediate focused rerun passed 3/3, followed by ten further focused runs passing 30/30. No code, test, or timeout changes were made. The cause remains unconfirmed; the timeout is not classified as a correction defect. The final serial full-suite gate above passed.

The earlier startup ordering was challenged because detachment preceded installation of signal handlers. Root's `fix-startup-signal-probe.json` records 20 immediate provider-to-parent SIGTERM trials with a one-million-byte prompt; every provider started, every caller exited with signal 15, and no delayed completion marker appeared. This nonreproduction did not prove race freedom or establish an introduced defect. The final preventive correction registers both temporary handlers at `src/lib/summarizer.ts:340` before spawning at `:345`. Its narrow catch at `:353` unregisters both handlers and rethrows the original spawn error. The explicit `Bun.Subprocess<Response, "pipe", "pipe">` type matches the configured streams. No new issue was found in this recheck; this remains a preventive ordering correction, not a reproduced-defect claim.

## Runtime limits

- Linux and Windows lifecycle execution remain unverified. In particular, Windows taskkill tree termination, failure fallback, and cleanup timing have only been reviewed statically here.
- The routing evidence covers one agent host and instruction navigation only; it does not demonstrate complete vault operations or every consuming harness.
- Independently detached descendants and remote work already accepted by a service are outside the stated cancellation guarantee, as documented in `skills/kb/references/sessions.md:68`.

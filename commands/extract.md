---
name: extract
description: Extract wiki-worthy knowledge from unprocessed session manifests
argument-hint: "[on|off]"
---

# Extract from Sessions

Invoke the `extract` skill.

- `/kb:extract` — extract from unprocessed sessions now.
- `/kb:extract on` — enable session-start nudge (reminds you when
  unprocessed sessions exist).
- `/kb:extract off` — disable session-start nudge.

The skill handles the toggle path (writes `autoExtractNudge` to
`.kb/state.json`) and the extraction path. It enumerates unprocessed
manifests with `kb sessions --unprocessed` (names include the `.md`
extension), runs `kb summarize --json sessions/<name>` lazily (requires the
`claude` CLI or `KB_SUMMARIZE_COMMAND`), retrieves each cached summary with
`kb read-session summaries/<name> --approve`, runs the ingest workflow on
confirmed candidates, and finishes each manifest with `kb mark-extracted`.
It surfaces degraded summaries and skipped summary-generation failures
visibly, using `entire_checkpoint` for provenance when present.

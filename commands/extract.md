---
name: extract
description: Extract approved knowledge from captured KB sessions
argument-hint: "[on|off]"
---

# Extract session knowledge

Use the [KB entry point](../skills/kb/SKILL.md) and the
[session guide](../skills/kb/references/sessions.md).

The shared pipeline is `kb sessions --unprocessed`, `kb summarize --json`,
ask-gated `kb read-session`, approved ingestion, then `kb mark-extracted` after
all candidates are accounted for. Surface failures and incomplete evidence.

`on` or `off` changes only the optional injection-hook reminder described in the
guide. This wrapper and native hooks are not required for extraction.

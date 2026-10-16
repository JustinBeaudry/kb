---
name: extract
description: >
  Use when the user asks to extract durable knowledge from captured KB sessions
  or to enable or disable the optional session-start extraction reminder.
argument-hint: "[on|off]"
---

# Extract session knowledge

Read the [KB entry point](../kb/SKILL.md) for vault discovery and boundaries, then
the [session guide](../kb/references/sessions.md) for the extraction workflow.

Natural-language requests work without slash commands or native hooks. Extract
only supported candidates, check already filed coverage, and mark a manifest
complete only after every candidate is accounted for. Failed or incomplete
summaries remain visibly pending.

For an explicit `on` or `off` request, follow the optional reminder section in
the session guide. The reminder affects installed injection hooks only.

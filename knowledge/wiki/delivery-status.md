---
title: Delivery status
type: overview
created: 2026-11-13
updated: 2026-11-24
tags:
  - delivery
  - status
source: docs/handoffs/2026-10-31-c3-done-deploy-first.md
---

# Delivery status

Status as of 2026-11-13. `main` is `909680d` (`feat: add private scoped graph memory and recoverable service deployment (#22)`). The current operational narrative is the 2026-11-01 section of `docs/handoffs/2026-10-31-c3-done-deploy-first.md`. The October 29 and October 30 handoffs below that section are historical. The October 29 plan's closing line that implementation has not started is also historical.

## Done

Shipped on `main`:

| Work | Evidence |
|---|---|
| Local Markdown vault, bounded retrieval, sessions, and optional qmd and Claude Code hooks | README.md, `src/cli.ts` |
| Rename from cairn to kb | `1381068` |
| Budget-bounded tree navigation (`kb map`, `kb get-node`) | pull request #17 |
| Open issue backlog across map, qmd, and session commands | pull request #18 |
| Docs aligned with shipped behavior | pull request #10 |
| Portable agent workflows | pull request #21, `8b0de6f` |
| Prebuilt release binaries, npm publish removed | pull request #20 |
| Foundation through bounded retrieval, filtered capture, remote CLI and MCP | commits cited in the 2026-10-29 handoff, then carried forward |
| I1 owner-adjudication slice, O1 deletion and retention, O2 packaging and Railway deploy | 2026-11-01 handoff; merged as pull request #22 |
| One live synthetic backup, purge, and restore drill | same handoff; API deployment `84be0e9d-d9e6-4c98-91d8-1311d6451b59` |

## Planned

The 2026-10-31 owner decision deferred these, and the 2026-11-01 section still lists them as deferred. The tree has no `src/adapters/` directory and no `integrations/hermes/` directory.

| Work | State |
|---|---|
| C2 Codex, Claude, and generic capture adapters | Deferred. Adapters must consume the remote client. |
| C4 selected import and export | Deferred. No historical import unless selected. |
| I2 Buzz owner decisions and recovery | Deferred. Model routes, Buzz, and Hermes remain disabled. |
| I3 budgeted dreaming | Deferred with I2 and I4. |
| I4 Hermes conversations | Deferred. No Hermes plugin directory in the tree. |
| O3 fair usefulness evaluation | Deferred. |
| Private usefulness and a new enrolled client | Qualification credentials were revoked. The handoff says to enroll a new scoped client for later authorized use. |
| Recovery targets beyond one drill | 24-hour RPO, four-hour RTO, 30-day retention, off-device custody, and provider disaster independence are not claimed. |

## Gaps

- The README's remote section and the 2026-11-01 handoff describe different layers. The client tests do not qualify production. The handoff records one live drill on the personal Railway project.

## Tracking

Linear tracks this work in the Applied Labs workspace, team `APP`, project Software Factory. Issues were filed on 2026-11-24.

| Issue | Work | State |
|---|---|---|
| APP-6 | Local vault, bounded retrieval, sessions | Done |
| APP-7 | Portable agent workflows, pull request #21 | Done |
| APP-8 | Prebuilt release binaries, pull request #20 | Done |
| APP-9 | Scoped graph memory, I1, O1, O2, pull request #22 | Done |
| APP-10 | Retrieval pilot, KB CLI against file search | Done |
| APP-5 | C2 capture adapters and C4 selected import and export | Backlog |
| APP-11 | Cross-project lessons, git gateway, attested test outcomes | Backlog |
| APP-12 | I2 Buzz owner decisions and recovery | Backlog |
| APP-13 | I3 budgeted dreaming | Backlog |
| APP-14 | I4 Hermes conversations | Backlog |
| APP-15 | O3 fair usefulness evaluation | Backlog |
| APP-16 | Live usability review of the remote CLI | Backlog |
| APP-17 | Retrieval follow-on and broader portability evaluation | Backlog |
| APP-18 | Recovery targets beyond one drill | Backlog |
| APP-19 | This project knowledge vault | In Progress |

## Reading Order

1. [[kb-product|KB product]]
2. [[local-vault|Local vault]]
3. [[remote-memory|Remote memory]]

## Backlinks

- [[kb-product|KB product]]
- [[local-vault|Local vault]]
- [[remote-memory|Remote memory]]

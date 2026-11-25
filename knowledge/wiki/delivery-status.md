---
title: Delivery status
type: overview
created: 2026-11-13
updated: 2026-11-25
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

## Merged on 2026-11-25

The repository was recreated on 2026-11-24 after the original was deleted. Pull request numbers below are in the new `JustinBeaudry/kb`; older numbers above refer to the deleted repository.

| Work | Pull request | Deployed |
|---|---|---|
| Project knowledge vault | kb#1 | n/a |
| C2 capture adapters, `kb capture poll` | kb#2 | n/a (client) |
| C4 selected import and portable export | kb#10 | No |
| I1 remainder: evidence policy, git source reader, cross-project lessons | kb#7 | No |
| I2 Buzz decisions and owner recovery | kb#9 | No |
| I3 budgeted dreaming | kb#8 | No |
| I4 Hermes memory provider | kb#6 | n/a (plugin) |
| Supersede older verified revisions, backfill migration | kb#14 | No |
| Remote CLI and API error details | kb#4 | No (API) |
| `kb get` accepts map paths | kb#13 | n/a (client) |
| Backup recovery point evidence in logs | kb#5 | Yes: graph deployment `5ed3e589`, commit `7787c3d` |
| Retrieval follow-on at 1,000 pages (APP-17) | kb#11 | n/a |
| Local usefulness trial (APP-15) | kb#12 | n/a |
| Recovery custody and backup provider decision | kb#15, ADR 0002 | n/a |

## Not yet done

| Work | State |
|---|---|
| Deploy kb#7, #8, #9, #10, #14 | Their migrations (007, 008, 010, 011) change the backup schema digest, so the new build cannot restore earlier backups. Take a backup right after deploying, and keep graph deployment `5ed3e589` for restoring older ones. |
| Off-device custody | Decided in ADR 0002 (1Password). Needs the owner to sign in to 1Password. |
| Backups outside Railway | Decided in ADR 0002 (Cloudflare R2). Needs a Cloudflare account. |
| 24-hour RPO and 30-day retention evidence | Accumulates from `backup_completed` lines after the kb#5 deploy. |
| Usefulness on real knowledge | Production holds no knowledge yet. A read-only client `app15-o3-agent` is enrolled for project `kb-railway-qualification`. |

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

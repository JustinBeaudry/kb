---
title: Remote memory
type: concept
created: 2026-11-13
updated: 2026-11-13
tags:
  - memory
  - deployment
source: docs/handoffs/2026-10-31-c3-done-deploy-first.md
---

# Remote memory

Remote memory is a private graph service. The Go API, worker, and Neo4j graph are authoritative. Evidence objects live in S3-compatible storage. The Bun CLI keeps local schema-v2 behavior and uses explicit schema-v3 commands for the remote service. There is no Postgres, document database, Redis, or vector service in the approved design.

The service is single-owner. Task and project scopes bound retrieval. Ordinary agent history and model conclusions stay candidates until an owner adjudication publishes them. A `user` role or repetition does not verify a fact.

## What is running

The 2026-11-01 handoff records a personal Applied Labs Railway project `90d569ba-e820-4e11-8619-366619307148` in `us-east4`. API, worker, and graph were observed SUCCESS. The API origin recorded there is `https://api-production-15a1f.up.railway.app`. Graph and worker have no public domain. `main` contains that work as `909680d`, pull request #22.

That handoff records one synthetic live drill: upload, completion, publication, retrieval with a continuation cursor, backup, selected-source deletion to `purge_complete`, and restore `railway-restore-001` in 24.813 seconds. Malformed credentials returned 401. A 32-request burst returned eight 401s and twenty-four 429s. Qualification clients were revoked afterward.

The README still says the remote client ships from source and that its synthetic tests do not qualify an OS credential store, a specific MCP client, a production deployment, or a released binary. The handoff's live drill is one provider qualification, not that broader claim.

## Limits recorded with the drill

The same handoff says this drill does not prove a continuous 24-hour RPO, a four-hour worst-case RTO, 30-day elapsed retention, off-device recovery custody, or provider disaster independence. Held-I/O drain and lost acknowledgements were exercised in local compiled acceptance, not fault-injected into the live provider. Model routes, Buzz, and Hermes stay disabled. Historical import was not selected.

## Related Concepts

- [[kb-product|KB product]]
- [[local-vault|Local vault]]
- [[delivery-status|Delivery status]]

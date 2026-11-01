# Current handoff — 2026-11-01: O2 live provider qualification

This section supersedes the operational state and next steps in the historical C3 handoff below. C3, the approved I1 owner-adjudication slice, O1 and the O2 synthetic provider drill are implemented and committed. C2, C4, the remaining intelligence slices, O3 and historical import remain deferred. Model routes, Buzz and Hermes remain disabled.

Production API now runs `fc708f82f748f0e232161c82f95b4d8bf79e4b92`, source tree `ac77df1e599db03d889834b5ebc204de4a0a2ae6`, archive SHA256 `d4e0b6d603cbabece3653bbdec1d11b34e70d5dd424d55e7f3763c093e621329`. Worker and graph remain on `03a6c10fc181327ca4a458c356e9c4926a4cbf35`, the source used for the full live restore drill below. The API update rejects malformed credentials before durable work and bounds plausible-credential dispatch to 8/second, burst 16, 8 concurrent per process while keeping authoritative authentication fenced. Local full Go vet/race/lint passed with 80.35% maintained coverage; compiled recovery passed in 542.186 seconds, including fresh restore in 52.311 seconds. Both hosted client jobs passed 587 tests. Consult [PR #22](https://github.com/JustinBeaudry/kb/pull/22) for current required checks, approval and merge state.

The API-only deployment `84be0e9d-d9e6-4c98-91d8-1311d6451b59` passed live HTTP checks: all three malformed-header cases returned 401; a finite 32-request burst returned eight 401s and twenty-four 429s with valid schema-3 responses and Retry-After; health returned 200; scoped status/retrieval returned 200; synthetic capture returned 201 and completion returned 200. The retained 4992-byte hash matched and the deleted publication stayed absent. Both new scoped clients were revoked, 401 was verified, and their exact remote credential sinks were removed. Actual API PID 1 UID 7474/amd64 and private host open/zero active/unknown were checked. This smoke supplements the earlier full provider restore; it does not claim a second live restore.

The personal Applied Labs project `90d569ba-e820-4e11-8619-366619307148`, production environment `8b4ecd41-5f64-4863-9d24-8b6000e3433c`, is in Virginia `us-east4-eqdc4a`. All three services were observed SUCCESS with no pending patch. API is `https://api-production-15a1f.up.railway.app`; graph and worker have no public domain or TCP proxy. One replica each: API/worker 0.5 CPU and 512 MiB; graph 2 CPU and 4 GiB with its original 32 GiB `/data` volume. Exact final deployment IDs and configuration are in [the resource manifest](../../deploy/resource-manifest.json). Exact provider image digests are recorded from final deployment metadata; all three running PID 1 processes were independently verified as UID `7474` on amd64. Historical local image digests remain separately labeled.

The live drill uploaded, completed, submitted and host-published two synthetic sources, then retrieved verified citations and a genuine continuation cursor. Backup `railway-before-delete-001` completed in 23.109 seconds. Selected-source deletion reached `purge_complete`; the independent authenticated journal reached sequence 1 and was checked before and after restoration. Retained backup copies remain `expiry_pending`.

Pause `railway-restore-001` drained to an exact recovering fence before stopping the apps and graph, preserving the volume, journal and backups. Fresh evidence bucket `evidence-restore-001-i2zbqf` passed conditional create/CAS, four cross-bucket GET/PUT denial pairs and exact planned-key cleanup. Maintenance/retrieval secrets were rotated only after versioned encrypted custody passed authenticated EOF readback; graph authentication, owner authority, journal and backup keyrings were preserved. Graph started alone with the new evidence bucket and restore mode 1, and actual runtime bindings were verified before the single protected restore.

Restore `railway-restore-001` completed in 24.813 seconds into `/data/kb/graph/restore-872e3de84028bf9e83155e7e`; persisted state and actual Java file descriptors agreed. Original `/data/kb/graph/initial` remains owned by UID `7474`, mode `0700`. All four objects in the fresh bucket were read (9984 bytes): exact retained bytes/hash, two zero-byte deletion fences and no deleted synthetic text. Old agent/collector bearers returned HTTP 401, the old real cursor returned HTTP 409 (`stale`), and fresh retrieval returned the exact retained 4992 bytes. Deleted publication remained absent.

Graph then restarted in normal restore mode 0. HostReady true, phase open, startup fence generation 6, zero active/unknown permits and journal sequence 1 were observed, followed by successful public readiness/auth/retrieval checks. All four qualification clients are now revoked, fresh bearer denial HTTP 401 was verified, and the four exact remote credential sinks were removed after protected local import. Worker has no retrieval signing key. Enroll a new scoped client through protected host administration for later authorized use; qualification credentials are not reusable access.

Operational details for the next operator:

- Railway SSH exec starts as root even though the normal service runs as UID `7474`. Follow [deployment](../runbooks/deployment.md): drop to UID `7474` before opening credential descriptors and use the runtime entrypoint for protected host commands.
- An unchanged source upload can be SKIPPED by watch paths. The drill explicitly redeployed the selected qualified build after committing role variables with `skipDeploys`, then verified actual bindings. No rollback was used.
- Restore mode intentionally leaves HostReady false before initialization/recovery. Closed-phase private status leaves `JournalSequence=0` unsampled; protected `journal-check` reads the current independent head. Follow [recovery](../runbooks/recovery.md), preserve the exact fence and never clear unknown permits by timeout or edit state files.

Earlier `03a6c10` release qualification passed full Go race/vet/lint with maintained coverage 7728/9636 (80.20%), TypeScript 587 tests/4073 assertions, and compiled lifecycle acceptance 543.710 seconds. The two hosted runs for `03a6c10` passed client tests and Go race/vet/lint/coverage, but failed initial compiled-fixture startup before capture. The diagnostics-only `38ee208` checks (PR run `41280814402`, push run `41280811137`) identified the same root cause: object readiness failed with Seaweed exit 255 and no OOM, while the other four containers ran. The pinned image drops to UID 1000, which cannot read a host-UID-1001 config at mode 0600. Controlled Linux-volume reproduction matched the failure; changing only ownership restored startup as UID 1000 and S3 HTTP 403. The fixture correction copies the protected read-only source into private tmpfs (directory 0700, file 0600) owned by Seaweed before invoking the original privilege-dropping entrypoint. Host permissions, service privilege level, readiness timeouts and acceptance assertions are preserved. Current branch gate results are attached to PR #22.

This one synthetic drill does not prove continuous 24-hour RPO, four-hour worst-case RTO, 30-day elapsed retention, off-device recovery custody or provider disaster independence. Encrypted recovery bundle and Keychain remain on the same device. Held-I/O drain and lost acknowledgements were exercised by local compiled acceptance, not fault-injected into the live provider. Before landing, require the current PR checks/review and merge without bypass. Private usefulness and historical capture are separate future work.

---

The following is the unchanged historical snapshot, not current deployment guidance.

# Handoff 2026-10-31: C3 finished (uncommitted), deploy-first path approved

Worktree `/Users/justinbeaudry/Projects/kb-memory-design`, branch `docs/persistent-memory-design`, HEAD `bd09d1b`. All C3 work is **uncommitted**; preserve tracked and untracked files. Supersedes `2026-10-30-c3-resumption.md`.

## Owner decisions (2026-10-31)
- Scope, deploy-first: C3 -> I1 owner-adjudication slice -> O1 deletion/retention -> O2 packaging + Railway deploy. Defer C2, C4, I2-I4, O3.
- Railway: create a new `kb-memory` project in the personal Applied Labs workspace (API, worker, Neo4j + volume, bucket). Confirm the region and show the config before the first deploy.
- Recovery: host break-glass. Railway project access is owner authority. Add `kb-admin enroll|revoke|list` run on the service host; Buzz recovery can come later. This replaces the "no live bootstrap before I2" constraint.
- I1 slice: the owner (kb-admin on host) verifies or rejects candidate revisions, labeled `owner_adjudicated`. Verification atomically publishes project-scoped knowledge, and invalidation excludes dependents. Defer the git gateway, attested test outcomes and cross-project lessons.

## C3 state: complete, reviewed, not committed
- Interrupted corrections finished; MCP write contract added (server-advertised `max_revision_bytes`, `unknown_outcome`). An independent adversarial review found 2 Important and 6 Minor issues: all fixed except two deferred advisories, recorded in the `.superpowers` ledger.
- Last full gate on the final tree (before the disk-full event): TS 570/0, `go vet` and `go test -race ./...` all ok (real Neo4j/SeaweedFS), golangci-lint 0 issues.
- **Pending:** whole-module coverage (`go test -race -coverpkg=./... -coverprofile=... ./...`, checker `.superpowers/sdd/.../task-6-fix2-coverage-check.py`, 80% floor). The attempt failed with a full disk. A per-package profile without coverpkg read 76.0%, which is not comparable to the floor.
- Then commit C3 (suggested: server, CLI, docs commits).

## Environment incident
- The disk filled because the Go build cache reached 96 GB. Cleared with `go clean -cache`; about 97 GB is free now.
- Docker Desktop daemon returns HTTP 500 on every API call, even after a quit/relaunch attempt. Fix Docker (fully restart Docker Desktop, or reboot) before any integration suite. Unrelated containers (gam132-*, softwarefactory-*) were not touched.

## Codex resumption verification — 2026-10-31

Docker Desktop restart restored daemon access. Fresh TS lint and tests passed (570 tests, 3978 assertions), Go vet and golangci-lint passed (0 issues), and full `go test -race -coverpkg=./...` passed including real graph/object fixtures. Maintained coverage is 3024/3485 = 86.77%, using the prior deduplicated-block checker with generated `.gen.go` files excluded. Detailed logs/profile are in `.superpowers/sdd/2026-10-29-persistent-memory/task-8-resume-*`. This supersedes the pending coverage and Docker incident above. C3 is ready for commit; no deployment or live acceptance is claimed. Next: the approved I1 owner-adjudication slice, then O1 and O2.

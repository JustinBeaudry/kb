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

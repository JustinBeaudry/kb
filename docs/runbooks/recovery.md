# Memory backup and recovery

Run host operations inside the private graph service as UID7474. The graph
supervisor owns Neo4j as its child, its sole volume, maintenance admission,
offline tools and the backup schedule. Operator commands use the owner-only
`/data/kb/control/backup.sock`; API and worker control credentials cannot invoke
backup, restore, pause, recovery, or owner administration. Public liveness can
remain healthy while readiness is false.

```sh
kb-supervisor status
kb-supervisor backup --id backup-20261031-001
```

Use a unique bounded opaque ID containing only letters, digits, dash or
underscore. Keep the ID when diagnosing a lost response. Inspect `status`
before retrying. A completed retry returns the recorded result. A failed
attempt never replaces an earlier authenticated completed set. Backup failure
keeps admission closed; a timeout does not clear permits.

The persisted schedule runs 20 hours from the successful snapshot boundary,
with persisted retry backoff beginning at five minutes and capped at 320
minutes. Failed/unknown maintenance must be reconciled before the next attempt;
the scheduler cannot reopen it. `BackupAgeSeconds` is the actual private
operational measure. The <=24 hour RPO and <=4 hour RTO are qualification
targets and can be missed during an outage. Daily provider volume snapshots do
not replace these encrypted 30 day archives.

## Backup sequence and files

The supervisor persists the operation and fence on `/data/kb/maintenance.json`
and `/data/kb/backup.json`, closes admission, and waits for every registered
source operation to finish. It authenticates and reconciles the current
independent deletion journal, captures the exact paged artifact inventory
(including source/support/excerpt generations and tombstones), then stops and
waits for the Neo4j process group. Both `neo4j` and `system` are dumped using
the pinned local Community tools.

The age v1.3.2 encrypted tar contains both dumps, the exact manifest and every
available nondeleted evidence generation. Per-role byte counts and SHA256
digests are checked. A separate HMAC-authorized completion envelope binds the
namespace, backup ID, age key IDs and entire ciphertext digest/length; the
manifest signing authority is separate from the journal authority. Recipient
encryption alone is never treated as proof of an authorized writer. The
supervisor reads back the uploaded archive, verifies exact inventory and reads
the decrypted stream through authenticated EOF before publishing completion.
No key material is included in archives or command output.

The graph restarts closed. Explicit schema initialization, current journal
replay, required object probes and retained backup key checks must succeed
before the matching fence resumes. Completed sets older than 30 days are
physically deleted only from the dedicated backup bucket. Evidence deletion
markers and independent journal objects never use this retention path.

Private scratch is on the encrypted graph volume with mode0700, files0600.
Cipher/download temporary files and verified restore staging are removed after
use. Failed local dump directories are retained for host diagnosis; remove
only the exact owned failed scratch directory after recovery and inspection,
never evidence keys, journal keys, or active graph paths. Successful backup
dump scratch is removed after online recovery.

## Key custody and rotation

`KB_BACKUP_KEYRING_FILE` is a protected current-UID regular file (no symlink,
maximum4096 bytes). It contains `authority_key_id`, `authority_keys` (base64
32 byte keys), `recipients` (X25519 public recipients by key ID), and
`identities` (separately recoverable retained private identities by key ID).
Keep this file, protected retrieval HMAC key, graph authentication secret and
independent journal keyring recoverable outside all graph/evidence backups.
Never paste values into argv, logs, stdout, a ticket or a runbook.

Every current recipient must have its matching retained identity. Keep old
identities and manifest authority keys until all corresponding completed sets
expire. Readiness refuses authenticated retained sets with missing identities
or authority keys. Key rotation tests prove old+new identities read both
generations, new-only refuses old archives, and tampered/truncated archives
never publish verified plaintext.

## Restore into fresh locations

1. Choose a unique restore ID. While the original configuration is still
   running, execute `kb-supervisor pause --id restore-20261031-001`. Wait for its
   successful reply, then inspect status: phase recovering, active0, unknown0,
   exact operation ID. This drain occurs before stopping API/worker and avoids
   orphaning claims or I/O permits during reconfiguration.
2. Stop API and worker, then stop graph normally. Preserve the graph volume and
   independently durable journal and backup buckets. Do not use volume removal
   or reset the journal. Prepare a **new empty evidence bucket**, new explicit
   scoped credentials, and a **new retrieval continuation HMAC key**. Configure
   API and graph with that same new protected retrieval key. The supervisor
   derives evidence namespace identity from the actual endpoint and bucket;
   an alias cannot make the original bucket look fresh.
3. Restart graph with `KB_SUPERVISOR_RESTORE_MODE=1`, the same graph volume,
   fresh evidence configuration, rotated retrieval key, and the unchanged live
   independent journal namespace/keyring. Restore mode prevents startup from
   reopening the old graph against empty evidence. The paused fence remains
   closed across restart. Do not initialize a missing historical journal.
4. Execute `kb-supervisor restore --backup-id BACKUP_ID --id RESTORE_ID` using
   the exact paused ID. Authentication, decryption, full EOF, source inventory,
   schema/tool/architecture compatibility and cursor-key rotation are checked.
   Dumps load into a fresh graph generation under `/data/kb/graph/`; the old
   graph generation remains intact. The durable process pointer changes only
   after both database loads complete. A new random incarnation binds staging
   and load paths even if an operator ID is reused after another backup.
5. The loaded snapshot's graph inventory and journal barrier must exactly match
   its authenticated manifest. Before opening, revoke **all** restored clients,
   remove their token digests and advance auth/knowledge/deletion epochs in one
   locked transaction. A cryptographically random restore-incarnation marker, persisted outside the restored graph before load, makes exact retries idempotent and preserves
   subsequently enrolled credentials. Replay the **live independent** journal
   to its authenticated current head. Only still-eligible evidence is copied
   from private staging to the fresh bucket. Deleted generations receive
   permanent zero-byte fences and never become eligible. Verify complete
   byte counts/digests and journal continuity again before resume.
6. Enroll fresh scoped clients through protected host `kb-admin` credential
   descriptors. Snapshot client IDs are retained revoked, so use new client
   IDs. Confirm old bearers and pre-restore continuation cursors are refused,
   deleted sources cannot be retrieved, and retained citations hash correctly.
   Start API/worker with the fresh configuration. Unset restore mode for later
   normal restarts; do not revert the new evidence bucket or retrieval key.

Never restore the journal head or journal keyring from the archive. Missing,
stale, rolled-back or unauthenticated journal state blocks readiness. Provider
operators with authority over the independent storage can destroy or roll it
back; encrypted hash chains do not remove that trust boundary. Follow the
provider qualification and key custody limits in the deployment runbook.

## Failure diagnosis and capacity

`status` reports safe private operation/phase, active and unknown counts,
schedule, actual backup age, journal capacity and disk availability. Public
health exposes boolean readiness and closed reason codes only. Journal
sequence>=9000 warns of the fixed10000-operation lifetime cap; capacity refuses
new lifecycle operations. No automatic namespace reset is supported.

`KB_BACKUP_MAX_BYTES` defaults to4GiB. Before dumping/decrypting/loading,
unprivileged filesystem capacity must be known and sufficient for conservative
scratch and fresh-store reservations. Archive creation reserves three times
the configured maximum plus256MiB; restore also checks authenticated declared
graph size and compressed archive sizes. Usage>=75% is a private warning.
Compressed dumps can expand substantially; initial32GiB volume qualification
applies to the measured synthetic dataset, not an arbitrary future corpus.
Increase capacity and remeasure the actual maintenance window before growth
invalidates the margin. Failure never silently omits evidence.

For an ordinary failed **backup** with no active/unknown source permits,
`kb-supervisor recover --id BACKUP_ID` starts the graph closed, replays the live
journal and verifies dependencies before resuming. It abandons that failed
attempt; use a new backup ID afterward. An interrupted restore remains closed
and is retried with its exact restore ID and configuration after inspection.

An unknown permit is a separate boundary: cancellation, process heartbeat
expiry, an HTTP disconnect, or a new instance ID does not prove source I/O
ended. Keep admission closed. Identify the exact original process/deployment,
prove its termination, and reconcile its exact journal/object operation with
provider readback and the idempotent O1 operation identity. Preserve receipts.
This release deliberately has no generic command that clears unknown permits
from an operator assertion. Engineering review and a bounded reconciler are
required to consume that evidence; do not edit persisted state or use a timeout
as a workaround. Unattended recovery from unknown external effects is not
claimed.

## Local evidence

Run `go test -race ./internal/service ./internal/gateway ./internal/platform
./cmd/kb-supervisor` for component behavior, and explicitly run
`go test -race -tags integration ./internal/gateway -run
TestNeo4jProcessRealOfflineRoundTrip -v` for the pinned actual Linux process
fixture. The latter fails when Docker/tooling is unavailable and never skips.
The compiled compose acceptance additionally exercises upload, owner
publication, retrieval, backup, deletion, fresh restore, old bearer denial and
deleted retrieval denial. Check the task15 report for executed results and
timings; image health or component tests alone do not establish that outcome.

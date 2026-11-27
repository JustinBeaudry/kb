# Memory credential custody

No credential value belongs in source, Docker build arguments, TOML, logs, screenshots, command-line arguments or backup archives. Scoped client credentials are distinct from graph/S3 credentials, maintenance infrastructure authority and owner host administration. There is no remote owner bearer.

## Protected runtime files

Files must be absolute, regular, owned by the runtime UID7474, bounded in size and unreadable by group/others. `/run/kb` is owner-only. Configuration rejects a value and its `_FILE` alternative supplied together. Journal keyring and maintenance control authority require protected files.

| Role | Runtime configuration | Custody |
|---|---|---|
| Retrieval cursor authentication | `KB_RETRIEVAL_HMAC_KEY_FILE` | API and graph supervisor; bind digest and require rotation during restore |
| Graph authentication | `KB_GRAPH_SECRET_FILE` | API/worker/graph only; not clients |
| Evidence storage | `KB_S3_ACCESS_KEY_FILE`, `KB_S3_SECRET_KEY_FILE` | Evidence bucket only; no lifecycle expiry or physical deletion of permanent fences |
| Independent journal storage | `KB_JOURNAL_ACCESS_KEY_FILE`, `KB_JOURNAL_SECRET_KEY_FILE` | Separate bucket/namespace and credentials, outside graph/evidence restore |
| Journal authentication | `KB_JOURNAL_KEYRING_FILE`, active `KB_JOURNAL_KEY_ID` | Separately recoverable old and current 32-byte authentication keys |
| Maintenance authority | `KB_MAINTENANCE_SECRET_FILE` | Infrastructure admit/finish/state only; cannot administer owners |
| Backup storage | `KB_BACKUP_ACCESS_KEY_FILE`, `KB_BACKUP_SECRET_KEY_FILE` | Separate backup namespace and credentials |
| Archive recipients, decryption and manifest authority | `KB_BACKUP_KEYRING_FILE` | Retained age identities plus independent authenticated-writer authority; never archived |

The packaging entrypoint can materialize a fixed list of sealed Railway `*_CONTENTS` variables into mode0600 files, export the corresponding `_FILE` path and unset those contents in the child environment. It never sources, evaluates or prints input. The names are `KB_GRAPH_SECRET_CONTENTS`, `KB_RETRIEVAL_HMAC_KEY_CONTENTS`, `KB_MAINTENANCE_SECRET_CONTENTS`, `KB_JOURNAL_KEYRING_CONTENTS`, evidence/journal/backup access and secret key names with `_CONTENTS`, and `KB_BACKUP_KEYRING_CONTENTS`. Supply each role only to services that need it. Sealed variables remain accessible to the provider/control-plane operator; file materialization does not protect against that trusted operator.

Journal keyring is a strict map of opaque key IDs to base64 32-byte keys. Backup keyring contains `authority_key_id`, `authority_keys` (base64 32-byte keys), `recipients` (key ID to age public recipient) and `identities` (key ID to age private identity). Generate actual values through a protected local process or secret manager; this runbook deliberately contains none. Keep a separately encrypted recovery copy outside the graph/evidence/backup restore lifecycle and test access after host loss.

Neo4j initial authentication uses a small helper compiled against the exact pinned Neo4j libraries. It reads the password from stdin into memory and invokes the vendor command in-process. The password never enters OS argv; helper output is suppressed. Never replace this with `neo4j-admin ... PASSWORD`, authentication disabled or secret-bearing shell tracing.

## Enrollment and rotation

The host Unix path is owner-only and validates the peer UID and fixed child execution handshake. `kb-admin setup` or `enroll` writes a scoped bearer to an already-open owner-only file/pipe descriptor. Import through the client credential adapter; never copy it through terminal stdout. If a host-exec channel cannot preserve a pipe descriptor, use an owner-only host file, protected transfer/import and cleanup. Qualify that channel before claiming live enrollment.

Client bearers can expire and be rotated:

- `kb-admin setup`, `enroll` and `rotate` take `--ttl DURATION`, a Go duration between `1h` and `8784h` (366 days). The bearer stops authenticating at that instant; the next request gets `401`. Without `--ttl` the bearer does not expire, which is the previous behavior. Prefer `--ttl 2160h` (90 days) for agents.
- Over HTTP, `POST /v1/clients` takes the same bound as `ttl_seconds` (3600 to 31622400). The response's `client.expires_at` says when the bearer stops authenticating, and is absent for a bearer without expiry. A value outside the bound is rejected with `400` before anything is enrolled.
- `kb-admin rotate --client-id ID --credential-fd 3 [--ttl DURATION]` issues a fresh bearer for an active client and writes it to the descriptor, like `enroll`. Grants are unchanged. The old bearer stops working in the same transaction, and the auth epoch advances, so export cursors and other state bound to it are stale. A revoked client cannot be rotated; enroll a new one.
- `kb-admin list` shows `ExpiresAt` for each client. Rotate before that time, then import the new bearer on the client with `kb remote credential-set`.

## Owner key loss or theft

The owner's Nostr key is the only signer of verified knowledge. kb holds only its public half. If the private key is lost, decisions can no longer be approved by signature. If it is stolen, whoever holds it can approve decisions as the owner. Replace it on the host:

1. Generate a new key on a device you control, in your Nostr signer or Buzz. Never move a private key to the service host.
2. On the service host, run `kb-admin owner-rotate --owner-public-key NEW_64_HEX`. It prints the owner ID, the retired key, the new key and the time, and nothing secret.
3. From that moment, every signature by the old key fails: pending decision challenges and Buzz replies signed with it are refused, and the old key is kept as retired so it can never be enrolled again. Client bearers and grants are unchanged.
4. If the key was stolen, review what was approved while it was out of your hands. Owner judgments record the signing key and time; run `kb-admin decisions` for open decisions, and use `kb-admin invalidate` on any verified claim you did not mean to approve.
5. Re-run `kb-admin decision-challenge` for anything that was waiting on your signature and sign it with the new key.

Drill it once on a test instance: rotate to a new key, confirm a challenge signed with the old key is refused and one signed with the new key is accepted, then confirm that rotating back to the old key fails.

Host access is a separate authority. Anyone who can run `kb-admin` on the host can already enroll clients and adjudicate, so `owner-rotate` adds no power the host did not have. Protect host access as you would the key itself.

## Access audit trail

The api counts every authorization decision an authenticated client causes: one row per client, project, capability and UTC day, with allowed and denied counts and the first and last time seen. It records counts and times only, never request content, queries, bearers or source addresses. Rows are buffered in memory and written to the graph every 30 seconds and on clean shutdown, and rows older than 400 days are pruned.

- `kb-admin access [--client-id ID] [--project PROJECT] [--since YYYY-MM-DD] [--limit 100] [--after CURSOR]` prints the trail as JSON, newest day first. The default window is the last 30 days. Pass the returned `next` as `--after` for the next page.
- A project of `*` means the client asked for a project it was never granted. Denials there are the first thing to look at after a suspected credential leak; then revoke or rotate the client.
- A project of `""` means a request that names no project, such as `status:read`.
- Requests that fail authentication have no client to attribute, so they are not in the trail; they appear only as `401` counts in the request logs.
- If the api is shut down abruptly, up to 30 seconds of counts can be lost. If more than 10,000 distinct rows build up between flushes, new rows are dropped and the api logs `access_audit_dropped`.

Rotate keys by adding new named keys while explicitly retaining old journal authentication keys, backup authority keys and age identities needed by retained evidence/backups. Verify both old and new archive reads, then verify that an old archive fails closed without its retained identity. Removing keys is deliberate data loss after retention, not routine rotation. Do not change journal namespace or reset sequence to recover capacity. A 9000 warning requires extension planning; sequence10000 closes admission.

## Provider guarantees to measure

Use isolated keys to test conditional-create races for evidence and independent conditional head updates/readback for journal. Verify anonymous denial and cross-bucket scoped credential denial. Railway bucket HTTPS is publicly reachable authenticated storage; it has no private-network endpoint, object locks/versioning or automatic bucket backups. Provider/operator rollback or deletion of independent storage remains a trusted-party risk. An encrypted hash chain cannot detect a malicious provider returning an old internally consistent head without an external freshness authority.

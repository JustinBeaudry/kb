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

Rotate keys by adding new named keys while explicitly retaining old journal authentication keys, backup authority keys and age identities needed by retained evidence/backups. Verify both old and new archive reads, then verify that an old archive fails closed without its retained identity. Removing keys is deliberate data loss after retention, not routine rotation. Do not change journal namespace or reset sequence to recover capacity. A 9000 warning requires extension planning; sequence10000 closes admission.

## Provider guarantees to measure

Use isolated keys to test conditional-create races for evidence and independent conditional head updates/readback for journal. Verify anonymous denial and cross-bucket scoped credential denial. Railway bucket HTTPS is publicly reachable authenticated storage; it has no private-network endpoint, object locks/versioning or automatic bucket backups. Provider/operator rollback or deletion of independent storage remains a trusted-party risk. An encrypted hash chain cannot detect a malicious provider returning an old internally consistent head without an external freshness authority.

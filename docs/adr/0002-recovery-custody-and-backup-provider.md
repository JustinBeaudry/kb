# ADR 0002: off-device recovery custody and an independent backup provider

Date: 2026-11-25

Status: Decided. Execution needs the owner's 1Password sign-in and a Cloudflare account. Linear APP-18.

## Context

One synthetic live drill passed on Railway. Two recovery gaps remain:

- **Custody is on one device.** The encrypted recovery bundle lives at `~/.local/share/kb-custody/<project>/recovery.age`. The age identity that decrypts it lives in the owner's login Keychain (service `com.justinbeaudry.kb.<project>.recovery`, account `recovery-age-v1`). Losing or wiping this Mac loses both.
- **Backups share a provider with production.** Graph, evidence, journal and backups all sit in the same Railway project. A provider-level outage or account loss takes the backups with it.

The backup gateway already reads its own `KB_BACKUP_ENDPOINT`, `KB_BACKUP_BUCKET`, `KB_BACKUP_REGION`, `KB_BACKUP_ADDRESSING_STYLE`, `KB_BACKUP_NAMESPACE` and credential files, separate from evidence and journal storage. Moving backups is configuration plus qualification, not code.

## Decision

### 1. Off-device custody in 1Password

Store two separate items in a dedicated 1Password vault named `kb-recovery`:

| Item | Content | Type |
|---|---|---|
| `kb-memory <project> recovery bundle` | `recovery.age`, attached as a file | Document |
| `kb-memory <project> recovery identity` | the `recovery-age-v1` age identity | Secure note, concealed field |

Why 1Password: it is already installed (`op` CLI), end-to-end encrypted, synced off this device, and recoverable with the owner's Secret Key and account password from any device. It adds no new infrastructure.

Keep the Keychain copy as the working copy. 1Password is the off-device copy. The two items stay separate so a single exported item reveals nothing usable on its own.

Rejected:
- **Paper only.** It is durable but slow to restore and easy to mislay. It is acceptable as a later third copy.
- **The same R2 bucket as backups.** That would put the decryption material next to the ciphertext it protects.

### 2. Backups on Cloudflare R2

Point the backup namespace at a private Cloudflare R2 bucket in a separate Cloudflare account. Evidence, journal and the graph stay on Railway.

Why R2:
- It is S3-compatible, with conditional writes, so the existing backup gateway works unchanged.
- There are no egress fees, so a full restore download costs nothing.
- Storage at this scale (archives are megabytes, 30-day retention) is a few cents a month. The free tier likely covers it.
- It is a different company from Railway, which is the independence the target asks for.

Rejected: Backblaze B2 (fine, but egress is billed beyond the free allowance), and AWS S3 (egress cost and more account overhead for a single-owner service).

## Procedure

### Custody (owner signs in once)

```sh
op signin
op vault create kb-recovery
op document create ~/.local/share/kb-custody/90d569ba-e820-4e11-8619-366619307148/recovery.age \
  --vault kb-recovery --title "kb-memory 90d569ba recovery bundle"
security find-generic-password -s com.justinbeaudry.kb.90d569ba-e820-4e11-8619-366619307148.recovery -a recovery-age-v1 -w \
  | op item create --vault kb-recovery --category "Secure Note" \
      --title "kb-memory 90d569ba recovery identity" 'identity[concealed]=-'
```

The identity goes through a pipe, never through argv or the terminal. If this `op` version does not read a field from stdin with `-`, create the item with an empty field and paste the value in the 1Password app instead. Then prove recovery on a scratch path: fetch both items from 1Password, decrypt the bundle with `age -d -i`, and compare the checksum with the local bundle.

### Backup provider

1. Create a Cloudflare account and an R2 bucket (for example `kb-memory-backups`), with public access off.
2. Create an R2 API token scoped to that bucket only, with object read and write.
3. Run the existing S3 qualification against it before switching. The conditional-create and fence tests in `server/internal/gateway` (`s3_fence_test.go`, `backup_s3.gateway_test.go`) use the qualification environment described in `docs/runbooks/deployment.md`.
4. On the Railway graph service, set `KB_BACKUP_ENDPOINT=https://<account-id>.r2.cloudflarestorage.com`, `KB_BACKUP_REGION=auto`, `KB_BACKUP_ADDRESSING_STYLE=path`, `KB_BACKUP_BUCKET`, and the key files through the sealed `*_CONTENTS` variables in `docs/runbooks/credentials.md`. Keep the evidence and journal settings unchanged.
5. Run one explicit backup and one fresh-target restore drill from R2 (`docs/runbooks/recovery.md`). Record the times.

## What this does and doesn't prove

Once both are done, the recovery material survives the loss of this Mac, and backups survive the loss of the Railway account. The 24-hour RPO is evidenced by `backup_completed` lines over time (kb#5). The 30-day retention needs 30 elapsed days after the switch. A four-hour worst-case RTO still needs a timed drill from R2 into a fresh target.

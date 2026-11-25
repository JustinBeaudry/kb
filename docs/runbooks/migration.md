# Selected import and portable export

`kb transfer` moves selected local vault material into the remote memory service
as candidates, and exports authorized remote memory as a portable,
vault-compatible bundle. It never rewrites vault originals and never uploads
anything that was not explicitly selected and approved.

## Credentials

Import needs two separately enrolled clients on the same project, registered
with `kb remote credential-set` for the same origin:

| Profile flag | Role | Capabilities |
| --- | --- | --- |
| `--profile` | agent | `policy:read`, `candidate:write`, `evidence:read` (plus `memory:export` for export) |
| `--evidence-profile` | collector | `artifacts:write` |

The collector credential stages evidence bytes; the agent credential submits
candidates, reads policy and exports. The gateway never uses one credential for
the other's operations. `memory:export` is granted only by explicit enrollment.

## Import

1. **Inventory (metadata only).**
   `kb transfer inventory --vault <path>` lists file IDs, kind (`curated`,
   `raw`, `session`) and byte size. It reads no file content. Dot-directories
   (`.kb/`, `.obsidian/`, `.git/`) are never listed. Symlinks and special files
   are listed with `"unsafe": true` and can never be imported.
2. **Preview (dry run).**
   `kb transfer preview --vault <path> --project <p> --task <t> --select a.md,b.md --manifest <new-file> --profile <agent> --endpoint <origin>`
   reads only the selected eligible files and writes a manifest (mode 0600,
   refuses to overwrite). Raw (`raw/`) and session (`sessions/`) files are
   omitted as `not_selected_kind`, **without being read**, unless
   `--include-raw` / `--include-sessions` is passed. Files excluded by the
   project's capture policy are omitted before any read. The manifest holds
   source SHA-256 hashes, locators, the scope binding, the policy revision and a
   policy digest; it never holds source content. Preview prints the approval
   digest.
3. **Review and approve.** Check `items` and `omissions`. Approval is the exact
   manifest digest.
4. **Apply.**
   `kb transfer apply --manifest <file> --approve <digest> --profile <agent> --evidence-profile <collector> --endpoint <origin>`.
   Before uploading anything, apply recomputes the manifest digest, re-reads and
   re-hashes every selected source, and re-reads the policy. Any change returns
   `approval_stale` and uploads nothing: run preview again. Exit code 2 means
   the import was partial; the JSON report lists `imported`, `duplicates`,
   `refused`, `failed` and `omissions`.

### Guarantees

- **Always candidates.** Import calls the ordinary artifact (F4) and candidate
  (F5) APIs. Status and trust are never submitted; the server derives origin
  (`submitted`). A server response with any other status is reported as
  `unexpected_status`, never accepted. Imported candidates are task-scoped and
  do not appear as verified shared knowledge until normal verification.
- **Idempotent retry.** Claim IDs derive from project, task, session, file ID and
  content hash; artifact IDs are deterministic server-side. Re-running an
  applied manifest reports `duplicates` and creates nothing new. Duplicate
  content inside one selection is imported once (`duplicate_content`). Changed
  content under the same file ID is a new candidate; it does not overwrite the
  earlier one.
- **Path safety.** File IDs are normalized relative paths. Any symlink in any
  path component, `..`, absolute paths, and files changed between `lstat` and
  `open` are denied (`path_unsafe`). Reads use `O_NOFOLLOW`.
- **No resurrection.** Deleted source content (source tombstone by digest) and
  deleted claim identities are refused by the server (`refused` with
  `deleted`/`forbidden`), including when the same bytes reappear under a new
  file name. A refused item is never retried under a fresh identity.
- **Limits.** One file is at most 8 MiB of evidence and 16 KiB of candidate
  text; larger files are omitted as `too_large` or `too_large_for_candidate`.
  Non-UTF-8 files are omitted as `not_text`.

## Export

`kb transfer export --project <p> [--task <t>] --out <new-dir> --profile <agent> --endpoint <origin>`

- Each chunk (`POST /v1/exports`, at most 50 claims) re-reads the caller's
  current grants and epochs and re-checks every record through the
  tombstone-aware eligibility boundary. Revocation, a narrowed grant, or deletion
  between chunks is observed by the next chunk. Records the caller may not see,
  that are deleted, or whose evidence is unavailable are counted in `omitted`
  and never identified; any omission makes the export `partial`.
- Continuations are authenticated and bound to the client and scope. Progress is
  saved in `<out>/.kb-export.json` after each chunk; after a failure, rerun with
  `--resume` to continue from the last completed chunk. A changed grant or
  scope makes the cursor `stale`: restart into a new directory.
- Each claim is written as `claims/<hash>.md` with front matter recording
  `claim_id`, `revision`, `kind`, `claim_status`, `origin_trust` (the weakest
  evidence origin) and evidence references (artifact ID, digest, locator,
  origin trust). Evidence bytes are not copied; they stay behind the
  authenticated evidence fetch.

### Re-importing an export

An export directory is a valid `--vault` for preview. Exported status and trust
are preserved only as provenance metadata in the evidence locator
(`exported_status`, `exported_origin_trust`, `exported_evidence`). The imported
record is always a new `candidate` with `submitted` origin: an exported
`verified` claim must be re-verified in its new home.

## Provenance loss and filtering

- Local curated Markdown has no evidence chain. Its import carries the vault file
  ID and content hash only; it cannot claim the original observation, author,
  or time.
- Exported evidence references point to artifacts in the source service. In a
  different service they are historical provenance, not fetchable evidence.
- When a provenance list would exceed the 4 KiB locator bound, trailing evidence
  references are dropped and `exported_evidence_total` records the original
  count.
- Import does not redact. The capture policy's path exclusions apply, but field
  redaction is not performed on vault files; select only files that are safe to
  store remotely.

## Not covered

Actual migration of the owner's vaults waits for onboarding selection. Tests use
synthetic vaults only.

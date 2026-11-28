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

## Read memory in Obsidian

`kb transfer vault-sync --project <p> [--task <t>] [--session <s>] --out <folder> --profile <agent> --endpoint <origin>`

Writes a read-only view of the memory this client may see into a folder inside
an Obsidian vault, for example `<vault>/kb-memory`. Rerun it whenever you want
the view refreshed.

- `index.md` lists every claim, grouped as Verified, Candidates (unverified),
  and Disputed, superseded or rejected, with wikilinks to one page per claim in
  `claims/`. Pages carry the same provenance front matter as an export bundle,
  plus `kb/<status>` and `kb/kind/<kind>` tags for Obsidian search and graph
  filters. Unverified claims open with a warning callout.
- Claim text is written by agents, so it is rendered inert: code fences, inline
  code, HTML and image embeds are escaped. A synced page cannot run Dataview JS,
  Templater or other plugin code, and cannot load remote content when opened.
- The sync owns only `index.md` and `claims/*.md` files it wrote, recorded in
  `<folder>/.kb-vault-sync.json`. It refuses a non-empty folder it did not
  create, a folder synced for another scope, and symlinks. Files you add are
  left alone. Pages are rewritten only when their content changed.
- Pages for claims that are gone are removed only when the export was
  `complete`. A `partial` export (withheld or unavailable records) keeps
  existing pages, marks the index Incomplete, and exits with status 2.
- Edits to synced pages are replaced on the next run. To correct memory, submit
  a candidate; an edited page is still a valid `--vault` for preview.

### Keep the view current

Add `--every <minutes>` (5 to 1440) and the command stays running, syncing now
and then at that interval. Each run prints one JSON line with its time, so the
output works as a log:

```sh
kb transfer vault-sync --project <p> --out "<vault>/kb-memory" \
  --profile <agent> --endpoint <origin> --every 15
```

- The bearer is read from the credential store on every run, so
  `kb remote credential-set` after a rotation takes effect at the next run.
- A run that fails for a reason a later run can get past (the service is
  unreachable, or another sync of the folder is running) is logged to stderr
  and retried at the next interval. A failure that needs you (a revoked or
  missing credential, a lost grant, a folder the sync does not own) stops the
  command with exit status 1.
- Only one sync writes a folder at a time. A sync holds
  `<folder>/.kb-vault-sync.lock` while it runs; a second one reports
  `sync_running` and leaves the folder alone. A lock left by a crashed run is
  reclaimed after 30 minutes.
- `Ctrl-C` or `SIGTERM` stops the command between runs.

To run it without a terminal open, let the system scheduler start a single run
(without `--every`) instead. On macOS, a launchd agent in
`~/Library/LaunchAgents/dev.kb.vault-sync.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>dev.kb.vault-sync</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/kb</string><string>transfer</string><string>vault-sync</string>
    <string>--project</string><string>PROJECT</string>
    <string>--out</string><string>/Users/YOU/Vault/kb-memory</string>
    <string>--profile</string><string>PROFILE</string>
    <string>--endpoint</string><string>https://memory.example.com</string>
  </array>
  <key>StartInterval</key><integer>900</integer>
  <key>StandardErrorPath</key><string>/Users/YOU/Library/Logs/kb-vault-sync.log</string>
</dict>
</plist>
```

Load it with `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/dev.kb.vault-sync.plist`.
On Linux, a systemd user service and timer:

```ini
# ~/.config/systemd/user/kb-vault-sync.service
[Service]
Type=oneshot
ExecStart=%h/.local/bin/kb transfer vault-sync --project PROJECT --out %h/Vault/kb-memory --profile PROFILE --endpoint https://memory.example.com

# ~/.config/systemd/user/kb-vault-sync.timer
[Timer]
OnBootSec=2min
OnUnitActiveSec=15min

[Install]
WantedBy=timers.target
```

Enable it with `systemctl --user enable --now kb-vault-sync.timer`. Both read
the bearer from the same credential store as an interactive run (the macOS
keychain, or the Secret Service keyring on Linux), so they need your login
session's keychain unlocked; a run that reports `credential_unavailable` while
an interactive one works could not reach it. The lock keeps a scheduled run
from overlapping one you start by hand. Exit status 2
from a single run means the export was partial: pages were kept, not removed.

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

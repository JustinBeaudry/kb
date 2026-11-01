# Host privacy and retention operations

O1 provides local host administration, graph exclusion, a separately configured
encrypted deletion journal and fenced asynchronous active-object purge. It does
not qualify production storage durability or backup removal. O2 must provision
and test the production journal namespace, credentials, key custody, backup
schedule and recovery before deployment acceptance.

Use `kb-admin` on the protected service host. No owner bearer or remote deletion
endpoint exists. The persisted owner identity is revalidated in each host graph
transaction. `setup` establishes that identity; journal initialization and an
initial project policy are separate explicit host operations. `policy-update`
with an empty `ExpectedRevision` creates only an absent project policy. Later
updates require the exact currently inspected revision. Zero retention values
select 90-day history, 30-day inactive task and 30-day backup defaults.

## Exact deletion

1. Supply a bounded JSON selection to `kb-admin delete --preview`: ProjectID and
   exact IDs, SessionIDs or TaskIDs. The preview expands same-project identical
   content and server-linked prepared/published excerpts. It returns the complete
   immutable manifest and SelectionDigest. Opaque identities may include Unicode
   and slash characters; they are encrypted identity metadata, never interpreted
   as filesystem paths or matching expressions.
2. Review that expanded selection. Execute `kb-admin delete` with the same
   selection, a new IdempotencyKey and ExpectedSelectionDigest from the preview.
   A changed selection is stale and does not silently broaden the deletion.
   Overall expanded identities/digests/generations are capped at 1000; oversized
   selections fail closed. There is no wildcard/global deletion.
3. Keep the complete confirmation payload for retries. A pending/unknown journal
   outcome is an error, never durable acceptance. Identical replay uses the same
   operation identity. `deletion-status` returns receipt metadata while startup
   recovery is blocked; it never returns source text.

The locked first phase immediately tombstones selected IDs, exact project/content
fingerprints and selected session/task identities, invalidates claims and complete
published dependents, invalidates prepared derivations, and advances deletion and
knowledge epochs. Explicit deletion and privacy exclusions override retention
pins. No object/journal I/O runs inside graph transaction callbacks.

The full bounded replay manifest, immutable operation ID, request digest and
manifest digest are appended outside the graph transaction to the independently
configured encrypted authenticated journal. Only confirmed append durability
allows accepted `purge_pending` acknowledgement and deterministic purge jobs.
`pending_journal` remains inaccessible and retryable. Restart retries stored
pending manifests without reconstructing/broadening their selections.

`purge_complete` means the confirmed active-object deletes and graph payload
scrubbing settled under current generation and live job lease fences. Unknown
provider deletion results remain pending. Evidence S3 uses create-only writes and
retains a minimal zero-byte opaque fence marker at each deleted generation key;
confirmed marker/readback prevents delayed Put from recreating source bytes.
Fence markers must remain durable: provider lifecycle expiry or physical deletion
of evidence generation keys must not remove them.
All legacy unconditional object writers must drain before deploying this protocol,
and O2 must qualify conditional writes on the actual evidence endpoint. `BackupState=expiry_pending` remains
explicit: encrypted backups may retain the source until the qualified backup
retention process expires those backup sets. O1 promises no immediate backup
removal and implements no speculative notifications/import/dream jobs.

## Retention and resurrection

Server ingestion timestamps are set once on insert. Caller ObservedAt is not
retention authority. Accepted actual task writes advance TaskActivity; reads,
worker audits, retries and rejected CAS do not. Retention decisions use UTC server
time and inclusive `timestamp <= now-retention` boundaries. Legacy data receives
a conservative migration baseline; unknown legacy ages are not immediately
expired. Identity projections are rebuilt from retained immutable JSON in bounded
locked pages before journal replay/serving.

Ordinary history expiry preserves original sources, excerpts, immutable support
revisions and exact artifact generations needed by complete F5 eligibility.
A same-byte identity with a live pin is retained conservatively. Inactive tasks
with publication support pins remain retained. Explicit host deletion and privacy
exclusions override these pins and retire publication eligibility before purge.

Capture rejects tombstoned ID, session/task scope and exact project/content bytes
before upload, then rechecks after Put. Completion, reference, append, sweep and
reconciliation cannot overwrite deleted availability. Checkpoint and candidate
writes gate deleted session/task identity even with a different record ID.
Prepared derivation reservation and exact generation binding precede excerpt Put;
final publication rechecks that reservation and the complete support closure.

Resurrection prevention recognizes **exact verified bytes within a project**, plus
authoritative task/session identities and server-authored derivation links. It
cannot recognize changed bytes supplied under a new untrusted locator as the same
semantic source. No reingestion authorization exception is implemented. Host/client
calls cannot clear tombstones, and changing policy or source IDs cannot bypass
exact-byte barriers. Trusted semantic namespaces remain a separate future task.

## Policy revisions

Inspect current policy before CAS update. Each immutable revision preserves its
restrictions and original artifact provenance. Pausing and changed revisions
immediately exclude old-policy sources. Reconciliation writes a separate exact
PolicyEligibility binding `(artifactID,generation,digest,currentPolicyRevision)`;
F4, F5 and I1 consume that same binding. It never relabels original bytes, clears
tombstones, bypasses health/support checks or broadens original source paths.
A later revision invalidates the prior binding until reevaluated.

Harmless policy changes can restore healthy retained evidence through that
binding. Newly excluded source paths and newly forbidden fields require lifecycle
journal removal: old opaque bytes cannot prove retroactive filtering. Such sources
remain denied while removal is pending. Widening cannot revive deleted sources.
Implemented capture/publication work is cancelled/quarantined on invalid input;
future notification dispatch, C4 imports and dreaming must recheck current policy,
source/session tombstones, generations and deletion epoch immediately before any
external effect. O1 makes no notification cancellation or import acceptance claim.

## Startup, journal capacity and restore

Every serving API/worker/owner source operation must complete Recover before
admitting reads, writes or object effects. Recovery authenticates a freshly read
independent external head, validates continuity from the graph watermark, retries
saved pending intents, replays full deletion manifests in bounded pages and
advances the safe graph barrier. Missing key/head/config, invalid ciphertext,
broken/divergent chain, stale prefix, conflicting operation or unmatched pending
intent leaves readiness closed. Maintenance status and exact deletion retry remain
available to repair durability failures. Startup never creates an absent journal.

`journal-init` is only for a source-empty fresh graph with no previous graph
journal barrier, source data, publication, deletion operation or tombstone. It must
not repair a missing journal after restore. Restore the original independent
journal namespace and required keys instead. Changing a namespace/key or resetting
a graph watermark is not an approved recovery path.

The first journal slice supports **at most 10,000 linked operations per namespace**.
Append fails closed at capacity. Recovery pages past 1000 records, with each
provider traversal bounded at 10,000. O2 must expose capacity/age monitoring with
ample warning and extend and qualify the bounded authenticated index before exhaustion.
Do not rotate/reset the namespace to erase deletion history. A valid old encrypted
prefix alone is insufficient freshness proof. The approved threat model trusts the independent storage operator. That operator
can destroy or roll back the complete journal namespace; encryption cannot detect
a total independent-storage rollback without another trusted anchor. O2 must
qualify and document this residual trust and any available protection, rather than
assume unavailable provider immutability/versioning. Ordinary graph snapshot
restore must never restore or roll back the independent journal.

The real local S3 fixture qualifies conditional create/CAS, authenticated envelope
validation, independent replay and strong head reads for that pinned local
provider only. Production endpoint behavior, least privilege, key recovery,
rotation, durability, backup retention and an actual offline restore exercise
remain explicit O2 requirements. Preserve old keys while any retained envelope
requires them; lost keys must fail closed, never trigger replacement initialization.

Within the explicitly selected project, immutable DEPENDS_ON edges and stored
complete publication support closures identify dependent revisions and excerpts.
Preview includes that bounded exact closure. Computed dependent revisions add their
payload and excerpt outputs; their independent upstream evidence is preserved
unless its complete bytes separately match an explicitly expanded source identity.
Directional source-to-excerpt traversal prevents reverse traversal to unrelated
original sources. Dependency projections have a separate startup version and
survive payload scrubbing. Cross-project supporting claims remain ineligible when
their support is deleted, but O1 does not silently delete another project's data;
explicit cross-project propagation/authority semantics remain future work.

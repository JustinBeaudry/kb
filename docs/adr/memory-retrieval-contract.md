# F5 graph and retrieval contract for I1, C3 and O1

This is the implemented foundation contract, not a publication API or a live
acceptance claim. Publication records in F5 tests are synthetic setup only.

- `ClaimRevision.id` is the immutable opaque revision; `claimID` is the logical
  claim ID. `data` holds immutable domain JSON; indexed `status` and `valid` are
  authoritative eligibility state. `projectID`, `taskID`, `clientID`, `text` and
  `kind` support discovery. `SubmittedBy` in domain data identifies the submitting
  client independently of artifact uploader. `Claim-[:HEAD]->ClaimRevision` is
  the compare-and-swap submission head. Candidate writes increment
  `OwnerState.revisionEpoch`. New writes are always `candidate`, never verified. Evidence-free submitted
  task hypotheses are allowed; they remain unverified and task-local.
- Optional `CandidateInput.EntityIDs` / `ClaimRevision.EntityIDs` are structural
  identity hints, at most 50 nonempty IDs. `Entity` contains only `projectID,id`,
  unique together. `ClaimRevision-[:ABOUT]->Entity` records the hint atomically;
  Entity never stores mutable facts. Request `entity_ids` means these entity IDs,
  never claim/revision IDs. `query` supports exact claim/revision IDs and lexical
  text. Cross-project hint lookup examines ABOUT links on the published generalized
  target only. I1 must approve those target identities as safe to share; source and
  support ABOUT links never participate in generalized discovery.
- `Publication` retains domain ID, immutable target `claimRevision`, `revision`,
  `policyRevision`, and `sharingScope` (`project` or `cross_project`). `valid=true`
  is required for retrieval. `supportRevisions` must be an explicit complete
  transitive closure, including an empty array for a target with no dependencies.
  `generalizedText` is required for cross-project output and must equal the verified
  lesson target's text; `kind` must be `lesson`. No fallback to restricted text.
  Missing/incomplete/duplicate/cyclic closure or more than 1000 supporting revisions
  is ineligible. I1 must leave such candidates unpublished.
- A published target is a new project-scoped immutable revision. I1 creates a
  distinct generalized target for cross-project sharing. It does not mutate a
  task candidate's scope. Hidden supporting findings may remain task-scoped and
  need authoritative validity/status/evidence, without being publicly retrievable.
  Published targets cite separately allowed project-scoped excerpts; I1 creates
  and pins those excerpts with original lineage/authenticity. F5 never exposes
  hidden supporting task revision IDs, artifact IDs, locators, or source text.
- `EVIDENCE` links bind `digest,locator` to the artifact; immutable domain references
  retain source revision and authenticated origin. Eligibility rechecks every
  target/support source through indexed artifact ID: ready state, available health,
  exact scope/digest/locator, tombstones and current policy/path exclusions. Internal
  abandoning/abandoned states are ineligible. Fetch additionally reads/hashes the
  stored bytes outside transactions, then rechecks locked metadata/epochs; failed
  integrity reads fence availability by the expected object key. The ID-only Fetch
  endpoint requires evidence:read plus retrieval:read, rechecked against the current
  authoritative principal, and can return only project-scoped sources of eligible publications, never
  task sources or candidate-only artifacts. Source bytes remain explicitly
  unverified evidence (`candidate`, submitted, with a warning), even when a claim
  using them is verified. An outcome is never fabricated from publication status.
- `Tombstone.id` predicates apply to revision IDs, logical claim IDs, artifact IDs
  and checkpoint IDs as appropriate. O1 must preserve these authoritative
  predicates and increment `deletionEpoch` / `revisionEpoch` for corresponding
  deletion, correction, publication and policy state changes. A stale projection
  cannot override authoritative indexed validity/status or tombstones.
- `TaskCheckpointOwner(projectID,taskID)` binds the authenticated client and holds
  `headID,headRevision`. Put updates checkpoint data and head atomically under
  OwnerState. Ownership is independent of session. Get/latest require existing
  `retrieval:read`, project authority and matching task/client ownership. Read
  snapshots omit stored evidence IDs rather than expose unchecked dependencies,
  explicitly setting `evidence_references_omitted`. Task state is separately labeled
  `task_state`, conveys no claim verification, and counts in the same wire budget.
- Candidate task ownership/current grants are checked before calling external
  source integrity reads and rechecked in the final write transaction. Raw Fetch
  requires evidence:read as well as retrieval:read before object I/O.
- Eligibility takes the explicit requested Scope separately from Principal grants:
  `Eligible(ctx,tx,p,requestedScope,revisionIDs)`. Retrieval passes its request scope,
  candidate dependency checks pass candidate scope, and ID-only Fetch passes the
  artifact's project scope. A cross-project request always projects the same safe
  generalized target whether or not its client also has source-project access.
  A separately authorized same-project request may retain permitted provenance.
- Target applicability remains authoritative through filtering and ranking and is
  optionally exposed on each chunk as `applicability`. Those public target conditions
  include versions, environment, repository/dirty qualifiers and validity times when
  present; unknown/absent values are preserved. I1 must verify these fields as safe to
  share together with the distinct target's text and EntityIDs. Hidden source/support
  Applicability is never copied into this public target projection. Both canonical
  JSON and HTTP output budget the complete optional object.
- Confirmed missing/corrupt objects (`ErrIncomplete`, including successful-read
  length/digest mismatch) fence generation-specific availability. Provider/network,
  mid-read or Close errors are unknown observations (`ErrUnavailable`): Fetch,
  Complete, Reference, Append and Reconcile preserve last-confirmed availability.
  Sweep still advances checkedAt on unknown observations but performs no availability
  write, so it cannot restore a concurrently recorded known-missing result. A later
  successful audit may confirm availability normally. This corrects F4's earlier
  overly conservative transient-error invalidation policy.
- Every graph read/eligibility/final epoch check shares the real OwnerState lock in
  a service-owned transaction. No object I/O or nested Within runs in its callback.
  Retrieval authenticates authoritative current client grants/revocation, not only
  the request's principal metadata. Revocation changes authEpoch; a newly enrolled
  credential returns that same authoritative epoch, including after revocation.
- Continuations are HMAC-SHA256 signed using operator configuration
  `KB_RETRIEVAL_HMAC_KEY` (exactly 32 bytes represented by 64 hex characters; absent
  or malformed configuration fails startup). Rotate it to invalidate old cursors.
  Fingerprints bind client, scope, query/entities, mode, applicability and every
  budget parameter, plus auth/knowledge/deletion epochs and current task head.
  An eligible-result digest also detects changed temporal applicability/availability
  without silently mixing result sets. Restart the original scoped request on stale.
- `Visited` counts every returned seed/neighbor expansion row, including rejected
  and duplicate records; repositories LIMIT before decoding. Maximum 50 examined
  expansion rows and depth 2 per request. Dependency validity reads are a separate
  indexed bound of 1000 support revisions per target, with at most 50 evidence refs
  per claim. Supporting records do not expand the discovery frontier. No unbounded
  fanout is collected in Go; publication lookup is capped at two rows and fails
  closed on ambiguity. Oversized support arrays are suppressed in Cypher before
  returning them. Existing lexical discovery can scan indexed graph projections;
  this is bounded context retrieval, not an exhaustive graph export.
- Cursor pagination covers eligible chunks in that bounded exploration snapshot.
  A graph frontier limit with no withheld eligible chunk has no continuation;
  clients narrow query/entity hints. `truncation_reason` distinguishes
  `visited_limit`, `wire_budget` and `task_state_budget`, with a safe `action`. No server cursor-state store or exhaustive
  graph enumeration is implied. Each renderer budgets its complete own framing:
  service canonical JSON, HTTP DTO JSON plus newline, C3 CLI length prefix, and
  eventual MCP protocol frames. HTTP orientation <=4096 bytes; evidence <=16384.
  `server/api/testdata/retrieval-v3.json` supplies safe synthetic HTTP/CLI target
  envelopes; C3 must test its actual renderer and preserve local schema-v2 behavior.

## Implemented host owner adjudication (I1)

`kb-admin` alone composes host owner recovery and adjudication. It resolves the
persisted owner under OwnerState; no owner bearer, Client, fabricated collector,
or HTTP authorization exception is created. Client enrollment reserves the
`host-owner:` uploader namespace. Listing returns bounded metadata without bearer
or token digest. Host identity recovery is available; backup/restore remains O2.

Owner `verify` previews bind the exact immutable draft, heads, current policy,
complete support closure, and original source generations using bounded canonical
JSON. Execution requires that exact digest. The owner supplies public text, kind,
applicability, EntityIDs and source UTF-8 byte spans explicitly. Conditions cannot
broaden supporting applicability. Rejection affects only a candidate's indexed
state; neither action rewrites task JSON/scope/HEAD. A distinct immutable project
target cites only selected project excerpts; original task evidence stays hidden.

`Verification` stores immutable owner_adjudicated outcome, checked time and
owner_authenticated decision origin. Excerpts and original uploaded evidence
remain submitted, with no claimed execution attestation. `OwnerDecision` stores
owner-scoped key, full request/input digest and original receipt/target revision;
identical replay precedes all current-state checks and performs no writes.
`ExcerptLineage` stores source artifact/digest/generation, original evidence
SourceRevision, separate candidate SupportRevision, exact byte span, excerpt
artifact/digest/generation, source origin and decision digest. PINS_SOURCE,
PINS_EXCERPT and PINS_SUPPORT retain both evidence ends and the hidden finding.
O1 will enforce retention and explicit deletion overriding pins.

Final publication, both verification records, target HEAD CAS, lineage/pins,
decision receipt and revision epoch commit in one locked transaction. F4 prepares
objects outside that transaction; unpublished prepared artifacts confer no Fetch
access. F4 reconciliation/extraction jobs are the only existing supported work;
I1 creates no speculative publication/index worker kinds. Invalidation previews
bind exact authoritative support state/head/digest. Execution invalidates the
support plus every stored complete-closure dependent in database-side updates,
without collecting an unbounded fanout in Go. F5 still excludes unhealthy or
policy-ineligible support immediately, even with stale discovery projections.

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

package domain

// RevisionState keeps immutable submitted JSON separate from authoritative state.
type RevisionState struct {
	Claim  ClaimRevision
	Status ClaimStatus
	Valid  bool
}

// OwnerDecision is host-only idempotency state; RequestDigest binds the exact
// confirmation, so a replay never silently accepts a newly computed digest.
type OwnerDecision struct {
	ID, OwnerID, Key, InputDigest, RequestDigest, Action string
	CandidateRevision, TargetRevision                    string
	Receipt                                              Receipt
}

// ExcerptLineage is internal provenance. Both ends are pinned by this record;
// public retrieval never projects these original source identifiers.
type ExcerptLineage struct {
	ID, DecisionID, InputDigest                                      string
	SourceArtifactID, SourceRevision, SourceDigest, SourceGeneration string
	SupportRevision                                                  string
	Start, End                                                       int64
	ExcerptArtifactID, ExcerptDigest, ExcerptGeneration              string
	SourceOrigin                                                     Origin
}

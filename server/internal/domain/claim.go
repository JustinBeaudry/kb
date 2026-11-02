package domain

import "time"

// MaxRevisionBytes bounds every claim and checkpoint revision the server
// issues. Servers advertise it in status so clients can size write receipts
// before mutating.
const MaxRevisionBytes = 64

type Applicability struct {
	RepositoryRevision, DirtyDigest, Environment string
	Versions                                     map[string]string
	ValidFrom, ValidUntil                        *time.Time
}

type ClaimStatus string

const (
	ClaimCandidate   ClaimStatus = "candidate"
	ClaimVerified    ClaimStatus = "verified"
	ClaimSuperseded  ClaimStatus = "superseded"
	ClaimDisputed    ClaimStatus = "disputed"
	ClaimInvalidated ClaimStatus = "invalidated"
	ClaimRejected    ClaimStatus = "rejected"
)

// Revision identifiers are immutable opaque strings; creating a new version
// never changes an existing revision or its evidence binding.
type ClaimRevision struct {
	EntityIDs                     []string `json:",omitempty"`
	SubmittedBy                   string   `json:",omitempty"`
	ClaimID, Revision, Text, Kind string
	Scope                         Scope
	Evidence                      []EvidenceRef
	DependsOn                     []string
	Applicability                 Applicability
	Status                        ClaimStatus
}

type CandidateInput struct {
	EntityIDs                             []string
	ClaimID, ExpectedRevision, Text, Kind string
	Scope                                 Scope
	Evidence                              []EvidenceRef
	DependsOn                             []string
	Applicability                         Applicability
}

type VerificationOutcome string

const (
	VerificationSupported   VerificationOutcome = "supported"
	VerificationUnsupported VerificationOutcome = "unsupported"
	VerificationAmbiguous   VerificationOutcome = "ambiguous"
)

type Verification struct {
	ID, ClaimRevision, PolicyRevision, EvidenceDigest string
	Outcome                                           VerificationOutcome
	Method                                            string
	Origin                                            Origin
	CheckedAt                                         time.Time
}

type Publication struct {
	ID, ClaimRevision, SharingScope, Revision string
	SupportRevisions                          []string
	PolicyRevision                            string
}

package domain

import (
	"strings"
	"time"
)

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
	// Verification is the authoritative supporting check, attached on read
	// only. It is never part of the immutable submitted revision data.
	Verification *Verification `json:"-"`
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
	Reasons                                           []ReasonCode `json:",omitempty"`
	DistinctOrigins                                   []string     `json:",omitempty"`
}

// Sharing scopes. A cross-project lesson is discoverable by other projects
// only through its generalized text; sources and identifiers stay private.
const (
	SharingProject      = "project"
	SharingCrossProject = "cross_project"
)

// Publication states returned by lesson publication. Pending and rejected
// outcomes write nothing and carry reason codes.
const (
	PublicationPublished = "published"
	PublicationPending   = "pending"
	PublicationRejected  = "rejected"
)

type Publication struct {
	ID, ClaimRevision, SharingScope, Revision string
	SupportRevisions                          []string
	PolicyRevision                            string
	GeneralizedText                           string       `json:",omitempty"`
	State                                     string       `json:",omitempty"`
	Reasons                                   []ReasonCode `json:",omitempty"`
}

// LessonInput requests a separate lesson derived from verified support.
type LessonInput struct {
	ProjectID, LessonID, ExpectedHead, Text, SharingScope string
	Applicability                                         Applicability
	Counterexamples, SupportRevisions                     []string
}

// LessonText renders the stored lesson with its counterexamples so they are
// never separated from the generalization they qualify.
func LessonText(text string, counterexamples []string) string {
	if len(counterexamples) == 0 {
		return text
	}
	return text + "\nNot applicable when: " + strings.Join(counterexamples, "; ")
}

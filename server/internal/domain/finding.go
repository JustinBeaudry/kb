package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"time"
)

type FindingStatus string

const (
	// FindingPending awaits delivery (immediate notice or daily digest).
	FindingPending FindingStatus = "pending"
	// FindingNotified was delivered once; identical findings never resend.
	FindingNotified FindingStatus = "notified"
	// FindingWithheld is sensitive: available on demand, never sent to Buzz.
	FindingWithheld FindingStatus = "withheld"
	// FindingSuppressed lost relevance (support changed) before delivery.
	FindingSuppressed FindingStatus = "suppressed"
)

type FindingUrgency string

const (
	FindingImmediate FindingUrgency = "immediate"
	FindingDaily     FindingUrgency = "digest"
)

// Finding is a dream result linked to the candidate it produced. Digest is
// stable across cycles, so an unchanged conclusion is recognized as such.
type Finding struct {
	ID, Digest, Kind, Text, Rationale string
	CandidateRevision, JobID          string
	PolicyRevision                    string
	Scope                             Scope
	SourceRevisions                   []string
	Applicability                     Applicability
	Urgency                           FindingUrgency
	Sensitive                         bool
	Status                            FindingStatus
	CreatedAt                         time.Time
}

// FindingDigest normalizes whitespace and case and sorts support so equivalent
// conclusions over the same revisions share one identity.
func FindingDigest(projectID string, kind ProposalKind, text string, sources []string) string {
	sorted := slices.Clone(sources)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	normalized := strings.ToLower(strings.Join(strings.Fields(text), " "))
	h := sha256.New()
	for _, part := range append([]string{projectID, string(kind), normalized}, sorted...) {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

type NoticeState string

const (
	NoticePending    NoticeState = "pending"
	NoticeSent       NoticeState = "sent"
	NoticeSuppressed NoticeState = "suppressed"
)

// Notice is an outbox record. Its ID is the delivery operation key, so a
// retried send after a lost acknowledgment is deduplicated downstream.
type Notice struct {
	ID         string
	Urgency    FindingUrgency
	ProjectID  string
	FindingIDs []string
	State      NoticeState
	DueAt      time.Time
}

// DreamingStatus is the owner-visible operating state of background dreaming.
type DreamingStatus struct {
	State                    string
	PendingJobs              int
	ReasonCodes              []string
	DailyCapMicroUSD         int64
	CommittedMicroUSD        int64
	UnreconciledReservations int
}

// Dreaming identities shared by services and persistence.
const (
	DreamJobKind  = "dream.consolidate"
	NotifyJobKind = "dream.notify"
	// DreamClientID authors every dream candidate. Revisions it authored are
	// never sent back as evidence and never count as support.
	DreamClientID = "kb-dreaming"
)

// DreamInput is one claim revision as dreaming sees it. Current is false once
// the revision is no longer its claim's live head or has been deleted.
type DreamInput struct {
	Revision, ClaimID, Text, SubmittedBy string
	Scope                                Scope
	Status                               ClaimStatus
	Current                              bool
}

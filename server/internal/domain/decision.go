package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

type DecisionState string

const (
	DecisionPending  DecisionState = "pending"
	DecisionAccepted DecisionState = "accepted"
	DecisionRejected DecisionState = "rejected"
	DecisionExpired  DecisionState = "expired"
	DecisionStale    DecisionState = "stale"
)

// Owner reply actions. A decision records owner judgment only; it is never an
// empirical verification outcome.
const (
	DecisionApprove = "approve"
	DecisionReject  = "reject"
)

// DefaultDecisionTTL bounds how long an owner approval request stays answerable.
const DefaultDecisionTTL = 24 * time.Hour

type Decision struct {
	ID, CandidateRevision, EvidenceDigest, RequestedAction, OwnerID string
	ExpiresAt                                                       time.Time
	State                                                           DecisionState
	// Delivery and resolution metadata. ThreadID/ChannelID are set only after
	// Buzz accepted the post; an undelivered decision stays pending.
	ProjectID, RequestedBy, ChannelID, ThreadID, ResolvedByEvent string
	CreatedAt                                                    time.Time
}

type DecisionReply struct {
	DecisionID, CandidateRevision, EvidenceDigest, Action, EventID, SignerID string
	Signature                                                                []byte
}

// OwnerJudgment is the durable record of an owner's adjudication. It is stored
// separately from Verification so owner judgment never masquerades as an
// empirical check.
type OwnerJudgment struct {
	ID, DecisionID, CandidateRevision, EvidenceDigest, Action string
	OwnerID, SignerID, EventID, Channel                       string
	RecordedAt                                                time.Time
}

// DecisionReplyReceipt deduplicates owner replies by event identity. The digest
// binds the exact reply so a replay returns the prior outcome without effect.
type DecisionReplyReceipt struct {
	EventID, DecisionID, ReplyDigest string
	State                            DecisionState
}

// SignedMessage is a vendor-neutral, locally verified Buzz/Nostr event. The
// gateway only returns messages whose event ID and BIP-340 signature verified.
type SignedMessage struct {
	EventID, PubKey, ChannelID, ThreadRootID, ParentID, Content string
	Kind                                                        int
	CreatedAt                                                   time.Time
	Signature                                                   []byte
}

// MessageReceipt identifies one accepted post. Reconciled reports that the post
// was found by operation identity instead of a fresh acknowledgement.
type MessageReceipt struct {
	OperationKey, EventID, ThreadID, ChannelID string
	Reconciled                                 bool
}

// DecisionReplyDigest is the canonical 32-byte message an enrolled owner signs
// (BIP-340) to answer a decision outside Buzz. EventID is a caller nonce.
func DecisionReplyDigest(r DecisionReply) [32]byte {
	return sha256.Sum256([]byte(strings.Join([]string{"kb-decision-reply/v1", r.DecisionID, r.CandidateRevision, r.EvidenceDigest, r.Action, r.EventID}, "\n")))
}

// DecisionActionDigest binds one owner action on one exact decision revision.
func DecisionActionDigest(decisionID, action, candidateRevision, evidenceDigest string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{"kb-decision-action/v1", decisionID, action, candidateRevision, evidenceDigest}, "\n")))
	return hex.EncodeToString(sum[:])
}

// MaxJudgmentPage bounds one page of the owner judgment review listing.
const MaxJudgmentPage = 500

// JudgmentQuery selects owner judgments for review on the host, for example
// everything a retired or stolen key signed. Signer is a 64-hex public key,
// Since a YYYY-MM-DD UTC day, and After the Next cursor of a previous page.
type JudgmentQuery struct {
	Signer, Since, After string
	Limit                int
}

// JudgmentRecord is one owner judgment as the review listing shows it, with the
// project of the decision it answered and whether its signing key is the one
// enrolled now.
type JudgmentRecord struct {
	ID                string    `json:"id"`
	RecordedAt        time.Time `json:"recorded_at"`
	SignerID          string    `json:"signer"`
	CurrentKey        bool      `json:"current_key"`
	Action            string    `json:"action"`
	ProjectID         string    `json:"project_id"`
	DecisionID        string    `json:"decision_id"`
	RequestedAction   string    `json:"requested_action"`
	CandidateRevision string    `json:"candidate_revision"`
	EvidenceDigest    string    `json:"evidence_digest"`
	OwnerID           string    `json:"owner_id"`
	EventID           string    `json:"event_id"`
	Channel           string    `json:"channel"`
}

// JudgmentPage is one page of owner judgments, oldest first.
type JudgmentPage struct {
	Judgments []JudgmentRecord `json:"judgments"`
	Next      string           `json:"next,omitempty"`
}

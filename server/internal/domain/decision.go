package domain

import "time"

type DecisionState string

const (
	DecisionPending  DecisionState = "pending"
	DecisionAccepted DecisionState = "accepted"
	DecisionRejected DecisionState = "rejected"
	DecisionExpired  DecisionState = "expired"
	DecisionStale    DecisionState = "stale"
)

type Decision struct {
	ID, CandidateRevision, EvidenceDigest, RequestedAction, OwnerID string
	ExpiresAt                                                       time.Time
	State                                                           DecisionState
}

type DecisionReply struct {
	DecisionID, CandidateRevision, EvidenceDigest, Action, EventID, SignerID string
	Signature                                                                []byte
}

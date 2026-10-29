package domain

type RetrievalMode string

const (
	RetrievalOrientation RetrievalMode = "orientation"
	RetrievalEvidence    RetrievalMode = "evidence"
	RetrievalWorking     RetrievalMode = "working"
)

type RetrievalRequest struct {
	Scope                             Scope
	Query                             string
	EntityIDs                         []string
	Applicability                     Applicability
	BudgetBytes, MaxDepth, MaxVisited int
	Continuation                      string
	Mode                              RetrievalMode
}

// ClaimStatus, OriginTrust and VerificationOutcome are independent. A candidate
// submission has no verification outcome until an actual verification exists.
type EvidenceChunk struct {
	RecordID, Revision, Text string
	ClaimStatus              ClaimStatus
	OriginTrust              OriginTrust
	VerificationOutcome      *VerificationOutcome
	Evidence                 []EvidenceRef
	Warnings                 []string
}

type TaskState struct {
	Label      string
	Checkpoint Checkpoint
}

type RetrievalResult struct {
	TruncationReason string
	Action           string
	TaskCheckpoint   *TaskState
	TaskStateOmitted bool
	SchemaVersion    string
	Chunks           []EvidenceChunk
	Truncated        bool
	Continuation     string
	Visited          int
	NoResults        bool
}

type RetrievalSnapshot struct {
	Principal                                Principal
	AuthEpoch, KnowledgeEpoch, DeletionEpoch uint64
	TaskHead, TaskOwner                      string
}
type EligibilityResult struct {
	Claims      []ClaimRevision
	Snapshot    RetrievalSnapshot
	Unavailable bool
}

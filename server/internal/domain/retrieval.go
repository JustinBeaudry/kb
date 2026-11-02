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
	Applicability            *Applicability
	Passage                  *Passage
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

// FetchMode separates ID-only publication evidence from explicitly task-scoped
// working evidence. Working mode never broadens ID-only access.
type FetchMode string

const (
	FetchPublication FetchMode = "publication"
	FetchWorking     FetchMode = "working"
)

// FetchRequest selects a bounded passage of one stored artifact. Offsets index
// the stored artifact bytes; EndByte nil means the end of the artifact. Scope is
// accepted only in working mode, where project and task are both required.
// Continuation requests omit StartByte (zero) and preserve every other field;
// the signed cursor retains the original start and current page offset.
type FetchRequest struct {
	ArtifactID, Locator string
	Mode                FetchMode
	Scope               Scope
	BudgetBytes         int
	StartByte           int64
	EndByte             *int64
	Continuation        string
}

// Passage reports the exact selected UTF-8 byte span of a stored artifact.
// The excerpt is the whole source only when StartByte is 0 and EndByte equals
// SourceBytes.
type Passage struct {
	StartByte, EndByte, SourceBytes int64
}

// Status is the authenticated client's own authoritative view; other clients
// and owner administration are never listed.
//
// MaxRevisionBytes is the server's enforced bound on every write receipt's
// revision; zero means the server did not advertise one (older server).
type Status struct {
	MaxRevisionBytes       int
	ConnectionState        string
	AuthorizedProjectIDs   []string
	AuthorizedCapabilities []string
	CapabilityGaps         []string
}

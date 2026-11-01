package domain

import "time"

// MaxArtifactBytes bounds one stored evidence artifact (capture chunk size).
const MaxArtifactBytes int64 = 8 << 20

type EventKind string

const (
	EventMessage    EventKind = "message"
	EventToolCall   EventKind = "tool_call"
	EventToolResult EventKind = "tool_result"
	EventCheckpoint EventKind = "checkpoint"
	EventArtifact   EventKind = "artifact"
)

type Completeness string

const (
	CompletenessComplete Completeness = "complete"
	CompletenessPartial  Completeness = "partial"
	CompletenessWithheld Completeness = "withheld"
)

type Event struct {
	ID, IdempotencyKey, Digest string
	Scope                      Scope
	Kind                       EventKind
	ObservedAt                 time.Time
	PayloadArtifactID          string
	Sequence                   uint64
	Completeness               Completeness
}

type OriginTrust string

const (
	TrustSubmitted          OriginTrust = "submitted"
	TrustCollectorAttested  OriginTrust = "collector_attested"
	TrustGatewayObserved    OriginTrust = "gateway_observed"
	TrustOwnerAuthenticated OriginTrust = "owner_authenticated"
)

// Origin is derived from authenticated server context, never accepted as
// authority from a submitted event or a transcript role label.
type Origin struct {
	ClientID      string
	Trust         OriginTrust
	AttestationID string
}

type ArtifactState string

const (
	ArtifactStaged  ArtifactState = "staged"
	ArtifactReady   ArtifactState = "ready"
	ArtifactDeleted ArtifactState = "deleted"
	// Internal capture retirement states; they do not represent owner deletion.
	ArtifactAbandoning ArtifactState = "abandoning"
	ArtifactAbandoned  ArtifactState = "abandoned"
)

type Artifact struct {
	ID, ObjectKey, Digest, PolicyRevision string
	SourceLocator, SourcePath             string
	Generation                            string
	UploadLeaseID                         string    `json:"-"`
	UploadLeaseUntil                      time.Time `json:"-"`
	ObjectAvailable                       bool      `json:"-"`
	Scope                                 Scope
	Bytes                                 int64
	State                                 ArtifactState
	ExpiresAt                             *time.Time
}

type EvidenceRef struct {
	ArtifactID, Digest, Locator, SourceRevision string
	Origin                                      Origin
	Applicability                               Applicability
}

type Receipt struct {
	ID        string
	Duplicate bool
	State     string
}

package domain

import "time"

const MaxBackupObjects = 100000

// BackupObject records every graph artifact, including source/support/excerpt
// generations and tombstones. Only Available, nondeleted records carry bytes.
type BackupObject struct {
	ArtifactID, ObjectKey, Generation, Digest, State string
	Bytes                                            int64
	Available, Deleted                               bool
}
type BackupManifest struct {
	Version                                        int
	BackupID, ArchiveNamespace, EvidenceNamespace  string
	SchemaDigest, ToolVersionsDigest, Architecture string
	RetrievalKeyDigest                             string
	CreatedAt                                      time.Time
	Journal                                        JournalBarrier
	Objects                                        []BackupObject
	Neo4jDumpDigest, SystemDumpDigest              string
	Neo4jDumpBytes, SystemDumpBytes                int64
	GraphStoreBytes                                int64
	KeyIDs                                         []string
	Completed                                      bool
}
type BackupResult struct {
	ID                     string
	StartedAt, CompletedAt time.Time
	State                  string
}

// BackupOperation is kept on the graph volume independently from both databases.
// A failed/interrupted operation never proves that its external effects ended.
type BackupOperation struct {
	TerminalState                        string
	RestoreIncarnation                   string
	FailedStage                          string
	VerifiedSnapshot                     bool
	Version                              int
	ID, BackupID, Kind, Stage, Directory string
	Fence                                MaintenanceFence
	StartedAt, CompletedAt               time.Time
}

type BackupSchedule struct {
	LastSnapshot, LastAttempt, NextAttempt time.Time
	AttemptID                              string
	Failures                               uint64
}
type BackupProcessState struct {
	Version          int
	Generation, Root string
}

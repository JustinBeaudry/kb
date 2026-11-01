package domain

import "time"

const MaxDeletionEntries = 1000
const MaxJournalRecordBytes = 1 << 20

type JournalBarrier struct {
	Namespace string
	Sequence  uint64
	Digest    string
}

// JournalRecord contains the entire bounded replay intent, never source prose,
// locators, object keys, credentials, or raw paths.
type JournalRecord struct {
	Version                                            int
	OperationID, Action, ManifestDigest, RequestDigest string
	Manifest                                           DeletionManifest
	CreatedAt                                          time.Time
}
type DeletionArtifact struct{ ID, Generation, Digest string }
type DeletionManifest struct {
	ProjectID          string
	IDs, SourceDigests []string
	Artifacts          []DeletionArtifact
	Sessions, Tasks    []string
}
type DeleteSelection struct {
	ProjectID                               string
	IDs, SessionIDs, TaskIDs                []string
	IdempotencyKey, ExpectedSelectionDigest string
	ExpiryAt                                *time.Time `json:"-"`
}
type DeletionPreview struct {
	Manifest        DeletionManifest
	SelectionDigest string
}
type DeletionReceipt struct {
	ID, State, ManifestDigest string
	Barrier                   JournalBarrier
	BackupState               string
}
type DeletionOperation struct {
	Receipt       DeletionReceipt
	Record        JournalRecord
	RequestDigest string
}
type PurgeObject struct {
	ID, Key, Generation, Digest string
	UploadLeaseUntil            time.Time
}
type ExpiryReport struct{ Operations []DeletionReceipt }

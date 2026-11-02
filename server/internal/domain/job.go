package domain

import "time"

type JobStatus string

const (
	JobQueued    JobStatus = "queued"
	JobRunning   JobStatus = "running"
	JobSucceeded JobStatus = "succeeded"
	JobFailed    JobStatus = "failed"
	JobCancelled JobStatus = "cancelled"
)

type Job struct {
	ID, Kind, InputDigest      string
	PolicyRevision, ReasonCode string
	Scope                      Scope
	InputRevisionIDs           []string
	Status                     JobStatus
	Attempts                   int
	DueAt                      time.Time
}

type Lease struct {
	JobID, WorkerID string
	Fence           uint64
	ExpiresAt       time.Time
}

// JobEffect is a durable completion notification. It intentionally contains only
// references and digests, never prompts, source payloads, or credentials.
type JobEffect struct {
	ID, JobID, Kind, InputDigest, PolicyRevision string
	Fence                                        uint64
	CreatedAt                                    time.Time
}

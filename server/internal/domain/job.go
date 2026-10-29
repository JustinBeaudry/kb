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
	ID, Kind, InputDigest string
	Scope                 Scope
	InputRevisionIDs      []string
	Status                JobStatus
	Attempts              int
	DueAt                 time.Time
}

type Lease struct {
	JobID, WorkerID string
	Fence           uint64
	ExpiresAt       time.Time
}

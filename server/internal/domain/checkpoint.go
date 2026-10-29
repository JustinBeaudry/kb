package domain

import "time"

type Checkpoint struct {
	ID, Revision                                       string
	Scope                                              Scope
	Objective                                          string
	Completed, Pending, Questions, EvidenceRevisionIDs []string
	UpdatedAt                                          time.Time
}

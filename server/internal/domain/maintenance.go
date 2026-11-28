package domain

import (
	"errors"
	"time"
)

var ErrMaintenance = errors.New("maintenance admission closed")

type MaintenanceFence struct {
	OperationID string `json:"operation_id"`
	Generation  uint64 `json:"generation"`
}
type OperationPermit struct {
	ID         string `json:"id"`
	InstanceID string `json:"instance_id"`
	Kind       string `json:"kind"`
	Generation uint64 `json:"generation"`
	Unknown    bool   `json:"unknown,omitempty"`
	// Restored marks a permit that was still registered when an earlier
	// supervisor process stopped and that its holder never declared uncertain.
	// Only its holder's exact Finish, or a host operator's settle after proving
	// the holder ended, removes it. It stays unknown for admission until then.
	Restored bool `json:"restored,omitempty"`
}
type MaintenanceState struct {
	Fence                MaintenanceFence `json:"fence"`
	Phase                string           `json:"phase"`
	Active               int              `json:"active"`
	Unknown              int              `json:"unknown"`
	LastSuccessfulBackup time.Time        `json:"last_successful_backup"`
	BackupID             string           `json:"backup_id,omitempty"`
	RecoveryComplete     bool             `json:"recovery_complete"`
}
type HealthReport struct {
	Live, Ready                      bool
	Reasons                          []string
	Maintenance                      MaintenanceState
	JournalSequence, JournalCapacity uint64
	JournalCapacityWarning           bool
}

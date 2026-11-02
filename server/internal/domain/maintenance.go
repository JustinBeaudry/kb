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

// Generates boundary-test samples from the production maintenance JSON schema.
// Test-only helper: never linked into a production service or image.
package main

import (
	"encoding/json"
	"os"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

type status struct {
	HostReady   bool
	Maintenance domain.MaintenanceState
}
type response struct {
	Version int
	Code    string
	Status  status
}

func main() {
	samples := map[string]response{}
	for name, state := range map[string]status{
		"schema_pending": {Maintenance: domain.MaintenanceState{Phase: "bootstrap"}},
		"unknown":        {HostReady: true, Maintenance: domain.MaintenanceState{Phase: "bootstrap", Unknown: 1}},
		"active":         {HostReady: true, Maintenance: domain.MaintenanceState{Phase: "bootstrap", Active: 1}},
		"ready":          {HostReady: true, Maintenance: domain.MaintenanceState{Phase: "bootstrap"}},
	} {
		samples[name] = response{Version: 1, Code: "ok", Status: state}
	}
	if json.NewEncoder(os.Stdout).Encode(samples) != nil {
		os.Exit(1)
	}
}

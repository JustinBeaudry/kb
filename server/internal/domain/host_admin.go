package domain

import (
	"encoding/json"
	"os"
)

// Host IPC contains public options and bounded owner-requested input only.
type HostAdminOptions struct {
	OwnerID      string `json:"owner_id,omitempty"`
	PublicKey    string `json:"public_key,omitempty"`
	ProjectID    string `json:"project_id,omitempty"`
	Role         string `json:"role,omitempty"`
	Capabilities string `json:"capabilities,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
	After        string `json:"after,omitempty"`
	Limit        int    `json:"limit,omitempty"`
	Preview      bool   `json:"preview,omitempty"`
}
type HostAdminRequest struct {
	Version   int              `json:"version"`
	RequestID string           `json:"request_id"`
	Action    string           `json:"action"`
	Options   HostAdminOptions `json:"options"`
	InputJSON json.RawMessage  `json:"input_json,omitempty"`
}
type HostAdminResult struct {
	RequestID   string `json:"request_id"`
	OperationID string `json:"operation_id,omitempty"`
	Code        string `json:"code"`
	Stage       string `json:"stage"`
	ResultJSON  []byte `json:"result_json,omitempty"`
	Completed   bool   `json:"completed"`
}

// HostAdminFiles carries borrowed descriptors, never file paths.
type HostAdminFiles struct{ CredentialSink *os.File }

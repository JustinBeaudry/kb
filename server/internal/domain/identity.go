package domain

import (
	"strings"
	"time"
)

type Role string

const (
	RoleCollector Role = "collector"
	RoleAgent     Role = "agent"
	RoleVerifier  Role = "verifier"
	RoleOwner     Role = "owner"
)

type Principal struct {
	ClientID     string
	OwnerID      string
	Role         Role
	ProjectIDs   []string
	Capabilities []string
	AuthEpoch    uint64
	// Resource, when set, binds the credential to one endpoint: an OAuth
	// access token holds OAuthResourceMCP and authenticates only at /mcp.
	Resource string `json:",omitempty"`
}

type Scope struct{ ProjectID, TaskID, SessionID string }

// ValidateIDs guards required opaque identifiers without assuming UUID format.
// Optional identifiers must be checked only when present at the owning boundary.
func ValidateIDs(ids ...string) error {
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return ErrInvalidInput
		}
	}
	return nil
}

// ClientRecord persists a digest only. Bearers never enter graph storage.
type ClientRecord struct {
	Principal   Principal
	TokenDigest string
	RevokedAt   *time.Time
	// ExpiresAt, when set, is the instant the bearer stops authenticating.
	ExpiresAt *time.Time
}

// OwnerIdentity is the enrolled public signing identity, not an access token.
type OwnerIdentity struct{ ID, PublicKey string }

// OwnerKeyChange replaces the owner's public signing key. The retired key is
// kept so it can never be enrolled again and so the owner can review what it
// signed.
type OwnerKeyChange struct {
	OwnerID    string    `json:"owner_id"`
	RetiredKey string    `json:"retired_key"`
	PublicKey  string    `json:"public_key"`
	ChangedAt  time.Time `json:"changed_at"`
}

// ClientMetadata deliberately has no bearer or token digest fields.
type ClientMetadata struct {
	Principal Principal
	RevokedAt *time.Time
	ExpiresAt *time.Time `json:",omitempty"`
}

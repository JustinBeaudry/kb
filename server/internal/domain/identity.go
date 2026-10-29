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
}

// OwnerIdentity is the enrolled public signing identity, not an access token.
type OwnerIdentity struct{ ID, PublicKey string }

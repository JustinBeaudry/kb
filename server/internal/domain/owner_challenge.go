package domain

import (
	"crypto/sha256"
	"strings"
	"time"
)

// DefaultOwnerChallengeTTL bounds the interactive recovery window.
const DefaultOwnerChallengeTTL = 10 * time.Minute

// OwnerChallenge asks the enrolled owner to sign one exact requested action.
// It carries no bearer and grants nothing until a valid owner signature arrives.
type OwnerChallenge struct {
	ID, Nonce, RequestedAction, ActionDigest string
	ExpiresAt                                time.Time
	ApprovedAt                               *time.Time `json:",omitempty"`
}

// SigningDigest is the 32-byte message the owner signs with BIP-340.
func (c OwnerChallenge) SigningDigest() [32]byte {
	return sha256.Sum256([]byte(strings.Join([]string{"kb-owner-challenge/v1", c.ID, c.Nonce, c.RequestedAction, c.ActionDigest, c.ExpiresAt.UTC().Format(time.RFC3339Nano)}, "\n")))
}

// ActionGrant is a one-use authorization for exactly one action digest. Only
// the token digest is persisted; the token is not a client bearer and carries
// no role, project, or capability.
type ActionGrant struct {
	ID, ChallengeID, RequestedAction, ActionDigest, TokenDigest string
	ExpiresAt                                                   time.Time
	ConsumedAt                                                  *time.Time `json:",omitempty"`
}

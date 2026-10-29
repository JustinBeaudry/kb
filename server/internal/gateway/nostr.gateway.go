// Package gateway contains provider-specific adapters without service policy.
package gateway

import (
	"encoding/hex"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

// NostrIdentity normalizes Buzz's BIP-340 secp256k1 x-only public identities.
// It never accepts or derives private keys and performs no relay operations.
type NostrIdentity struct{}

func (NostrIdentity) NormalizePublicKey(encoded string) (string, error) {
	if len(encoded) != 64 {
		return "", domain.ErrInvalidInput
	}
	raw, err := hex.DecodeString(encoded)
	if err != nil {
		return "", domain.ErrInvalidInput
	}
	// Use the same vetted parser as SoftwareFactory's Buzz gateway. Length checks
	// alone do not establish that the x coordinate is a point on secp256k1.
	publicKey, err := schnorr.ParsePubKey(raw)
	if err != nil {
		return "", domain.ErrInvalidInput
	}
	return hex.EncodeToString(schnorr.SerializePubKey(publicKey)), nil
}

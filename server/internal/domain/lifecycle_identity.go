package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// LifecycleIdentityDigest is an index key for an exact opaque identity tuple.
// Original values remain authoritative; hashing avoids concatenation ambiguity
// and new database index-length restrictions on previously accepted identities.
func LifecycleIdentityDigest(parts ...string) string {
	encoded, _ := json.Marshal(parts)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

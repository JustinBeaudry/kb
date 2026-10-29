package platform

import (
	"encoding/hex"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"os"
)

// LoadRetrievalKey requires an operator-managed 256-bit secret. Rotation makes
// previous cursors stale; the key is never generated or logged by the server.
func LoadRetrievalKey() ([]byte, error) {
	raw := os.Getenv("KB_RETRIEVAL_HMAC_KEY")
	key, e := hex.DecodeString(raw)
	if e != nil || len(key) != 32 {
		return nil, domain.ErrInvalidInput
	}
	return key, nil
}

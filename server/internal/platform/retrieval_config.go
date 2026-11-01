package platform

import (
	"encoding/hex"
	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// LoadRetrievalKey requires an operator-managed 256-bit secret. Rotation makes
// previous cursors stale; the key is never generated or logged by the server.
func LoadRetrievalKey() ([]byte, error) {
	raw, err := secretSetting("KB_RETRIEVAL_HMAC_KEY")
	if err != nil {
		return nil, err
	}
	key, e := hex.DecodeString(raw)
	if e != nil || len(key) != 32 {
		return nil, domain.ErrInvalidInput
	}
	return key, nil
}

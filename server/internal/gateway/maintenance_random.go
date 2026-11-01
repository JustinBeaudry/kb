package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"github.com/JustinBeaudry/kb/server/internal/domain"
)

func randomHex() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", domain.ErrUnavailable
	}
	return hex.EncodeToString(b[:]), nil
}

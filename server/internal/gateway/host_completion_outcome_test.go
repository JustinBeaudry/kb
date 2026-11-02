package gateway

import (
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestCompletedUnavailableRemainsKnownInHostProtocol(t *testing.T) {
	assert.True(t, safeHostCompletion("unavailable"))
	assert.ErrorIs(t, hostCompletionError("unavailable"), domain.ErrUnavailable)
}

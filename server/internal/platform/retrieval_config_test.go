package platform

import (
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestRetrievalKeyFailsClosed(t *testing.T) {
	for _, raw := range []string{"", "secret", strings.Repeat("a", 63), strings.Repeat("g", 64)} {
		t.Setenv("KB_RETRIEVAL_HMAC_KEY", raw)
		_, e := LoadRetrievalKey()
		assert.ErrorIs(t, e, domain.ErrInvalidInput)
	}
	t.Setenv("KB_RETRIEVAL_HMAC_KEY", strings.Repeat("a", 64))
	key, e := LoadRetrievalKey()
	require.NoError(t, e)
	assert.Len(t, key, 32)
}

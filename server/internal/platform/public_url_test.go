package platform

import (
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublicOriginAcceptsOnlyAnHTTPSOrLoopbackOrigin(t *testing.T) {
	for raw, want := range map[string]string{
		"https://kb.example.com":      "https://kb.example.com",
		"https://KB.Example.com/":     "https://kb.example.com",
		"https://kb.example.com:8443": "https://kb.example.com:8443",
		"http://127.0.0.1:8080":       "http://127.0.0.1:8080",
		"http://[::1]:8080":           "http://[::1]:8080",
	} {
		got, err := PublicOrigin(raw)
		require.NoError(t, err, raw)
		assert.Equal(t, want, got, raw)
	}
	for _, raw := range []string{
		"http://kb.example.com", "http://localhost:8080", "ftp://kb.example.com",
		"https://kb.example.com/mcp", "https://kb.example.com?x=1", "https://kb.example.com#f",
		"https://user:pw@kb.example.com", "kb.example.com", "https://", "https:///path",
		"https://kb.example.com/?", `https://kb"x.example.com`,
	} {
		_, err := PublicOrigin(raw)
		assert.ErrorIs(t, err, domain.ErrInvalidInput, raw)
	}
}

func TestLoadPublicURLPrefersSettingThenRailwayDomain(t *testing.T) {
	t.Setenv("KB_PUBLIC_URL", "")
	t.Setenv("RAILWAY_PUBLIC_DOMAIN", "")
	got, err := LoadPublicURL()
	require.NoError(t, err)
	assert.Empty(t, got)

	t.Setenv("RAILWAY_PUBLIC_DOMAIN", "api-production.up.railway.app")
	got, err = LoadPublicURL()
	require.NoError(t, err)
	assert.Equal(t, "https://api-production.up.railway.app", got)

	t.Setenv("KB_PUBLIC_URL", "https://kb.example.com")
	got, err = LoadPublicURL()
	require.NoError(t, err)
	assert.Equal(t, "https://kb.example.com", got)

	t.Setenv("KB_PUBLIC_URL", "http://kb.example.com")
	_, err = LoadPublicURL()
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
}

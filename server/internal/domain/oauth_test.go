package domain

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientMetadataJSONShowsOAuthOnlyForGrants(t *testing.T) {
	plain, err := json.Marshal(ClientMetadata{Principal: Principal{ClientID: "agent"}})
	require.NoError(t, err)
	assert.NotContains(t, string(plain), "OAuth")
	assert.NotContains(t, string(plain), "Resource")
	ends := time.Date(2026, 12, 27, 9, 0, 0, 0, time.UTC)
	granted, err := json.Marshal(ClientMetadata{Principal: Principal{ClientID: "oauth-og_1", Resource: OAuthResourceMCP}, OAuth: &OAuthClientGrant{RedirectHost: "client.example", GrantExpiresAt: ends}})
	require.NoError(t, err)
	assert.Contains(t, string(granted), `"Resource":"mcp"`)
	assert.Contains(t, string(granted), `"OAuth":{"RedirectHost":"client.example","GrantExpiresAt":"2026-12-27T09:00:00Z"}`)
}

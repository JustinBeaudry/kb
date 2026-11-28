package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClientMetadataURLsAreHTTPSWithAPathAndNothingAmbiguous(t *testing.T) {
	for _, ok := range []string{
		"https://app.example/oauth/client.json",
		"https://claude.ai/oauth/mcp-oauth-client-metadata",
		"https://a.b.example/x",
	} {
		assert.True(t, ValidClientMetadataURL(ok), ok)
		assert.True(t, IsClientMetadataID(ok), ok)
	}
	for _, bad := range []string{
		"",
		"http://app.example/client.json",
		"https://app.example",
		"https://app.example/",
		"https://app.example:8443/client.json",
		"https://app.example:443/client.json",
		"https://user@app.example/client.json",
		"https://app.example/client.json?v=1",
		"https://app.example/client.json?",
		"https://app.example/client.json#x",
		"https://app.example/a/../client.json",
		"https://app.example/a/./client.json",
		"https://app.example/a/%2e%2e/client.json",
		"https://App.Example/client.json",
		"https://app.example./client.json",
		"https://localhost/client.json",
		"https://127.0.0.1/client.json",
		"https://[::1]/client.json",
		"https://app.example/client json",
		"https://app.example/client\\json",
		"https://app.example/" + strings.Repeat("a", MaxOAuthClientIDURLLength),
	} {
		assert.False(t, ValidClientMetadataURL(bad), bad)
	}
	assert.False(t, IsClientMetadataID("oc_ABC"), "dynamic registrations are not metadata documents")
}

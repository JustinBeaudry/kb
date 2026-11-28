package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const discoveryOrigin = "https://kb.example.com"

func discoveryAPI(reached *atomic.Int64) http.Handler {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	return WithOAuthDiscovery(GuardPublicAPI(inner, slog.New(slog.DiscardHandler)), discoveryOrigin)
}

func TestProtectedResourceMetadataIsServedBeforeTheGuard(t *testing.T) {
	var reached atomic.Int64
	api := discoveryAPI(&reached)
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource/mcp", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, "*", rec.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	var doc map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	// RFC 9728 §3.3: the resource must be the identifier the address was
	// derived from.
	assert.Equal(t, "https://kb.example.com/mcp", doc["resource"])
	assert.Equal(t, []any{"header"}, doc["bearer_methods_supported"])
	// kb is its own authorization server.
	assert.Equal(t, []any{"https://kb.example.com"}, doc["authorization_servers"])
	assert.Zero(t, reached.Load(), "metadata never reaches admission or authentication")

	// The root address would describe the origin, not /mcp, so it is not
	// served and goes through the guard like any other path.
	root := httptest.NewRecorder()
	api.ServeHTTP(root, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil))
	assert.Equal(t, http.StatusUnauthorized, root.Code)

	head := httptest.NewRecorder()
	api.ServeHTTP(head, httptest.NewRequest(http.MethodHead, MCPResourceMetadataPath, nil))
	assert.Equal(t, http.StatusOK, head.Code)
	assert.Empty(t, head.Body.Bytes())

	post := httptest.NewRecorder()
	api.ServeHTTP(post, httptest.NewRequest(http.MethodPost, MCPResourceMetadataPath, nil))
	assert.Equal(t, http.StatusMethodNotAllowed, post.Code)
	assert.Equal(t, "GET, HEAD, OPTIONS", post.Header().Get("Allow"))
}

func TestMCPUnauthorizedPointsToResourceMetadata(t *testing.T) {
	var reached atomic.Int64
	api := discoveryAPI(&reached)
	for name, header := range map[string]string{"missing": "", "malformed": "Bearer short", "basic": "Basic Zm9vOmJhcg=="} {
		req := httptest.NewRequest(http.MethodPost, RemoteMCPPath, nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)
		require.Equal(t, http.StatusUnauthorized, rec.Code, name)
		assert.Equal(t, `Bearer resource_metadata="https://kb.example.com/.well-known/oauth-protected-resource/mcp"`, rec.Header().Get("WWW-Authenticate"), name)
	}
	assert.Zero(t, reached.Load())

	// REST routes are not the /mcp resource and get no MCP discovery pointer.
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, rec.Header().Get("WWW-Authenticate"))
}

func TestMCPChallengeLeavesOtherResponsesAlone(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Case") {
		case "ok":
			_, _ = w.Write([]byte(`{}`))
		case "forbidden":
			w.WriteHeader(http.StatusForbidden)
		case "own":
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			w.WriteHeader(http.StatusUnauthorized)
		}
	})
	api := WithOAuthDiscovery(inner, discoveryOrigin)
	for c, want := range map[string]string{"ok": "", "forbidden": "", "own": `Bearer error="invalid_token"`} {
		req := httptest.NewRequest(http.MethodPost, RemoteMCPPath, nil)
		req.Header.Set("X-Case", c)
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)
		assert.Equal(t, want, rec.Header().Get("WWW-Authenticate"), c)
	}
}

func TestOAuthDiscoveryIsOffWithoutAPublicOrigin(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	api := WithOAuthDiscovery(inner, "")
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, MCPResourceMetadataPath, nil))
	assert.Equal(t, http.StatusTeapot, rec.Code)
}

func TestAuthorizationServerMetadataNamesOnlyWhatKbIssues(t *testing.T) {
	var reached atomic.Int64
	api := discoveryAPI(&reached)
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, AuthorizationServerMetadataPath, nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "*", rec.Header().Get("Access-Control-Allow-Origin"))
	var doc map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	// RFC 8414 §3.3: the issuer must match the origin the document was fetched from.
	assert.Equal(t, "https://kb.example.com", doc["issuer"])
	assert.Equal(t, "https://kb.example.com/oauth/authorize", doc["authorization_endpoint"])
	assert.Equal(t, "https://kb.example.com/oauth/token", doc["token_endpoint"])
	assert.Equal(t, "https://kb.example.com/oauth/register", doc["registration_endpoint"])
	assert.Equal(t, "https://kb.example.com/oauth/revoke", doc["revocation_endpoint"])
	assert.Equal(t, []any{"code"}, doc["response_types_supported"])
	assert.Equal(t, []any{"authorization_code", "refresh_token"}, doc["grant_types_supported"])
	assert.Equal(t, []any{"S256"}, doc["code_challenge_methods_supported"])
	assert.Equal(t, []any{"none"}, doc["token_endpoint_auth_methods_supported"])
	assert.Equal(t, true, doc["authorization_response_iss_parameter_supported"])
	assert.NotContains(t, doc["scopes_supported"], "owner:adjudicate")
	assert.Zero(t, reached.Load(), "metadata never reaches admission or authentication")
}

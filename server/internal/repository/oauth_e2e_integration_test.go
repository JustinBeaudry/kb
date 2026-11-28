package repository_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/handler"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOAuthScriptedClientRealGraph drives the whole OAuth flow the way a
// hosted MCP connector does, against a real graph and object store: discover,
// register, authorize, owner approval on the host, poll, code exchange, an MCP
// call, refresh rotation, and reuse detection ending the grant.
func TestOAuthScriptedClientRealGraph(t *testing.T) {
	store, jobs, _ := jobsGraph(t)
	objects := captureS3(t)
	warmObjects(t, objects)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	identities := repository.NewIdentityRepository(store)
	identity := service.NewIdentityService(store, identities, gateway.NostrIdentity{})
	_, err := identity.Setup(t.Context(), domain.OwnerIdentity{ID: "owner", PublicKey: "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"}, service.EnrollInput{ClientID: "collector", Role: domain.RoleCollector, ProjectIDs: []string{"demo"}, Capabilities: []string{"artifacts:write"}})
	require.NoError(t, err)
	evidence := repository.NewEvidenceRepository(store)
	retrievalRepo := repository.NewRetrievalRepository(store)
	checkpoints := repository.NewCheckpointRepository(store)
	retrieval, err := service.NewRetrievalService(store, retrievalRepo, checkpoints, evidence, objects, bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	capture := service.NewCaptureService(store, evidence, jobs, objects)
	memory := service.NewMemoryService(store, repository.NewMemoryRepository(store), retrievalRepo, evidence, capture)
	api, err := handler.NewServer(identity, capture, service.NewCheckpointService(store, checkpoints, retrievalRepo), memory, retrieval, service.NewPolicyService(store, evidence, retrievalRepo), service.NewStatusService(store, retrievalRepo), nil, logger, handler.WithRemoteMCP())
	require.NoError(t, err)

	var root http.Handler
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { root.ServeHTTP(w, r) }))
	t.Cleanup(server.Close)
	origin := server.URL
	oauthRepo := repository.NewOAuthRepository(store)
	grants := service.NewOAuthService(store, oauthRepo, time.Now).WithGrants(origin, identities).WithClients(identities, oauthRepo)
	root = handler.WithOAuthDiscovery(handler.WithOAuthEndpoints(api, handler.GuardOAuth(handler.NewOAuthHandler(grants, logger), logger)), origin)

	// The OAuth guard allows a burst of 8 requests and 2 a second after that,
	// and this flow makes more than 8, so the client waits out a 429 as a
	// real connector would.
	client := &http.Client{Transport: waitOutRateLimit{}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	getJSON := func(path string, out any) {
		t.Helper()
		resp, err := client.Get(origin + path)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		require.Equal(t, http.StatusOK, resp.StatusCode, path)
		require.NoError(t, json.NewDecoder(resp.Body).Decode(out))
	}

	// Discovery: the resource names kb as its authorization server.
	var resource struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	getJSON(handler.MCPResourceMetadataPath, &resource)
	assert.Equal(t, origin+"/mcp", resource.Resource)
	require.Equal(t, []string{origin}, resource.AuthorizationServers)
	var metadata struct {
		Issuer                string `json:"issuer"`
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
		RegistrationEndpoint  string `json:"registration_endpoint"`
		RevocationEndpoint    string `json:"revocation_endpoint"`
	}
	getJSON("/.well-known/oauth-authorization-server", &metadata)
	assert.Equal(t, origin, metadata.Issuer)

	// Registration.
	redirect := "http://127.0.0.1:9/callback"
	resp, err := client.Post(metadata.RegistrationEndpoint, "application/json", strings.NewReader(`{"redirect_uris":["`+redirect+`"],"client_name":"Scripted","token_endpoint_auth_method":"none","grant_types":["authorization_code","refresh_token"],"response_types":["code"]}`))
	require.NoError(t, err)
	var registered struct {
		ClientID string `json:"client_id"`
	}
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&registered))
	_ = resp.Body.Close()

	// Authorization request, with PKCE.
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"client_id": {registered.ClientID}, "redirect_uri": {redirect}, "response_type": {"code"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		"state": {"st-1"}, "resource": {origin + "/mcp"}, "scope": {"project:demo status:read retrieval:read"},
	}
	resp, err = client.Get(metadata.AuthorizationEndpoint + "?" + query.Encode())
	require.NoError(t, err)
	page, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(page))
	match := regexp.MustCompile(`data-request="([^"]+)"`).FindSubmatch(page)
	require.NotNil(t, match)
	requestID := string(match[1])
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == handler.OAuthRequestCookie {
			cookie = c
		}
	}
	require.NotNil(t, cookie)
	poll := func() map[string]string {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, origin+handler.OAuthRequestsPath+requestID, nil)
		require.NoError(t, err)
		req.Header.Set("Cookie", cookie.Name+"="+cookie.Value)
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body map[string]string
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		return body
	}
	assert.Equal(t, "pending", poll()["status"])

	// The owner approves on the host, granting less than was asked.
	pending, err := grants.ListRequestsHost(t.Context())
	require.NoError(t, err)
	require.Len(t, pending, 1)
	_, err = grants.ApproveHost(t.Context(), pending[0].ApprovalCode, domain.OAuthScope{Projects: []string{"demo"}, Capabilities: []string{"status:read"}}, 0)
	require.NoError(t, err)
	done := poll()
	require.Equal(t, "done", done["status"])
	back, err := url.Parse(done["redirect"])
	require.NoError(t, err)
	assert.Equal(t, "st-1", back.Query().Get("state"))
	assert.Equal(t, origin, back.Query().Get("iss"))
	code := back.Query().Get("code")
	require.NotEmpty(t, code)

	token := func(form url.Values) (int, map[string]any) {
		t.Helper()
		resp, err := client.PostForm(metadata.TokenEndpoint, form)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		var body map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		return resp.StatusCode, body
	}
	status, tokens := token(url.Values{"grant_type": {"authorization_code"}, "client_id": {registered.ClientID}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}, "resource": {origin + "/mcp"}})
	require.Equal(t, http.StatusOK, status, tokens)
	assert.Equal(t, "Bearer", tokens["token_type"])
	assert.Equal(t, "project:demo status:read", tokens["scope"])
	access := tokens["access_token"].(string)

	call := func(path, bearer string) int {
		t.Helper()
		var req *http.Request
		if path == "/mcp" {
			req, err = http.NewRequest(http.MethodPost, origin+path, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"memory_status","arguments":{}}}`))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
		} else {
			req, err = http.NewRequest(http.MethodGet, origin+path, nil)
			require.NoError(t, err)
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		resp, err := client.Do(req)
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	assert.Equal(t, http.StatusOK, call("/mcp", access), "the access token works at /mcp")
	assert.Equal(t, http.StatusUnauthorized, call("/v1/status", access), "and nowhere else")

	status, body := token(url.Values{"grant_type": {"authorization_code"}, "client_id": {registered.ClientID}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}})
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "invalid_grant", body["error"], "a code works once")
	assert.Equal(t, http.StatusUnauthorized, call("/mcp", access), "and its second use revoked the grant")

	// A fresh grant, to exercise refresh rotation and reuse detection.
	resp, err = client.Get(metadata.AuthorizationEndpoint + "?" + query.Encode())
	require.NoError(t, err)
	page, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	requestID = string(regexp.MustCompile(`data-request="([^"]+)"`).FindSubmatch(page)[1])
	for _, c := range resp.Cookies() {
		if c.Name == handler.OAuthRequestCookie {
			cookie = c
		}
	}
	pending, err = grants.ListRequestsHost(t.Context())
	require.NoError(t, err)
	require.Len(t, pending, 1)
	_, err = grants.ApproveHost(t.Context(), pending[0].ApprovalCode, domain.OAuthScope{Projects: []string{"demo"}, Capabilities: []string{"status:read"}}, 0)
	require.NoError(t, err)
	back, err = url.Parse(poll()["redirect"])
	require.NoError(t, err)
	status, tokens = token(url.Values{"grant_type": {"authorization_code"}, "client_id": {registered.ClientID}, "code": {back.Query().Get("code")}, "redirect_uri": {redirect}, "code_verifier": {verifier}})
	require.Equal(t, http.StatusOK, status, tokens)
	access, refresh := tokens["access_token"].(string), tokens["refresh_token"].(string)

	status, rotated := token(url.Values{"grant_type": {"refresh_token"}, "client_id": {registered.ClientID}, "refresh_token": {refresh}})
	require.Equal(t, http.StatusOK, status, rotated)
	newAccess := rotated["access_token"].(string)
	assert.NotEqual(t, refresh, rotated["refresh_token"])
	assert.Equal(t, http.StatusUnauthorized, call("/mcp", access), "refreshing replaces the access token")
	assert.Equal(t, http.StatusOK, call("/mcp", newAccess))

	status, body = token(url.Values{"grant_type": {"refresh_token"}, "client_id": {registered.ClientID}, "refresh_token": {refresh}})
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "invalid_grant", body["error"])
	assert.Equal(t, http.StatusUnauthorized, call("/mcp", newAccess), "reusing a refresh token ends the grant")
	status, _ = token(url.Values{"grant_type": {"refresh_token"}, "client_id": {registered.ClientID}, "refresh_token": {rotated["refresh_token"].(string)}})
	assert.Equal(t, http.StatusBadRequest, status, "including its newest refresh token")
}

// waitOutRateLimit retries a request the OAuth guard refused with 429.
type waitOutRateLimit struct{}

func (waitOutRateLimit) RoundTrip(req *http.Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil || resp.StatusCode != http.StatusTooManyRequests || attempt == 10 {
			return resp, err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		time.Sleep(600 * time.Millisecond)
		if req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			req = req.Clone(req.Context())
			req.Body = body
		}
	}
}

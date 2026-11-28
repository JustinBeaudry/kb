package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type registrarFake struct {
	got []service.OAuthRegistrationInput
	reg domain.OAuthRegistration
	err error
}

func (f *registrarFake) Register(_ context.Context, in service.OAuthRegistrationInput) (domain.OAuthRegistration, error) {
	f.got = append(f.got, in)
	return f.reg, f.err
}

func registerRequest(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, OAuthRegisterPath, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json; charset=utf-8")
	return r
}

func TestOAuthRegisterReturnsClientMetadata(t *testing.T) {
	at := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	fake := &registrarFake{reg: domain.OAuthRegistration{ID: "oc_ABC", RedirectURIs: []string{"https://claude.ai/api/mcp/auth_callback"}, ClientName: "Claude", CreatedAt: at, ExpiresAt: at.Add(24 * time.Hour)}}
	var logs bytes.Buffer
	h := NewOAuthHandler(fake, slog.New(slog.NewJSONHandler(&logs, nil)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, registerRequest(`{"redirect_uris":["https://claude.ai/api/mcp/auth_callback"],"client_name":"Claude","grant_types":["authorization_code","refresh_token"],"response_types":["code"],"token_endpoint_auth_method":"none","scope":"ignored","logo_uri":"https://evil.example/x.png","software_statement":"ignored"}`))

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
	assert.NotEmpty(t, w.Header().Get("X-Request-Id"))
	assert.JSONEq(t, `{"client_id":"oc_ABC","client_id_issued_at":1795770000,"redirect_uris":["https://claude.ai/api/mcp/auth_callback"],"client_name":"Claude","token_endpoint_auth_method":"none","grant_types":["authorization_code","refresh_token"],"response_types":["code"]}`, w.Body.String())
	require.Len(t, fake.got, 1)
	assert.Equal(t, service.OAuthRegistrationInput{RedirectURIs: []string{"https://claude.ai/api/mcp/auth_callback"}, ClientName: "Claude", TokenEndpointAuthMethod: "none", GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"}}, fake.got[0], "unknown metadata is ignored, never passed on")
	assert.NotContains(t, logs.String(), "claude.ai", "logs hold no client metadata")
	assert.NotContains(t, logs.String(), "oc_ABC")
	assert.Contains(t, logs.String(), `"status":201`)
}

func TestOAuthRegisterRefusals(t *testing.T) {
	cases := map[string]struct {
		req    func() *http.Request
		err    error
		status int
		code   string
	}{
		"bad redirect": {func() *http.Request { return registerRequest(`{"redirect_uris":["http://a.example"]}`) }, domain.ErrInvalidRedirectURI, 400, "invalid_redirect_uri"},
		"bad metadata": {func() *http.Request { return registerRequest(`{"redirect_uris":["https://a.example"]}`) }, domain.ErrInvalidClientMetadata, 400, "invalid_client_metadata"},
		"cap reached":  {func() *http.Request { return registerRequest(`{"redirect_uris":["https://a.example"]}`) }, domain.ErrBudget, 429, "temporarily_unavailable"},
		"graph down":   {func() *http.Request { return registerRequest(`{"redirect_uris":["https://a.example"]}`) }, domain.ErrUnavailable, 503, "temporarily_unavailable"},
		"not json":     {func() *http.Request { return registerRequest(`redirect_uris=x`) }, nil, 400, "invalid_client_metadata"},
		"wrong type":   {func() *http.Request { return registerRequest(`{"redirect_uris":"https://a.example"}`) }, nil, 400, "invalid_client_metadata"},
		"two objects":  {func() *http.Request { return registerRequest(`{"redirect_uris":["https://a.example"]}{}`) }, nil, 400, "invalid_client_metadata"},
		"array":        {func() *http.Request { return registerRequest(`[]`) }, nil, 400, "invalid_client_metadata"},
		"too large": {func() *http.Request {
			return registerRequest(`{"client_name":"` + strings.Repeat("a", maxOAuthRegisterBody) + `"}`)
		}, nil, 413, "invalid_client_metadata"},
		"form encoded": {func() *http.Request {
			r := registerRequest(`{}`)
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return r
		}, nil, 415, "invalid_client_metadata"},
		"get":           {func() *http.Request { return httptest.NewRequest(http.MethodGet, OAuthRegisterPath, nil) }, nil, 405, "invalid_request"},
		"unknown route": {func() *http.Request { return httptest.NewRequest(http.MethodPost, "/oauth/token", nil) }, nil, 404, "not_found"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fake := &registrarFake{err: tc.err}
			w := httptest.NewRecorder()
			NewOAuthHandler(fake, nil).ServeHTTP(w, tc.req())
			require.Equal(t, tc.status, w.Code, w.Body.String())
			var body oauthErrorBody
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			assert.Equal(t, tc.code, body.Error)
			assert.NotEmpty(t, body.ErrorDescription)
			assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
			if tc.err == nil {
				assert.Empty(t, fake.got, "nothing reaches the service")
			}
		})
	}
}

func TestOAuthRegisterAnswersPreflight(t *testing.T) {
	w := httptest.NewRecorder()
	NewOAuthHandler(&registrarFake{}, nil).ServeHTTP(w, httptest.NewRequest(http.MethodOptions, OAuthRegisterPath, nil))
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "POST", w.Header().Get("Access-Control-Allow-Methods"))
}

func TestOAuthEndpointsRouteOnlyTheOAuthPrefix(t *testing.T) {
	mark := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("X-Went", name) })
	}
	h := WithOAuthEndpoints(mark("api"), mark("oauth"))
	for path, want := range map[string]string{"/oauth/register": "oauth", "/oauth/": "oauth", "/oauth": "api", "/oauthx": "api", "/mcp": "api", "/v1/exports": "api"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))
		assert.Equal(t, want, w.Header().Get("X-Went"), path)
	}
	assert.NotNil(t, WithOAuthEndpoints(mark("api"), nil))
}

func TestOAuthGuardBoundsRateAndConcurrency(t *testing.T) {
	now := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	served := 0
	g := newOAuthGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { served++; w.WriteHeader(201) }), nil, clock)
	hit := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		g.ServeHTTP(w, registerRequest(`{}`))
		return w
	}
	for range oauthRequestBurst {
		require.Equal(t, 201, hit().Code)
	}
	w := hit()
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Equal(t, "1", w.Header().Get("Retry-After"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"), "browser clients can read the refusal")
	assert.Contains(t, w.Body.String(), "temporarily_unavailable")
	assert.Equal(t, oauthRequestBurst, served)

	mu.Lock()
	now = now.Add(time.Second)
	mu.Unlock()
	for range oauthRequestsPerSecond {
		require.Equal(t, 201, hit().Code)
	}
	assert.Equal(t, http.StatusTooManyRequests, hit().Code)

	// Requests in flight count against the concurrency bound.
	mu.Lock()
	now = now.Add(time.Hour)
	mu.Unlock()
	release := make(chan struct{})
	started := make(chan struct{}, oauthConcurrentRequests)
	blocking := newOAuthGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-release
	}), nil, clock)
	var wg sync.WaitGroup
	for range oauthConcurrentRequests {
		wg.Go(func() { blocking.ServeHTTP(httptest.NewRecorder(), registerRequest(`{}`)) })
	}
	for range oauthConcurrentRequests {
		<-started
	}
	w = httptest.NewRecorder()
	blocking.ServeHTTP(w, registerRequest(`{}`))
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	close(release)
	wg.Wait()
}

func TestOAuthGuardAdmitsEachRequestAndAnswersMaintenanceInOAuthTerms(t *testing.T) {
	a := &guardAdmission{}
	served := 0
	h := GuardOAuth(platform.AdmittedHandlerRefusing(a, "api", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served++
		assert.True(t, domain.OperationSettled(r.Context()), "the request runs inside effect accounting")
		w.WriteHeader(201)
	}), RefuseOAuthMaintenance(nil)), nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, registerRequest(`{}`))
	require.Equal(t, 201, w.Code)
	assert.Equal(t, int64(1), a.admitted.Load())
	assert.Equal(t, int64(1), a.finished.Load())

	a.denied = true
	w = httptest.NewRecorder()
	h.ServeHTTP(w, registerRequest(`{}`))
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	var body oauthErrorBody
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "temporarily_unavailable", body.Error)
	assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.NotEmpty(t, w.Header().Get("Retry-After"))
	assert.Equal(t, 1, served, "nothing runs outside admission")
}

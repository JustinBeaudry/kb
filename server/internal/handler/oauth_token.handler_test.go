package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type tokensFake struct {
	grantsFake
	got       service.OAuthTokenInput
	tokens    service.OAuthTokens
	err       error
	revoked   []string
	revokeErr error
}

func (f *tokensFake) Token(_ context.Context, in service.OAuthTokenInput) (service.OAuthTokens, error) {
	f.got = in
	return f.tokens, f.err
}

func (f *tokensFake) Revoke(_ context.Context, clientID, token string) error {
	f.revoked = append(f.revoked, clientID+" "+token)
	return f.revokeErr
}

func newTokensFake() *tokensFake {
	return &tokensFake{grantsFake: *newGrantsFake(), tokens: service.OAuthTokens{AccessToken: strings.Repeat("a", 64), ExpiresIn: 3600, RefreshToken: strings.Repeat("r", 64), Scope: "project:demo retrieval:read"}}
}

func formRequest(path string, form url.Values) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

func TestOAuthTokenEndpointIssuesUncacheableTokens(t *testing.T) {
	f := newTokensFake()
	w := httptest.NewRecorder()
	NewOAuthHandler(f, nil).ServeHTTP(w, formRequest(OAuthTokenPath, url.Values{"grant_type": {"authorization_code"}, "client_id": {"oc_1"}, "code": {"c"}, "redirect_uri": {"https://app.example/cb"}, "code_verifier": {"v"}, "resource": {"https://kb.example/mcp"}}))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, service.OAuthTokenInput{GrantType: "authorization_code", ClientID: "oc_1", Code: "c", RedirectURI: "https://app.example/cb", CodeVerifier: "v", Resource: "https://kb.example/mcp"}, f.got)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, "no-cache", w.Header().Get("Pragma"))
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, map[string]any{"access_token": strings.Repeat("a", 64), "token_type": "Bearer", "expires_in": float64(3600), "refresh_token": strings.Repeat("r", 64), "scope": "project:demo retrieval:read"}, body)

	f.err = &service.OAuthTokenError{Code: "invalid_grant", Description: "no"}
	w = httptest.NewRecorder()
	NewOAuthHandler(f, nil).ServeHTTP(w, formRequest(OAuthTokenPath, url.Values{"grant_type": {"refresh_token"}}))
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), `"error":"invalid_grant"`)

	f.err = domain.ErrUnavailable
	w = httptest.NewRecorder()
	NewOAuthHandler(f, nil).ServeHTTP(w, formRequest(OAuthTokenPath, url.Values{"grant_type": {"refresh_token"}}))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestOAuthTokenEndpointRefusesWhatPublicClientsMustNotSend(t *testing.T) {
	cases := map[string]struct {
		req    func() *http.Request
		status int
	}{
		"json body": {func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, OAuthTokenPath, strings.NewReader(`{}`))
			r.Header.Set("Content-Type", "application/json")
			return r
		}, http.StatusUnsupportedMediaType},
		"parameters in the URL": {func() *http.Request {
			return formRequest(OAuthTokenPath+"?code=c", url.Values{"grant_type": {"authorization_code"}})
		}, http.StatusBadRequest},
		"basic client auth": {func() *http.Request {
			r := formRequest(OAuthTokenPath, url.Values{"grant_type": {"authorization_code"}})
			r.SetBasicAuth("oc_1", "secret")
			return r
		}, http.StatusUnauthorized},
		"client secret": {func() *http.Request {
			return formRequest(OAuthTokenPath, url.Values{"grant_type": {"authorization_code"}, "client_secret": {"s"}})
		}, http.StatusUnauthorized},
		"repeated parameter": {func() *http.Request {
			return formRequest(OAuthTokenPath, url.Values{"grant_type": {"authorization_code", "refresh_token"}})
		}, http.StatusBadRequest},
		"oversized body": {func() *http.Request {
			return formRequest(OAuthTokenPath, url.Values{"code": {strings.Repeat("c", maxOAuthFormBody)}})
		}, http.StatusRequestEntityTooLarge},
		"GET": {func() *http.Request { return httptest.NewRequest(http.MethodGet, OAuthTokenPath, nil) }, http.StatusMethodNotAllowed},
	}
	for name, tc := range cases {
		f := newTokensFake()
		w := httptest.NewRecorder()
		NewOAuthHandler(f, nil).ServeHTTP(w, tc.req())
		assert.Equal(t, tc.status, w.Code, name)
		assert.Empty(t, f.got.GrantType, name)
		assert.NotContains(t, w.Body.String(), "access_token", name)
	}
}

func TestOAuthRevokeEndpointAnswersOKForAnyToken(t *testing.T) {
	f := newTokensFake()
	w := httptest.NewRecorder()
	NewOAuthHandler(f, nil).ServeHTTP(w, formRequest(OAuthRevokePath, url.Values{"client_id": {"oc_1"}, "token": {"t"}, "token_type_hint": {"refresh_token"}}))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, []string{"oc_1 t"}, f.revoked)
	assert.Empty(t, w.Body.String())

	f.revokeErr = &service.OAuthTokenError{Code: "invalid_request", Description: "token is required"}
	w = httptest.NewRecorder()
	NewOAuthHandler(f, nil).ServeHTTP(w, formRequest(OAuthRevokePath, url.Values{"client_id": {"oc_1"}}))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestOAuthTokenEndpointsStayClosedWithoutAnIssuer(t *testing.T) {
	f := newTokensFake()
	f.issuer = ""
	for _, path := range []string{OAuthTokenPath, OAuthRevokePath} {
		w := httptest.NewRecorder()
		NewOAuthHandler(f, nil).ServeHTTP(w, formRequest(path, url.Values{"grant_type": {"authorization_code"}, "token": {"t"}, "client_id": {"c"}}))
		assert.Equal(t, http.StatusNotFound, w.Code, path)
	}
	assert.Empty(t, f.got.GrantType)
	assert.Empty(t, f.revoked)
}

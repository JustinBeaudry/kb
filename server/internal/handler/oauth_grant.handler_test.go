package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type grantsFake struct {
	registrarFake
	issuer    string
	auth      service.OAuthAuthorization
	authErr   error
	got       service.OAuthAuthorizeInput
	poll      service.OAuthPoll
	pollErr   error
	pollID    string
	pollValue string
}

func (f *grantsFake) Issuer() string { return f.issuer }

func (f *grantsFake) Authorize(_ context.Context, in service.OAuthAuthorizeInput) (service.OAuthAuthorization, error) {
	f.got = in
	return f.auth, f.authErr
}

func (f *grantsFake) Poll(_ context.Context, id, cookie string) (service.OAuthPoll, error) {
	f.pollID, f.pollValue = id, cookie
	return f.poll, f.pollErr
}

func newGrantsFake() *grantsFake {
	return &grantsFake{
		issuer: "https://kb.example",
		auth: service.OAuthAuthorization{Cookie: "browser-cookie", Request: domain.OAuthRequest{
			ID: "or_ABC", ApprovalCode: "ABCDEFGH", ClientName: "<script>alert(1)</script>", RedirectURI: "https://app.example/cb",
			Requested: domain.OAuthScope{Projects: []string{"demo"}, Capabilities: []string{"retrieval:read"}},
		}},
	}
}

const authorizeQuery = "?client_id=oc_1&redirect_uri=https%3A%2F%2Fapp.example%2Fcb%3Fkeep%3D1&response_type=code&code_challenge=c&code_challenge_method=S256&state=s1&resource=https%3A%2F%2Fkb.example%2Fmcp&scope=project%3Ademo"

func TestOAuthAuthorizeShowsALockedDownApprovalPage(t *testing.T) {
	f := newGrantsFake()
	w := httptest.NewRecorder()
	NewOAuthHandler(f, nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, OAuthAuthorizePath+authorizeQuery, nil))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, service.OAuthAuthorizeInput{ClientID: "oc_1", RedirectURI: "https://app.example/cb?keep=1", ResponseType: "code", CodeChallenge: "c", CodeChallengeMethod: "S256", State: "s1", Resource: "https://kb.example/mcp", Scope: "project:demo"}, f.got)

	h := w.Header()
	assert.Contains(t, h.Get("Content-Security-Policy"), "frame-ancestors 'none'")
	assert.Contains(t, h.Get("Content-Security-Policy"), "script-src 'self'")
	assert.NotContains(t, h.Get("Content-Security-Policy"), "unsafe-inline")
	assert.Equal(t, "DENY", h.Get("X-Frame-Options"))
	assert.Equal(t, "no-referrer", h.Get("Referrer-Policy"))
	assert.Equal(t, "no-store", h.Get("Cache-Control"))
	assert.Empty(t, h.Get("Access-Control-Allow-Origin"), "the page is never readable cross-origin")

	cookies := w.Result().Cookies()
	require.Len(t, cookies, 1)
	c := cookies[0]
	assert.Equal(t, OAuthRequestCookie, c.Name)
	assert.Equal(t, "browser-cookie", c.Value)
	assert.Equal(t, OAuthRequestsPath+"or_ABC", c.Path, "the cookie is sent only to this request's poll")
	assert.True(t, c.Secure)
	assert.True(t, c.HttpOnly)
	assert.Equal(t, http.SameSiteStrictMode, c.SameSite)
	assert.Greater(t, c.MaxAge, int(domain.OAuthRequestTTL.Seconds()), "the cookie outlives the request, so the poll after expiry still reaches kb")
	assert.LessOrEqual(t, c.MaxAge, int((domain.OAuthRequestTTL + 5*time.Minute).Seconds()))

	body := w.Body.String()
	assert.Contains(t, body, "app.example", "the page names where the browser will be sent")
	assert.Contains(t, body, "ABCD-EFGH")
	assert.Contains(t, body, "not verified")
	assert.NotContains(t, body, "<script>alert", "the client's name is escaped")
	assert.NotContains(t, body, "<script>\n", "no inline script")
	assert.Contains(t, body, `data-request="or_ABC"`)
}

func TestOAuthApprovalPageNamesTheSiteThatPublishedAMetadataClient(t *testing.T) {
	f := newGrantsFake()
	f.auth.Request.ClientID = "https://publisher.example/oauth/client.json"
	f.auth.Request.ClientName = "Published App"
	w := httptest.NewRecorder()
	NewOAuthHandler(f, nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, OAuthAuthorizePath+authorizeQuery, nil))
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "publisher.example")
	assert.Contains(t, body, "Published App")
	assert.NotContains(t, body, "not verified", "the publisher was read from the client ID itself")
}

func TestOAuthAuthorizeNeverRedirectsBeforeTheRedirectURIIsKnown(t *testing.T) {
	for _, err := range []error{domain.ErrUnauthorized, domain.ErrInvalidInput} {
		f := newGrantsFake()
		f.authErr = err
		w := httptest.NewRecorder()
		NewOAuthHandler(f, nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, OAuthAuthorizePath+authorizeQuery, nil))
		assert.Equal(t, http.StatusBadRequest, w.Code, err)
		assert.Empty(t, w.Header().Get("Location"), err)
		assert.Empty(t, w.Result().Cookies(), err)
		assert.Contains(t, w.Body.String(), "isn't valid", err)
	}
	f := newGrantsFake()
	f.authErr = domain.ErrUnavailable
	w := httptest.NewRecorder()
	NewOAuthHandler(f, nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, OAuthAuthorizePath+authorizeQuery, nil))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Empty(t, w.Header().Get("Location"))
}

func TestOAuthAuthorizeRedirectsClientErrorsWithStateAndIssuer(t *testing.T) {
	f := newGrantsFake()
	f.authErr = &service.OAuthRedirectError{Code: "invalid_scope", Description: "Unknown scope."}
	w := httptest.NewRecorder()
	NewOAuthHandler(f, nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, OAuthAuthorizePath+authorizeQuery, nil))
	require.Equal(t, http.StatusFound, w.Code)
	target, err := url.Parse(w.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "app.example", target.Host)
	assert.Equal(t, "/cb", target.Path)
	q := target.Query()
	assert.Equal(t, "invalid_scope", q.Get("error"))
	assert.Equal(t, "s1", q.Get("state"))
	assert.Equal(t, "https://kb.example", q.Get("iss"))
	assert.Equal(t, "1", q.Get("keep"), "the registered URI keeps its own query")
	assert.Empty(t, w.Result().Cookies())

	f.got = service.OAuthAuthorizeInput{}
	w = httptest.NewRecorder()
	bad := strings.Replace(authorizeQuery, "state=s1", "state=%0A", 1)
	NewOAuthHandler(f, nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, OAuthAuthorizePath+bad, nil))
	require.Equal(t, http.StatusFound, w.Code)
	target, err = url.Parse(w.Header().Get("Location"))
	require.NoError(t, err)
	assert.False(t, target.Query().Has("state"), "an invalid state is never echoed")
}

func TestOAuthPollAnswersOnlyTheBrowserThatStartedTheRequest(t *testing.T) {
	poll := func(f *grantsFake, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		NewOAuthHandler(f, nil).ServeHTTP(w, r)
		return w
	}
	cookie := &http.Cookie{Name: OAuthRequestCookie, Value: "browser-cookie"}

	f := newGrantsFake()
	f.poll = service.OAuthPoll{Pending: true}
	w := poll(f, OAuthRequestsPath+"or_ABC", nil)
	assert.Equal(t, http.StatusNotFound, w.Code, "no cookie learns nothing")
	assert.Empty(t, f.pollID, "and costs no graph work")

	w = poll(f, OAuthRequestsPath+"or_ABC", cookie)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "or_ABC", f.pollID)
	assert.Equal(t, "browser-cookie", f.pollValue)
	assert.JSONEq(t, `{"status":"pending"}`, w.Body.String())
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))

	f.poll = service.OAuthPoll{Redirect: "https://app.example/cb?code=x&state=s1"}
	w = poll(f, OAuthRequestsPath+"or_ABC", cookie)
	require.Equal(t, http.StatusOK, w.Code)
	var body map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, map[string]string{"status": "done", "redirect": "https://app.example/cb?code=x&state=s1"}, body)

	f.pollErr = domain.ErrForbidden
	w = poll(f, OAuthRequestsPath+"or_ABC", cookie)
	assert.Equal(t, http.StatusNotFound, w.Code, "a wrong cookie or a released request is indistinguishable from none")
	assert.NotContains(t, w.Body.String(), "redirect")

	for _, path := range []string{OAuthRequestsPath, OAuthRequestsPath + "a/b", OAuthRequestsPath + strings.Repeat("a", 65)} {
		f := newGrantsFake()
		w := poll(f, path, cookie)
		assert.Equal(t, http.StatusNotFound, w.Code, path)
		assert.Empty(t, f.pollID, path)
	}

	r := httptest.NewRequest(http.MethodPost, OAuthRequestsPath+"or_ABC", nil)
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	NewOAuthHandler(newGrantsFake(), nil).ServeHTTP(w, r)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestOAuthApprovalPageAssetsAreServedAndNothingElse(t *testing.T) {
	f := newGrantsFake()
	for path, kind := range map[string]string{"/oauth/static/approve.js": "text/javascript", "/oauth/static/approve.css": "text/css"} {
		w := httptest.NewRecorder()
		NewOAuthHandler(f, nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, http.StatusOK, w.Code, path)
		assert.True(t, strings.HasPrefix(w.Header().Get("Content-Type"), kind), path)
		assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"), path)
	}
	for _, path := range []string{"/oauth/static/", "/oauth/static/../oauth.handler.go", "/oauth/static/other.js"} {
		w := httptest.NewRecorder()
		NewOAuthHandler(f, nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, http.StatusNotFound, w.Code, path)
	}
}

func TestOAuthGrantEndpointsStayClosedWithoutAnIssuer(t *testing.T) {
	f := newGrantsFake()
	f.issuer = ""
	for _, path := range []string{OAuthAuthorizePath + authorizeQuery, OAuthRequestsPath + "or_ABC", "/oauth/static/approve.js"} {
		w := httptest.NewRecorder()
		NewOAuthHandler(f, nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, http.StatusNotFound, w.Code, path)
	}
	assert.Empty(t, f.got.ClientID)
}

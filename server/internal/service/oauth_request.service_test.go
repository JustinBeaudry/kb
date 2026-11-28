package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testIssuer = "https://kb.example"

type ownerFake struct{ owner OwnerIdentity }

func (o ownerFake) Owner(context.Context, Tx) (OwnerIdentity, error) { return o.owner, nil }

type oauthFlow struct {
	t    *testing.T
	now  time.Time
	repo *oauthRepoFake
	svc  *OAuthService
}

func newOAuthFlow(t *testing.T) *oauthFlow {
	f := &oauthFlow{t: t, now: time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC), repo: &oauthRepoFake{}}
	f.svc = NewOAuthService(directTx{}, f.repo, func() time.Time { return f.now }).WithGrants(testIssuer, ownerFake{OwnerIdentity{ID: "owner", PublicKey: strings.Repeat("a", 64)}})
	f.repo.regs = []domain.OAuthRegistration{{ID: "oc_client", RedirectURIs: []string{"https://client.example/cb?tenant=1"}, ClientName: "Client", CreatedAt: f.now, ExpiresAt: f.now.Add(24 * time.Hour)}}
	return f
}

func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (f *oauthFlow) input() OAuthAuthorizeInput {
	return OAuthAuthorizeInput{ClientID: "oc_client", RedirectURI: "https://client.example/cb?tenant=1", ResponseType: "code", CodeChallenge: challenge("verifier"), CodeChallengeMethod: "S256", State: "st-1", Resource: testIssuer + "/mcp", Scope: "project:demo project:notes retrieval:read candidate:write"}
}

func (f *oauthFlow) authorize() OAuthAuthorization {
	a, err := f.svc.Authorize(f.t.Context(), f.input())
	require.NoError(f.t, err)
	return a
}

func TestOAuthAuthorizeStoresRequestBoundToBrowser(t *testing.T) {
	f := newOAuthFlow(t)
	a := f.authorize()
	req := a.Request
	assert.True(t, strings.HasPrefix(req.ID, "or_"))
	_, ok := domain.NormalizeApprovalCode(req.ApprovalCode)
	assert.True(t, ok, req.ApprovalCode)
	assert.Equal(t, domain.OAuthScope{Projects: []string{"demo", "notes"}, Capabilities: []string{"retrieval:read", "candidate:write"}}, req.Requested)
	assert.Equal(t, f.now.Add(domain.OAuthRequestTTL), req.ExpiresAt)
	stored := f.repo.requests[req.ID]
	assert.NotEmpty(t, a.Cookie)
	assert.NotContains(t, stored.CookieDigest, a.Cookie, "only the cookie digest is stored")
	assert.Equal(t, oauthDigest(a.Cookie), stored.CookieDigest)

	poll, err := f.svc.Poll(t.Context(), req.ID, a.Cookie)
	require.NoError(t, err)
	assert.True(t, poll.Pending)
	for _, cookie := range []string{"", "wrong", strings.ToUpper(a.Cookie) + "x"} {
		_, err := f.svc.Poll(t.Context(), req.ID, cookie)
		assert.ErrorIs(t, err, domain.ErrForbidden, "a poll without the request cookie learns nothing")
	}
	_, err = f.svc.Poll(t.Context(), "or_unknown", a.Cookie)
	assert.ErrorIs(t, err, domain.ErrForbidden)
}

func TestOAuthAuthorizeNeverRedirectsToAnUnregisteredURI(t *testing.T) {
	f := newOAuthFlow(t)
	for name, mutate := range map[string]func(*OAuthAuthorizeInput){
		"unknown client":     func(in *OAuthAuthorizeInput) { in.ClientID = "oc_other" },
		"no redirect":        func(in *OAuthAuthorizeInput) { in.RedirectURI = "" },
		"other redirect":     func(in *OAuthAuthorizeInput) { in.RedirectURI = "https://evil.example/cb" },
		"prefix of redirect": func(in *OAuthAuthorizeInput) { in.RedirectURI = "https://client.example/cb" },
		"bad everything too": func(in *OAuthAuthorizeInput) { in.RedirectURI = "https://evil.example/cb"; in.ResponseType = "token" },
	} {
		in := f.input()
		mutate(&in)
		_, err := f.svc.Authorize(t.Context(), in)
		assert.ErrorIs(t, err, domain.ErrUnauthorized, name)
	}
	f.now = f.now.Add(25 * time.Hour)
	_, err := f.svc.Authorize(t.Context(), f.input())
	assert.ErrorIs(t, err, domain.ErrUnauthorized, "an expired registration is unknown")
	assert.Empty(t, f.repo.requests)
}

func TestOAuthAuthorizeRedirectsProtocolErrors(t *testing.T) {
	f := newOAuthFlow(t)
	for name, tc := range map[string]struct {
		mutate func(*OAuthAuthorizeInput)
		code   string
	}{
		"token response":     {func(in *OAuthAuthorizeInput) { in.ResponseType = "token" }, "unsupported_response_type"},
		"no state":           {func(in *OAuthAuthorizeInput) { in.State = "" }, "invalid_request"},
		"state with newline": {func(in *OAuthAuthorizeInput) { in.State = "a\nb" }, "invalid_request"},
		"long state":         {func(in *OAuthAuthorizeInput) { in.State = strings.Repeat("s", 513) }, "invalid_request"},
		"plain pkce":         {func(in *OAuthAuthorizeInput) { in.CodeChallengeMethod = "plain" }, "invalid_request"},
		"no pkce":            {func(in *OAuthAuthorizeInput) { in.CodeChallenge = "" }, "invalid_request"},
		"short challenge":    {func(in *OAuthAuthorizeInput) { in.CodeChallenge = "abc" }, "invalid_request"},
		"no resource":        {func(in *OAuthAuthorizeInput) { in.Resource = "" }, "invalid_target"},
		"other resource":     {func(in *OAuthAuthorizeInput) { in.Resource = "https://other.example/mcp" }, "invalid_target"},
		"resource sub path":  {func(in *OAuthAuthorizeInput) { in.Resource = testIssuer + "/mcp/x" }, "invalid_target"},
		"owner capability":   {func(in *OAuthAuthorizeInput) { in.Scope = "project:demo verification:write" }, "invalid_scope"},
		"unknown scope":      {func(in *OAuthAuthorizeInput) { in.Scope = "admin" }, "invalid_scope"},
		"bad project":        {func(in *OAuthAuthorizeInput) { in.Scope = "project:<script>" }, "invalid_scope"},
	} {
		in := f.input()
		tc.mutate(&in)
		_, err := f.svc.Authorize(t.Context(), in)
		var redirect *OAuthRedirectError
		require.ErrorAs(t, err, &redirect, name)
		assert.Equal(t, tc.code, redirect.Code, name)
	}
	assert.Empty(t, f.repo.requests)
}

func TestOAuthAuthorizeCapsPendingRequests(t *testing.T) {
	f := newOAuthFlow(t)
	for range domain.MaxPendingOAuthRequests {
		f.authorize()
	}
	_, err := f.svc.Authorize(t.Context(), f.input())
	var redirect *OAuthRedirectError
	require.ErrorAs(t, err, &redirect)
	assert.Equal(t, "temporarily_unavailable", redirect.Code)

	f2 := newOAuthFlow(t)
	f2.repo.clashes = 2
	f2.authorize()
	f2.repo.clashes = 5
	_, err = f2.svc.Authorize(t.Context(), f2.input())
	assert.ErrorIs(t, err, domain.ErrUnavailable, "approval code clashes are retried a few times only")
}

func TestOAuthApprovedRequestReleasesOneCodeToItsBrowser(t *testing.T) {
	f := newOAuthFlow(t)
	a := f.authorize()
	granted, err := f.svc.ApproveHost(t.Context(), strings.ToLower(a.Request.ApprovalCode[:4])+"-"+a.Request.ApprovalCode[4:], domain.OAuthScope{Projects: []string{"demo"}, Capabilities: []string{"retrieval:read"}}, 7*24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, domain.OAuthRequestApproved, granted.Status)

	poll, err := f.svc.Poll(t.Context(), a.Request.ID, a.Cookie)
	require.NoError(t, err)
	require.False(t, poll.Pending)
	u, err := url.Parse(poll.Redirect)
	require.NoError(t, err)
	assert.Equal(t, "client.example", u.Host)
	assert.Equal(t, "/cb", u.Path)
	q := u.Query()
	assert.Equal(t, "1", q.Get("tenant"), "the registered query is kept")
	assert.Equal(t, "st-1", q.Get("state"))
	assert.Equal(t, testIssuer, q.Get("iss"))
	assert.Empty(t, q.Get("error"))
	code := q.Get("code")
	require.Len(t, code, 52)
	stored := f.repo.requests[a.Request.ID]
	assert.Equal(t, oauthDigest(code), stored.CodeDigest, "only the code digest is stored")
	assert.Equal(t, f.now.Add(domain.OAuthCodeTTL), stored.CodeExpiresAt)
	assert.Equal(t, domain.OAuthScope{Projects: []string{"demo"}, Capabilities: []string{"retrieval:read"}}, stored.Granted)
	assert.Equal(t, 7*24*time.Hour, stored.GrantTTL)

	_, err = f.svc.Poll(t.Context(), a.Request.ID, a.Cookie)
	assert.ErrorIs(t, err, domain.ErrForbidden, "the redirect is released once")
	_, err = f.svc.DenyHost(t.Context(), a.Request.ApprovalCode)
	assert.ErrorIs(t, err, domain.ErrForbidden, "a decided request cannot be decided again")
}

func TestOAuthDeniedOrExpiredRequestRedirectsAccessDenied(t *testing.T) {
	for name, decide := range map[string]func(f *oauthFlow, a OAuthAuthorization){
		"denied": func(f *oauthFlow, a OAuthAuthorization) {
			_, err := f.svc.DenyHost(f.t.Context(), a.Request.ApprovalCode)
			require.NoError(f.t, err)
		},
		"expired": func(f *oauthFlow, a OAuthAuthorization) {
			f.now = f.now.Add(domain.OAuthRequestTTL)
			_, err := f.svc.ApproveHost(f.t.Context(), a.Request.ApprovalCode, domain.OAuthScope{Projects: []string{"demo"}, Capabilities: []string{"retrieval:read"}}, 0)
			require.ErrorIs(f.t, err, domain.ErrForbidden, "an expired request cannot be approved")
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newOAuthFlow(t)
			a := f.authorize()
			decide(f, a)
			poll, err := f.svc.Poll(t.Context(), a.Request.ID, a.Cookie)
			require.NoError(t, err)
			u, err := url.Parse(poll.Redirect)
			require.NoError(t, err)
			assert.Equal(t, "access_denied", u.Query().Get("error"))
			assert.Equal(t, "st-1", u.Query().Get("state"))
			assert.Empty(t, u.Query().Get("code"))
			assert.Empty(t, f.repo.requests[a.Request.ID].CodeDigest)
			_, err = f.svc.Poll(t.Context(), a.Request.ID, a.Cookie)
			assert.ErrorIs(t, err, domain.ErrForbidden)
		})
	}
}

func TestOAuthGrantNeverWidensTheRequest(t *testing.T) {
	f := newOAuthFlow(t)
	a := f.authorize()
	for name, tc := range map[string]struct {
		grant domain.OAuthScope
		ttl   time.Duration
		want  error
	}{
		"other project":       {domain.OAuthScope{Projects: []string{"secret"}, Capabilities: []string{"retrieval:read"}}, 0, domain.ErrForbidden},
		"extra capability":    {domain.OAuthScope{Projects: []string{"demo"}, Capabilities: []string{"memory:export"}}, 0, domain.ErrForbidden},
		"owner capability":    {domain.OAuthScope{Projects: []string{"demo"}, Capabilities: []string{"verification:write"}}, 0, domain.ErrInvalidInput},
		"no project":          {domain.OAuthScope{Capabilities: []string{"retrieval:read"}}, 0, domain.ErrInvalidInput},
		"no capability":       {domain.OAuthScope{Projects: []string{"demo"}}, 0, domain.ErrInvalidInput},
		"too long":            {domain.OAuthScope{Projects: []string{"demo"}, Capabilities: []string{"retrieval:read"}}, domain.MaxOAuthGrantTTL + time.Second, domain.ErrInvalidInput},
		"duplicate project":   {domain.OAuthScope{Projects: []string{"demo", "demo"}, Capabilities: []string{"retrieval:read"}}, 0, domain.ErrInvalidInput},
		"project with spaces": {domain.OAuthScope{Projects: []string{"demo notes"}, Capabilities: []string{"retrieval:read"}}, 0, domain.ErrInvalidInput},
	} {
		_, err := f.svc.ApproveHost(t.Context(), a.Request.ApprovalCode, tc.grant, tc.ttl)
		assert.ErrorIs(t, err, tc.want, name)
	}
	assert.Equal(t, domain.OAuthRequestPending, f.repo.requests[a.Request.ID].Status)

	// A request that names nothing leaves the choice to the owner, within the
	// agent capabilities.
	in := f.input()
	in.Scope = ""
	open, err := f.svc.Authorize(t.Context(), in)
	require.NoError(t, err)
	_, err = f.svc.ApproveHost(t.Context(), open.Request.ApprovalCode, domain.OAuthScope{Projects: []string{"any"}, Capabilities: []string{"verification:write"}}, 0)
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
	req, err := f.svc.ApproveHost(t.Context(), open.Request.ApprovalCode, domain.OAuthScope{Projects: []string{"any"}, Capabilities: []string{"retrieval:read"}}, 0)
	require.NoError(t, err)
	assert.Equal(t, domain.DefaultOAuthGrantTTL, req.GrantTTL)
}

func TestOAuthHostDecisionsNeedAnEnrolledOwner(t *testing.T) {
	f := newOAuthFlow(t)
	a := f.authorize()
	f.svc.WithGrants(testIssuer, ownerFake{})
	_, err := f.svc.ApproveHost(t.Context(), a.Request.ApprovalCode, domain.OAuthScope{Projects: []string{"demo"}, Capabilities: []string{"retrieval:read"}}, 0)
	assert.ErrorIs(t, err, domain.ErrForbidden)
	_, err = f.svc.ListRequestsHost(t.Context())
	assert.ErrorIs(t, err, domain.ErrForbidden)
	_, err = f.svc.DenyHost(t.Context(), "nope")
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
}

func TestApprovalCodesAvoidLookAlikes(t *testing.T) {
	for range 200 {
		code := newApprovalCode()
		assert.Len(t, code, domain.OAuthApprovalCodeLength)
		assert.NotContains(t, code, "0")
		assert.NotContains(t, code, "O")
		assert.NotContains(t, code, "1")
		assert.NotContains(t, code, "I")
		assert.NotContains(t, code, "L")
	}
}

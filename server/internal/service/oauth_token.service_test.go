package service

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// grantStoreFake keeps grants in memory beside oauthRepoFake's requests.
type grantStoreFake struct {
	repo   *oauthRepoFake
	grants map[string]domain.OAuthGrant
	used   map[string]string
}

func (g *grantStoreFake) ClaimCode(_ context.Context, _ Tx, digest, grantID string) (domain.OAuthRequest, error) {
	for id, r := range g.repo.requests {
		if r.CodeDigest != digest || digest == "" || r.Status != domain.OAuthRequestApproved || !r.Released {
			continue
		}
		if r.CodeUsed {
			return r, domain.ErrConflict
		}
		r.CodeUsed, r.GrantID = true, grantID
		g.repo.requests[id] = r
		return r, nil
	}
	return domain.OAuthRequest{}, domain.ErrForbidden
}

func (g *grantStoreFake) AddGrant(_ context.Context, _ Tx, grant domain.OAuthGrant) error {
	g.grants[grant.ID] = grant
	return nil
}

func (g *grantStoreFake) FindGrantByRefresh(_ context.Context, _ Tx, digest string) (domain.OAuthGrant, error) {
	for _, grant := range g.grants {
		if grant.RefreshDigest == digest {
			return grant, nil
		}
	}
	if id, ok := g.used[digest]; ok {
		if grant, ok := g.grants[id]; ok {
			return grant, domain.ErrConflict
		}
	}
	return domain.OAuthGrant{}, domain.ErrForbidden
}

func (g *grantStoreFake) FindGrantByClient(_ context.Context, _ Tx, client string) (domain.OAuthGrant, error) {
	for _, grant := range g.grants {
		if grant.ClientID == client {
			return grant, nil
		}
	}
	return domain.OAuthGrant{}, domain.ErrForbidden
}

func (g *grantStoreFake) RotateRefresh(_ context.Context, _ Tx, id, old, next string) error {
	grant, ok := g.grants[id]
	if !ok || grant.RefreshDigest != old {
		return domain.ErrConflict
	}
	grant.RefreshDigest = next
	g.grants[id] = grant
	g.used[old] = id
	return nil
}

func (g *grantStoreFake) DeleteGrant(_ context.Context, _ Tx, id string) error {
	delete(g.grants, id)
	for digest, grant := range g.used {
		if grant == id {
			delete(g.used, digest)
		}
	}
	return nil
}

func (g *grantStoreFake) ExpiredGrants(_ context.Context, _ Tx, now time.Time, _ int) ([]domain.OAuthGrant, error) {
	var out []domain.OAuthGrant
	for _, grant := range g.grants {
		if !now.Before(grant.ExpiresAt) {
			out = append(out, grant)
		}
	}
	return out, nil
}

// clientsFake is the identity repository's client records, in memory.
type clientsFake struct {
	ownerFake
	clients map[string]ClientRecord
}

func (c *clientsFake) FindToken(_ context.Context, _ Tx, digest string) (ClientRecord, error) {
	for _, r := range c.clients {
		if r.TokenDigest == digest {
			return r, nil
		}
	}
	return ClientRecord{}, domain.ErrUnauthorized
}

func (c *clientsFake) PutClient(_ context.Context, _ Tx, r ClientRecord) error {
	if _, ok := c.clients[r.Principal.ClientID]; ok {
		return domain.ErrConflict
	}
	c.clients[r.Principal.ClientID] = r
	return nil
}

func (c *clientsFake) RotateClient(_ context.Context, _ Tx, id, digest string, expires *time.Time) error {
	r, ok := c.clients[id]
	if !ok || r.RevokedAt != nil {
		return domain.ErrForbidden
	}
	r.TokenDigest, r.ExpiresAt = digest, expires
	c.clients[id] = r
	return nil
}

func (c *clientsFake) RevokeClient(_ context.Context, _ Tx, id string) error {
	r, ok := c.clients[id]
	if !ok {
		return domain.ErrForbidden
	}
	at := time.Now()
	r.RevokedAt = &at
	c.clients[id] = r
	return nil
}

const testVerifier = "kb-test-verifier-0123456789-abcdefghijklmnopqrstuvwxyz"

type tokenFlow struct {
	*oauthFlow
	grants  *grantStoreFake
	clients *clientsFake
}

func newTokenFlow(t *testing.T) *tokenFlow {
	f := &tokenFlow{oauthFlow: newOAuthFlow(t)}
	f.grants = &grantStoreFake{repo: f.repo, grants: map[string]domain.OAuthGrant{}, used: map[string]string{}}
	f.clients = &clientsFake{ownerFake: ownerFake{OwnerIdentity{ID: "owner", PublicKey: strings.Repeat("a", 64)}}, clients: map[string]ClientRecord{}}
	f.svc.WithClients(f.clients, f.grants)
	return f
}

// approvedCode runs the flow up to the redirect and returns the code.
func (f *tokenFlow) approvedCode(ttl time.Duration) string {
	in := f.input()
	in.CodeChallenge = challenge(testVerifier)
	a, err := f.svc.Authorize(f.t.Context(), in)
	require.NoError(f.t, err)
	_, err = f.svc.ApproveHost(f.t.Context(), a.Request.ApprovalCode, domain.OAuthScope{Projects: []string{"demo"}, Capabilities: []string{"retrieval:read"}}, ttl)
	require.NoError(f.t, err)
	poll, err := f.svc.Poll(f.t.Context(), a.Request.ID, a.Cookie)
	require.NoError(f.t, err)
	u, err := url.Parse(poll.Redirect)
	require.NoError(f.t, err)
	return u.Query().Get("code")
}

func (f *tokenFlow) exchangeInput(code string) OAuthTokenInput {
	return OAuthTokenInput{GrantType: "authorization_code", ClientID: "oc_client", Code: code, RedirectURI: "https://client.example/cb?tenant=1", CodeVerifier: testVerifier, Resource: testIssuer + "/mcp"}
}

func (f *tokenFlow) principal(access string) (domain.Principal, error) {
	ids := &IdentityService{tx: directTx{}, repo: identityAdapter{f.clients}, now: func() time.Time { return f.now }}
	return ids.Authenticate(f.t.Context(), access)
}

// identityAdapter lets IdentityService authenticate against clientsFake.
type identityAdapter struct{ *clientsFake }

func (identityAdapter) ListClients(context.Context, Tx, string, int, string) ([]domain.ClientMetadata, error) {
	return nil, nil
}
func (identityAdapter) PutOwner(context.Context, Tx, OwnerIdentity) error { return nil }
func (identityAdapter) ReplaceOwnerKey(context.Context, Tx, domain.OwnerKeyChange) error {
	return nil
}

func requireTokenError(t *testing.T, err error, code string, msg ...any) {
	t.Helper()
	var te *OAuthTokenError
	require.ErrorAs(t, err, &te, msg...)
	assert.Equal(t, code, te.Code, msg...)
}

func TestOAuthCodeExchangeIssuesAGrantBoundToMCP(t *testing.T) {
	f := newTokenFlow(t)
	code := f.approvedCode(30 * 24 * time.Hour)
	tokens, err := f.svc.Token(t.Context(), f.exchangeInput(code))
	require.NoError(t, err)
	assert.Len(t, tokens.AccessToken, 64)
	assert.Len(t, tokens.RefreshToken, 64)
	assert.NotEqual(t, tokens.AccessToken, tokens.RefreshToken)
	assert.Equal(t, int64(3600), tokens.ExpiresIn)
	assert.Equal(t, "project:demo retrieval:read", tokens.Scope)
	assert.NotContains(t, tokens.String(), tokens.AccessToken)

	p, err := f.principal(tokens.AccessToken)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(p.ClientID, domain.OAuthClientPrefix))
	assert.Equal(t, domain.RoleAgent, p.Role)
	assert.Equal(t, []string{"demo"}, p.ProjectIDs)
	assert.Equal(t, []string{"retrieval:read"}, p.Capabilities, "the grant never carries more than the owner approved")
	assert.Equal(t, domain.OAuthResourceMCP, p.Resource)
	assert.Equal(t, "owner", p.OwnerID)
	for _, r := range f.clients.clients {
		assert.NotEqual(t, tokens.AccessToken, r.TokenDigest, "only digests are stored")
	}
	require.Len(t, f.grants.grants, 1)
	for _, g := range f.grants.grants {
		assert.Equal(t, TokenDigest(tokens.RefreshToken), g.RefreshDigest)
		assert.Equal(t, f.now.Add(30*24*time.Hour), g.ExpiresAt)
		assert.Equal(t, "oc_client", g.RegistrationID)
	}
}

func TestOAuthCodeWorksOnceOnlyWithItsVerifierRedirectAndClient(t *testing.T) {
	for name, mutate := range map[string]func(*OAuthTokenInput){
		"wrong verifier":     func(in *OAuthTokenInput) { in.CodeVerifier = strings.Repeat("x", 43) },
		"wrong redirect URI": func(in *OAuthTokenInput) { in.RedirectURI = "https://client.example/cb" },
		"another client":     func(in *OAuthTokenInput) { in.ClientID = "oc_other" },
	} {
		f := newTokenFlow(t)
		code := f.approvedCode(0)
		in := f.exchangeInput(code)
		mutate(&in)
		_, err := f.svc.Token(t.Context(), in)
		requireTokenError(t, err, "invalid_grant", name)
		_, err = f.svc.Token(t.Context(), f.exchangeInput(code))
		requireTokenError(t, err, "invalid_grant", name+": a code presented wrongly is spent")
		assert.Empty(t, f.clients.clients, name)
	}

	f := newTokenFlow(t)
	code := f.approvedCode(0)
	f.now = f.now.Add(domain.OAuthCodeTTL)
	_, err := f.svc.Token(t.Context(), f.exchangeInput(code))
	requireTokenError(t, err, "invalid_grant", "a code lives 60 seconds")

	f = newTokenFlow(t)
	_, err = f.svc.Token(t.Context(), f.exchangeInput("unknown"))
	requireTokenError(t, err, "invalid_grant")
}

func TestOAuthSecondUseOfACodeRevokesItsGrant(t *testing.T) {
	f := newTokenFlow(t)
	code := f.approvedCode(0)
	tokens, err := f.svc.Token(t.Context(), f.exchangeInput(code))
	require.NoError(t, err)
	_, err = f.principal(tokens.AccessToken)
	require.NoError(t, err)

	_, err = f.svc.Token(t.Context(), f.exchangeInput(code))
	requireTokenError(t, err, "invalid_grant")
	_, err = f.principal(tokens.AccessToken)
	assert.ErrorIs(t, err, domain.ErrUnauthorized, "the grant's access token stops working")
	_, err = f.svc.Token(t.Context(), OAuthTokenInput{GrantType: "refresh_token", ClientID: "oc_client", RefreshToken: tokens.RefreshToken})
	requireTokenError(t, err, "invalid_grant", "and so does its refresh token")
}

func TestOAuthRefreshRotatesAndReuseRevokesTheGrant(t *testing.T) {
	f := newTokenFlow(t)
	first, err := f.svc.Token(t.Context(), f.exchangeInput(f.approvedCode(0)))
	require.NoError(t, err)
	refresh := OAuthTokenInput{GrantType: "refresh_token", ClientID: "oc_client", RefreshToken: first.RefreshToken}

	f.now = f.now.Add(30 * time.Minute)
	second, err := f.svc.Token(t.Context(), refresh)
	require.NoError(t, err)
	assert.NotEqual(t, first.RefreshToken, second.RefreshToken)
	assert.NotEqual(t, first.AccessToken, second.AccessToken)
	_, err = f.principal(first.AccessToken)
	assert.ErrorIs(t, err, domain.ErrUnauthorized, "the old access token is replaced")
	_, err = f.principal(second.AccessToken)
	require.NoError(t, err)

	_, err = f.svc.Token(t.Context(), OAuthTokenInput{GrantType: "refresh_token", ClientID: "oc_other", RefreshToken: second.RefreshToken})
	requireTokenError(t, err, "invalid_grant", "a refresh token works only for its client")

	_, err = f.svc.Token(t.Context(), refresh)
	requireTokenError(t, err, "invalid_grant", "a used refresh token is refused")
	_, err = f.principal(second.AccessToken)
	assert.ErrorIs(t, err, domain.ErrUnauthorized, "and its reuse ends the grant")
	_, err = f.svc.Token(t.Context(), OAuthTokenInput{GrantType: "refresh_token", ClientID: "oc_client", RefreshToken: second.RefreshToken})
	requireTokenError(t, err, "invalid_grant")
	assert.Empty(t, f.grants.grants)
}

func TestOAuthGrantThatKeepsRefreshingStillEndsAtItsExpiry(t *testing.T) {
	f := newTokenFlow(t)
	tokens, err := f.svc.Token(t.Context(), f.exchangeInput(f.approvedCode(90*time.Minute)))
	require.NoError(t, err)
	assert.Equal(t, int64(3600), tokens.ExpiresIn)

	f.now = f.now.Add(time.Hour)
	tokens, err = f.svc.Token(t.Context(), OAuthTokenInput{GrantType: "refresh_token", ClientID: "oc_client", RefreshToken: tokens.RefreshToken})
	require.NoError(t, err)
	assert.Equal(t, int64(1800), tokens.ExpiresIn, "access never outlives the grant")

	f.now = f.now.Add(30 * time.Minute)
	_, err = f.principal(tokens.AccessToken)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
	_, err = f.svc.Token(t.Context(), OAuthTokenInput{GrantType: "refresh_token", ClientID: "oc_client", RefreshToken: tokens.RefreshToken})
	requireTokenError(t, err, "invalid_grant")
	assert.Empty(t, f.grants.grants, "an expired grant is removed")
}

func TestOAuthRefreshAfterHostRevocationEndsTheGrant(t *testing.T) {
	f := newTokenFlow(t)
	tokens, err := f.svc.Token(t.Context(), f.exchangeInput(f.approvedCode(0)))
	require.NoError(t, err)
	p, err := f.principal(tokens.AccessToken)
	require.NoError(t, err)
	require.NoError(t, f.clients.RevokeClient(t.Context(), nil, p.ClientID))
	_, err = f.svc.Token(t.Context(), OAuthTokenInput{GrantType: "refresh_token", ClientID: "oc_client", RefreshToken: tokens.RefreshToken})
	requireTokenError(t, err, "invalid_grant")
	assert.Empty(t, f.grants.grants)
}

func TestOAuthRevokeEndsTheGrantFromEitherToken(t *testing.T) {
	for _, which := range []string{"access", "refresh"} {
		f := newTokenFlow(t)
		tokens, err := f.svc.Token(t.Context(), f.exchangeInput(f.approvedCode(0)))
		require.NoError(t, err)
		token := tokens.AccessToken
		if which == "refresh" {
			token = tokens.RefreshToken
		}
		require.NoError(t, f.svc.Revoke(t.Context(), "oc_other", token), which)
		_, err = f.principal(tokens.AccessToken)
		require.NoError(t, err, "%s: another client's revocation changes nothing", which)

		require.NoError(t, f.svc.Revoke(t.Context(), "oc_client", token), which)
		_, err = f.principal(tokens.AccessToken)
		assert.ErrorIs(t, err, domain.ErrUnauthorized, which)
		assert.Empty(t, f.grants.grants, which)
		require.NoError(t, f.svc.Revoke(t.Context(), "oc_client", token), "%s: revoking twice is not an error", which)
	}
	f := newTokenFlow(t)
	require.NoError(t, f.svc.Revoke(t.Context(), "oc_client", strings.Repeat("0", 64)))
	require.NoError(t, f.svc.Revoke(t.Context(), "oc_client", "short"))
	requireTokenError(t, f.svc.Revoke(t.Context(), "", "x"), "invalid_request")
}

func TestOAuthTokenRequestRefusals(t *testing.T) {
	f := newTokenFlow(t)
	code := f.approvedCode(0)
	for name, tc := range map[string]struct {
		mutate func(*OAuthTokenInput)
		code   string
	}{
		"no grant type":      {func(in *OAuthTokenInput) { in.GrantType = "" }, "invalid_request"},
		"implicit":           {func(in *OAuthTokenInput) { in.GrantType = "password" }, "unsupported_grant_type"},
		"no client":          {func(in *OAuthTokenInput) { in.ClientID = "" }, "invalid_request"},
		"other resource":     {func(in *OAuthTokenInput) { in.Resource = "https://evil.example/mcp" }, "invalid_target"},
		"short verifier":     {func(in *OAuthTokenInput) { in.CodeVerifier = "short" }, "invalid_request"},
		"verifier bad chars": {func(in *OAuthTokenInput) { in.CodeVerifier = strings.Repeat("a", 42) + "!" }, "invalid_request"},
		"no redirect":        {func(in *OAuthTokenInput) { in.RedirectURI = "" }, "invalid_request"},
	} {
		in := f.exchangeInput(code)
		tc.mutate(&in)
		_, err := f.svc.Token(t.Context(), in)
		requireTokenError(t, err, tc.code, name)
	}
	_, err := f.svc.Token(t.Context(), f.exchangeInput(code))
	require.NoError(t, err, "refusals before the code is looked up do not spend it")
}

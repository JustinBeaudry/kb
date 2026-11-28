package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type oauthRepoFake struct {
	regs   []domain.OAuthRegistration
	pruned []time.Time
	full   bool
}

func (f *oauthRepoFake) AddRegistration(_ context.Context, _ Tx, reg domain.OAuthRegistration, maxPending int) error {
	if f.full || len(f.regs) >= maxPending {
		return domain.ErrBudget
	}
	f.regs = append(f.regs, reg)
	return nil
}

func (f *oauthRepoFake) FindRegistration(context.Context, Tx, string, time.Time) (domain.OAuthRegistration, error) {
	return domain.OAuthRegistration{}, domain.ErrUnauthorized
}

func (f *oauthRepoFake) PruneRegistrations(_ context.Context, _ Tx, now time.Time, _ int) (int, error) {
	f.pruned = append(f.pruned, now)
	return 0, nil
}

func TestOAuthRegisterStoresPublicClient(t *testing.T) {
	at := time.Date(2026, 11, 27, 9, 0, 0, 0, time.FixedZone("x", 3600))
	repo := &oauthRepoFake{}
	svc := NewOAuthService(directTx{}, repo, func() time.Time { return at })
	reg, err := svc.Register(t.Context(), OAuthRegistrationInput{
		RedirectURIs:  []string{"https://claude.ai/api/mcp/auth_callback", "http://127.0.0.1:33418/cb", "https://claude.ai/api/mcp/auth_callback"},
		ClientName:    "  Claude  ",
		GrantTypes:    []string{"authorization_code", "refresh_token"},
		ResponseTypes: []string{"code"},
	})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(reg.ID, "oc_"))
	assert.Len(t, reg.ID, 3+26)
	assert.Equal(t, []string{"https://claude.ai/api/mcp/auth_callback", "http://127.0.0.1:33418/cb"}, reg.RedirectURIs, "duplicates are dropped and order kept")
	assert.Equal(t, "Claude", reg.ClientName)
	assert.Equal(t, at.UTC(), reg.CreatedAt)
	assert.Equal(t, at.UTC().Add(domain.OAuthRegistrationTTL), reg.ExpiresAt)
	require.Len(t, repo.regs, 1)
	assert.Equal(t, reg, repo.regs[0])
	assert.Equal(t, []time.Time{at.UTC()}, repo.pruned, "expired registrations are pruned before adding")

	other, err := svc.Register(t.Context(), OAuthRegistrationInput{RedirectURIs: []string{"https://a.example/cb"}, TokenEndpointAuthMethod: "none"})
	require.NoError(t, err)
	assert.NotEqual(t, reg.ID, other.ID)
}

func TestOAuthRegisterRefusesWhatKbWillNotIssue(t *testing.T) {
	long := "https://a.example/" + strings.Repeat("x", domain.MaxOAuthRedirectURILength)
	six := []string{"https://a.example/1", "https://a.example/2", "https://a.example/3", "https://a.example/4", "https://a.example/5", "https://a.example/6"}
	cases := map[string]struct {
		in   OAuthRegistrationInput
		want error
	}{
		"no redirect uri":           {OAuthRegistrationInput{}, domain.ErrInvalidRedirectURI},
		"too many redirect uris":    {OAuthRegistrationInput{RedirectURIs: six}, domain.ErrInvalidRedirectURI},
		"plain http":                {OAuthRegistrationInput{RedirectURIs: []string{"http://a.example/cb"}}, domain.ErrInvalidRedirectURI},
		"http localhost name":       {OAuthRegistrationInput{RedirectURIs: []string{"http://localhost:8080/cb"}}, domain.ErrInvalidRedirectURI},
		"custom scheme":             {OAuthRegistrationInput{RedirectURIs: []string{"myapp://cb"}}, domain.ErrInvalidRedirectURI},
		"javascript":                {OAuthRegistrationInput{RedirectURIs: []string{"javascript:alert(1)"}}, domain.ErrInvalidRedirectURI},
		"fragment":                  {OAuthRegistrationInput{RedirectURIs: []string{"https://a.example/cb#x"}}, domain.ErrInvalidRedirectURI},
		"empty fragment":            {OAuthRegistrationInput{RedirectURIs: []string{"https://a.example/cb#"}}, domain.ErrInvalidRedirectURI},
		"user info":                 {OAuthRegistrationInput{RedirectURIs: []string{"https://evil@a.example/cb"}}, domain.ErrInvalidRedirectURI},
		"relative":                  {OAuthRegistrationInput{RedirectURIs: []string{"/cb"}}, domain.ErrInvalidRedirectURI},
		"no host":                   {OAuthRegistrationInput{RedirectURIs: []string{"https:///cb"}}, domain.ErrInvalidRedirectURI},
		"space":                     {OAuthRegistrationInput{RedirectURIs: []string{"https://a.example/c b"}}, domain.ErrInvalidRedirectURI},
		"too long":                  {OAuthRegistrationInput{RedirectURIs: []string{long}}, domain.ErrInvalidRedirectURI},
		"confidential client":       {OAuthRegistrationInput{RedirectURIs: []string{"https://a.example/cb"}, TokenEndpointAuthMethod: "client_secret_basic"}, domain.ErrInvalidClientMetadata},
		"implicit grant":            {OAuthRegistrationInput{RedirectURIs: []string{"https://a.example/cb"}, GrantTypes: []string{"implicit"}}, domain.ErrInvalidClientMetadata},
		"client credentials":        {OAuthRegistrationInput{RedirectURIs: []string{"https://a.example/cb"}, GrantTypes: []string{"client_credentials"}}, domain.ErrInvalidClientMetadata},
		"refresh without code":      {OAuthRegistrationInput{RedirectURIs: []string{"https://a.example/cb"}, GrantTypes: []string{"refresh_token"}}, domain.ErrInvalidClientMetadata},
		"token response":            {OAuthRegistrationInput{RedirectURIs: []string{"https://a.example/cb"}, ResponseTypes: []string{"token"}}, domain.ErrInvalidClientMetadata},
		"name with control":         {OAuthRegistrationInput{RedirectURIs: []string{"https://a.example/cb"}, ClientName: "Claude\x1b[2J"}, domain.ErrInvalidClientMetadata},
		"name with bidi override":   {OAuthRegistrationInput{RedirectURIs: []string{"https://a.example/cb"}, ClientName: "Cla\u202eude"}, domain.ErrInvalidClientMetadata},
		"name too long":             {OAuthRegistrationInput{RedirectURIs: []string{"https://a.example/cb"}, ClientName: strings.Repeat("n", domain.MaxOAuthClientNameLength+1)}, domain.ErrInvalidClientMetadata},
		"loopback not an ip public": {OAuthRegistrationInput{RedirectURIs: []string{"http://127.0.0.1.evil.example/cb"}}, domain.ErrInvalidRedirectURI},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			repo := &oauthRepoFake{}
			_, err := NewOAuthService(directTx{}, repo, nil).Register(t.Context(), tc.in)
			require.ErrorIs(t, err, tc.want)
			require.ErrorIs(t, err, domain.ErrInvalidInput)
			assert.Empty(t, repo.regs)
		})
	}
}

func TestOAuthRegisterAcceptsLoopbackForNativeApps(t *testing.T) {
	for _, uri := range []string{"http://127.0.0.1/cb", "http://127.0.0.1:65535/cb", "http://[::1]:8080/cb", "https://localhost:8443/cb"} {
		_, err := NewOAuthService(directTx{}, &oauthRepoFake{}, nil).Register(t.Context(), OAuthRegistrationInput{RedirectURIs: []string{uri}})
		assert.NoError(t, err, uri)
	}
}

func TestOAuthRegisterStopsAtPendingCap(t *testing.T) {
	_, err := NewOAuthService(directTx{}, &oauthRepoFake{full: true}, nil).Register(t.Context(), OAuthRegistrationInput{RedirectURIs: []string{"https://a.example/cb"}})
	assert.ErrorIs(t, err, domain.ErrBudget)
}

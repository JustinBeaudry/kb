package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testMetadataID = "https://app.example/oauth/client.json"

type metadataFetcherFake struct {
	body   string
	maxAge time.Duration
	err    error
	calls  int
}

func (f *metadataFetcherFake) FetchClientMetadata(_ context.Context, id string) ([]byte, time.Duration, error) {
	f.calls++
	return []byte(f.body), f.maxAge, f.err
}

func metadataDoc(extra string) string {
	return `{"client_id":"` + testMetadataID + `","client_name":"App","redirect_uris":["https://app.example/cb"],"token_endpoint_auth_method":"none","grant_types":["authorization_code","refresh_token"],"response_types":["code"]` + extra + `}`
}

func newMetadataFlow(t *testing.T, fetcher *metadataFetcherFake) *oauthFlow {
	f := newOAuthFlow(t)
	f.svc.WithClientMetadata(fetcher)
	return f
}

func metadataInput(f *oauthFlow) OAuthAuthorizeInput {
	in := f.input()
	in.ClientID = testMetadataID
	in.RedirectURI = "https://app.example/cb"
	return in
}

func TestOAuthAuthorizeAcceptsAClientIDMetadataDocument(t *testing.T) {
	fetcher := &metadataFetcherFake{body: metadataDoc("")}
	f := newMetadataFlow(t, fetcher)
	a, err := f.svc.Authorize(t.Context(), metadataInput(f))
	require.NoError(t, err)
	assert.Equal(t, testMetadataID, a.Request.ClientID, "the request names the document URL, which token exchange then checks")
	assert.Equal(t, "App", a.Request.ClientName)

	var stored *domain.OAuthRegistration
	for i := range f.repo.regs {
		if f.repo.regs[i].ID == testMetadataID {
			stored = &f.repo.regs[i]
		}
	}
	require.NotNil(t, stored, "a registration keyed by the URL backs grants and pruning")
	assert.Equal(t, []string{"https://app.example/cb"}, stored.RedirectURIs)
	assert.Equal(t, f.now.Add(domain.OAuthRegistrationTTL), stored.ExpiresAt)

	f.now = f.now.Add(2 * time.Minute)
	_, err = f.svc.Authorize(t.Context(), metadataInput(f))
	require.NoError(t, err)
	assert.Equal(t, 1, fetcher.calls, "a document is reused for its default lifetime")

	f.now = f.now.Add(clientMetadataDefaultCache)
	fetcher.body = `{"client_id":"` + testMetadataID + `","client_name":"App 2","redirect_uris":["https://app.example/new"]}`
	in := metadataInput(f)
	_, err = f.svc.Authorize(t.Context(), in)
	assert.ErrorIs(t, err, domain.ErrUnauthorized, "after expiry the document is read again, and it no longer lists the old redirect URI")
	assert.Equal(t, 2, fetcher.calls)
	in.RedirectURI = "https://app.example/new"
	a, err = f.svc.Authorize(t.Context(), in)
	require.NoError(t, err)
	assert.Equal(t, "App 2", a.Request.ClientName)
	assert.Equal(t, 2, fetcher.calls)
}

func TestOAuthAuthorizeRefusesMetadataClientsKbCannotTrust(t *testing.T) {
	cases := map[string]struct {
		id, body string
		err      error
		fetches  int
	}{
		"fetch failed":          {testMetadataID, "", errors.New("dial refused"), 1},
		"not json":              {testMetadataID, "<html>", nil, 1},
		"two objects":           {testMetadataID, metadataDoc("") + "{}", nil, 1},
		"trailing garbage":      {testMetadataID, metadataDoc("") + "x", nil, 1},
		"array":                 {testMetadataID, "[]", nil, 1},
		"null":                  {testMetadataID, "null", nil, 1},
		"other client_id":       {testMetadataID, `{"client_id":"https://evil.example/c.json","redirect_uris":["https://app.example/cb"]}`, nil, 1},
		"client_id differs":     {testMetadataID, `{"client_id":"https://app.example/oauth/client.json/","redirect_uris":["https://app.example/cb"]}`, nil, 1},
		"shared secret":         {testMetadataID, metadataDoc(`,"client_secret":"s"`), nil, 1},
		"secret expiry":         {testMetadataID, metadataDoc(`,"client_secret_expires_at":0`), nil, 1},
		"confidential":          {testMetadataID, `{"client_id":"` + testMetadataID + `","redirect_uris":["https://app.example/cb"],"token_endpoint_auth_method":"private_key_jwt"}`, nil, 1},
		"implicit grant":        {testMetadataID, `{"client_id":"` + testMetadataID + `","redirect_uris":["https://app.example/cb"],"grant_types":["implicit"]}`, nil, 1},
		"no redirect uris":      {testMetadataID, `{"client_id":"` + testMetadataID + `"}`, nil, 1},
		"http redirect":         {testMetadataID, `{"client_id":"` + testMetadataID + `","redirect_uris":["http://app.example/cb"]}`, nil, 1},
		"unlisted redirect uri": {testMetadataID, `{"client_id":"` + testMetadataID + `","redirect_uris":["https://app.example/other"]}`, nil, 1},
		"not a metadata url":    {"https://app.example:8443/c.json", metadataDoc(""), nil, 0},
		"query in url":          {"https://app.example/c.json?x=1", metadataDoc(""), nil, 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fetcher := &metadataFetcherFake{body: tc.body, err: tc.err}
			f := newMetadataFlow(t, fetcher)
			in := metadataInput(f)
			in.ClientID = tc.id
			_, err := f.svc.Authorize(t.Context(), in)
			assert.ErrorIs(t, err, domain.ErrUnauthorized, "never redirected: the redirect URI is not known to be the client's")
			assert.Equal(t, tc.fetches, fetcher.calls)
			assert.Len(t, f.repo.regs, 1, "nothing is stored for a refused client")
			assert.Empty(t, f.repo.requests)
		})
	}
}

func TestOAuthAuthorizeIgnoresMetadataClientsWhenNotEnabled(t *testing.T) {
	f := newOAuthFlow(t)
	_, err := f.svc.Authorize(t.Context(), metadataInput(f))
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
}

func TestOAuthMetadataRegistrationsShareTheRegistrationCap(t *testing.T) {
	fetcher := &metadataFetcherFake{body: metadataDoc("")}
	f := newMetadataFlow(t, fetcher)
	f.repo.full = true
	_, err := f.svc.Authorize(t.Context(), metadataInput(f))
	var redirect *OAuthRedirectError
	require.ErrorAs(t, err, &redirect, "the redirect URI is known by now, so the client hears it should retry")
	assert.Equal(t, "temporarily_unavailable", redirect.Code)
}

func TestClientMetadataCacheHonorsBoundedLifetimesAndSize(t *testing.T) {
	now := time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC)
	fetcher := &metadataFetcherFake{body: metadataDoc(""), maxAge: time.Nanosecond}
	c := &clientMetadataCache{fetcher: fetcher, entries: map[string]clientMetadata{}}
	_, err := c.get(t.Context(), testMetadataID, now)
	require.NoError(t, err)
	_, err = c.get(t.Context(), testMetadataID, now.Add(clientMetadataMinCache-time.Second))
	require.NoError(t, err)
	assert.Equal(t, 1, fetcher.calls, "no-store is still reused for the minimum lifetime, so a client cannot make kb fetch on every request")

	fetcher.maxAge = 30 * 24 * time.Hour
	_, err = c.get(t.Context(), testMetadataID, now.Add(clientMetadataMinCache))
	require.NoError(t, err)
	assert.Equal(t, now.Add(clientMetadataMinCache+clientMetadataMaxCache), c.entries[testMetadataID].expires, "a long max-age is capped")

	c.entries = map[string]clientMetadata{}
	for i := range clientMetadataCacheEntries {
		c.entries[time.Duration(i).String()] = clientMetadata{expires: now.Add(time.Hour + time.Duration(i))}
	}
	fetcher.maxAge = 0
	_, err = c.get(t.Context(), testMetadataID, now)
	require.NoError(t, err)
	assert.Len(t, c.entries, clientMetadataCacheEntries, "a full cache drops the entry closest to expiry")
	_, kept := c.entries["0s"]
	assert.False(t, kept)
}

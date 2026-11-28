package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// OAuthClientMetadataFetcher reads a client ID metadata document. It returns
// the body and how long the publisher allows it to be cached (zero when the
// response gave no lifetime). Any error means the client is not recognized.
type OAuthClientMetadataFetcher interface {
	FetchClientMetadata(ctx context.Context, clientID string) ([]byte, time.Duration, error)
}

const (
	clientMetadataMinCache     = time.Minute
	clientMetadataDefaultCache = 5 * time.Minute
	clientMetadataMaxCache     = 24 * time.Hour
	clientMetadataCacheEntries = 128
)

// clientMetadata is what kb keeps from a validated document.
type clientMetadata struct {
	redirectURIs []string
	name         string
	expires      time.Time
}

// clientMetadataCache holds validated documents for their allowed lifetime,
// bounded to [1 minute, 24 hours] and to a fixed number of clients, so a flood
// of client IDs cannot grow it without bound.
type clientMetadataCache struct {
	fetcher OAuthClientMetadataFetcher
	mu      sync.Mutex
	entries map[string]clientMetadata
}

// WithClientMetadata lets clients identify themselves with a client ID
// metadata document URL instead of a dynamic registration.
func (s *OAuthService) WithClientMetadata(fetcher OAuthClientMetadataFetcher) *OAuthService {
	if fetcher != nil {
		s.meta = &clientMetadataCache{fetcher: fetcher, entries: map[string]clientMetadata{}}
	}
	return s
}

// metadataRegistration resolves a metadata document client ID to a
// registration for redirectURI, refreshing the stored registration so grants,
// token exchange and pruning treat it like any other. Until the document
// names redirectURI, errors are ErrUnauthorized and must not be redirected.
func (s *OAuthService) metadataRegistration(ctx context.Context, clientID, redirectURI string, now time.Time) (domain.OAuthRegistration, error) {
	if s.meta == nil || !domain.ValidClientMetadataURL(clientID) {
		return domain.OAuthRegistration{}, domain.ErrUnauthorized
	}
	meta, err := s.meta.get(ctx, clientID, now)
	if err != nil {
		return domain.OAuthRegistration{}, err
	}
	if redirectURI == "" || !slices.Contains(meta.redirectURIs, redirectURI) {
		return domain.OAuthRegistration{}, domain.ErrUnauthorized
	}
	reg := domain.OAuthRegistration{ID: clientID, RedirectURIs: meta.redirectURIs, ClientName: meta.name, CreatedAt: now, ExpiresAt: now.Add(domain.OAuthRegistrationTTL)}
	err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := s.repo.PruneRegistrations(ctx, tx, now, oauthPruneBatch); err != nil {
			return err
		}
		return s.repo.PutMetadataRegistration(ctx, tx, reg, domain.MaxPendingOAuthRegistrations)
	})
	if errors.Is(err, domain.ErrBudget) {
		return domain.OAuthRegistration{}, &OAuthRedirectError{"temporarily_unavailable", "Too many apps are waiting for the owner. Try again later."}
	}
	if err != nil {
		return domain.OAuthRegistration{}, err
	}
	return reg, nil
}

func (c *clientMetadataCache) get(ctx context.Context, clientID string, now time.Time) (clientMetadata, error) {
	c.mu.Lock()
	entry, ok := c.entries[clientID]
	c.mu.Unlock()
	if ok && now.Before(entry.expires) {
		return entry, nil
	}
	body, maxAge, err := c.fetcher.FetchClientMetadata(ctx, clientID)
	if err != nil {
		return clientMetadata{}, domain.ErrUnauthorized
	}
	meta, err := parseClientMetadata(body, clientID)
	if err != nil {
		return clientMetadata{}, domain.ErrUnauthorized
	}
	if maxAge == 0 {
		maxAge = clientMetadataDefaultCache
	}
	meta.expires = now.Add(min(max(maxAge, clientMetadataMinCache), clientMetadataMaxCache))
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, present := c.entries[clientID]; !present && len(c.entries) >= clientMetadataCacheEntries {
		c.evict(now)
	}
	c.entries[clientID] = meta
	return meta, nil
}

// evict drops expired entries, or the one closest to expiry when none are.
func (c *clientMetadataCache) evict(now time.Time) {
	oldest := ""
	for id, e := range c.entries {
		if !now.Before(e.expires) {
			delete(c.entries, id)
			continue
		}
		if oldest == "" || e.expires.Before(c.entries[oldest].expires) {
			oldest = id
		}
	}
	if len(c.entries) >= clientMetadataCacheEntries && oldest != "" {
		delete(c.entries, oldest)
	}
}

type clientMetadataDocument struct {
	ClientID                string   `json:"client_id"`
	RedirectURIs            []string `json:"redirect_uris"`
	ClientName              string   `json:"client_name"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
}

// parseClientMetadata validates a document: one JSON object whose client_id
// is exactly the URL it was fetched from, with no shared secret, describing a
// public client kb could also accept by dynamic registration.
func parseClientMetadata(body []byte, clientID string) (clientMetadata, error) {
	var fields map[string]json.RawMessage
	if err := decodeOne(body, &fields); err != nil || fields == nil {
		return clientMetadata{}, domain.ErrInvalidClientMetadata
	}
	for _, secret := range []string{"client_secret", "client_secret_expires_at"} {
		if _, ok := fields[secret]; ok {
			return clientMetadata{}, domain.ErrInvalidClientMetadata
		}
	}
	var doc clientMetadataDocument
	if err := decodeOne(body, &doc); err != nil || doc.ClientID != clientID {
		return clientMetadata{}, domain.ErrInvalidClientMetadata
	}
	uris, name, err := validClientMetadata(OAuthRegistrationInput{
		RedirectURIs: doc.RedirectURIs, ClientName: doc.ClientName, TokenEndpointAuthMethod: doc.TokenEndpointAuthMethod,
		GrantTypes: doc.GrantTypes, ResponseTypes: doc.ResponseTypes,
	}, domain.MaxOAuthMetadataRedirectURIs)
	if err != nil {
		return clientMetadata{}, err
	}
	return clientMetadata{redirectURIs: uris, name: name}, nil
}

func decodeOne(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return domain.ErrInvalidClientMetadata
	}
	return nil
}

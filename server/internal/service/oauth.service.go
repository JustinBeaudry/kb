package service

import (
	"context"
	"crypto/rand"
	"slices"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// OAuthRepository persists OAuth client registrations.
type OAuthRepository interface {
	AddRegistration(ctx context.Context, tx Tx, reg domain.OAuthRegistration, maxPending int) error
	FindRegistration(ctx context.Context, tx Tx, id string, now time.Time) (domain.OAuthRegistration, error)
	PutMetadataRegistration(ctx context.Context, tx Tx, reg domain.OAuthRegistration, maxPending int) error
	PruneRegistrations(ctx context.Context, tx Tx, now time.Time, limit int) (int, error)
	AddRequest(ctx context.Context, tx Tx, req domain.OAuthRequest, maxPending int) error
	FindRequest(ctx context.Context, tx Tx, id string) (domain.OAuthRequest, error)
	FindPendingRequest(ctx context.Context, tx Tx, code string, now time.Time) (domain.OAuthRequest, error)
	ListPendingRequests(ctx context.Context, tx Tx, now time.Time, limit int) ([]domain.OAuthRequest, error)
	DecideRequest(ctx context.Context, tx Tx, id string, status domain.OAuthRequestStatus, granted domain.OAuthScope, ttl time.Duration, now time.Time) error
	ReleaseRequest(ctx context.Context, tx Tx, id, codeDigest string, codeExpires time.Time) error
	PruneRequests(ctx context.Context, tx Tx, before time.Time, limit int) (int, error)
}

// OAuthRegistrationInput is the client metadata kb reads from a dynamic
// registration request. Everything else a client sends is ignored, as RFC
// 7591 §2 requires, and never stored.
type OAuthRegistrationInput struct {
	RedirectURIs            []string
	ClientName              string
	TokenEndpointAuthMethod string
	GrantTypes              []string
	ResponseTypes           []string
}

const oauthPruneBatch = 100

// OAuthService registers public OAuth clients. A registration grants nothing:
// it only lets a client ask the owner for a grant.
type OAuthService struct {
	tx      Transactor
	repo    OAuthRepository
	now     func() time.Time
	issuer  string
	owners  AccessOwners
	clients OAuthClients
	grants  OAuthGrantStore
	meta    *clientMetadataCache
}

func NewOAuthService(tx Transactor, repo OAuthRepository, now func() time.Time) *OAuthService {
	if now == nil {
		now = time.Now
	}
	return &OAuthService{tx: tx, repo: repo, now: now}
}

// WithGrants enables grant requests. issuer is kb's public origin, which is
// also the authorization server's issuer; the only resource a grant may name
// is issuer + "/mcp". owners resolves the enrolled owner for host decisions.
func (s *OAuthService) WithGrants(issuer string, owners AccessOwners) *OAuthService {
	s.issuer = issuer
	s.owners = owners
	return s
}

// Register validates the metadata and stores a new public client. Only public
// clients using the authorization code grant with PKCE are accepted.
func (s *OAuthService) Register(ctx context.Context, in OAuthRegistrationInput) (domain.OAuthRegistration, error) {
	uris, name, err := validClientMetadata(in, domain.MaxOAuthRedirectURIs)
	if err != nil {
		return domain.OAuthRegistration{}, err
	}
	now := s.now().UTC()
	reg := domain.OAuthRegistration{ID: "oc_" + rand.Text(), RedirectURIs: uris, ClientName: name, CreatedAt: now, ExpiresAt: now.Add(domain.OAuthRegistrationTTL)}
	err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := s.repo.PruneRegistrations(ctx, tx, now, oauthPruneBatch); err != nil {
			return err
		}
		return s.repo.AddRegistration(ctx, tx, reg, domain.MaxPendingOAuthRegistrations)
	})
	if err != nil {
		return domain.OAuthRegistration{}, err
	}
	return reg, nil
}

// validClientMetadata checks client metadata from a dynamic registration or a
// client ID metadata document. Only public clients using the authorization
// code grant are accepted. It returns the redirect URIs without duplicates
// and the trimmed client name.
func validClientMetadata(in OAuthRegistrationInput, maxURIs int) ([]string, string, error) {
	if len(in.RedirectURIs) == 0 || len(in.RedirectURIs) > maxURIs {
		return nil, "", domain.ErrInvalidRedirectURI
	}
	uris := make([]string, 0, len(in.RedirectURIs))
	for _, raw := range in.RedirectURIs {
		uri, err := domain.CanonicalRedirectURI(raw)
		if err != nil {
			return nil, "", err
		}
		if !slices.Contains(uris, uri) {
			uris = append(uris, uri)
		}
	}
	name, err := domain.CanonicalClientName(in.ClientName)
	if err != nil {
		return nil, "", err
	}
	if in.TokenEndpointAuthMethod != "" && in.TokenEndpointAuthMethod != "none" {
		return nil, "", domain.ErrInvalidClientMetadata
	}
	for _, g := range in.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			return nil, "", domain.ErrInvalidClientMetadata
		}
	}
	if len(in.GrantTypes) > 0 && !slices.Contains(in.GrantTypes, "authorization_code") {
		return nil, "", domain.ErrInvalidClientMetadata
	}
	for _, t := range in.ResponseTypes {
		if t != "code" {
			return nil, "", domain.ErrInvalidClientMetadata
		}
	}
	return uris, name, nil
}

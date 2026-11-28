package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// OAuthClients creates, rotates and revokes the client records OAuth grants
// authenticate as. The identity repository implements it.
type OAuthClients interface {
	Owner(context.Context, Tx) (OwnerIdentity, error)
	FindToken(context.Context, Tx, string) (ClientRecord, error)
	PutClient(context.Context, Tx, ClientRecord) error
	RotateClient(ctx context.Context, tx Tx, id, digest string, expiresAt *time.Time) error
	RevokeClient(context.Context, Tx, string) error
}

// OAuthGrantStore keeps grants and their refresh token digests.
type OAuthGrantStore interface {
	ClaimCode(ctx context.Context, tx Tx, codeDigest, grantID string) (domain.OAuthRequest, error)
	AddGrant(ctx context.Context, tx Tx, g domain.OAuthGrant) error
	FindGrantByRefresh(ctx context.Context, tx Tx, digest string) (domain.OAuthGrant, error)
	FindGrantByClient(ctx context.Context, tx Tx, clientID string) (domain.OAuthGrant, error)
	RotateRefresh(ctx context.Context, tx Tx, grantID, old, next string) error
	DeleteGrant(ctx context.Context, tx Tx, grantID string) error
	ExpiredGrants(ctx context.Context, tx Tx, now time.Time, limit int) ([]domain.OAuthGrant, error)
}

const oauthGrantPruneBatch = 20

// WithClients enables token exchange, refresh and revocation. grants must be
// the same store as the service's repository.
func (s *OAuthService) WithClients(clients OAuthClients, grants OAuthGrantStore) *OAuthService {
	s.clients = clients
	s.grants = grants
	return s
}

// OAuthTokenInput is a token request (OAuth 2.1 §3.2.2) from a public client.
type OAuthTokenInput struct {
	GrantType, ClientID, Code, RedirectURI, CodeVerifier, RefreshToken, Resource string
}

// OAuthTokens is a successful token response. Both tokens are returned only
// here; kb keeps their digests.
type OAuthTokens struct {
	AccessToken  string
	ExpiresIn    int64
	RefreshToken string
	Scope        string
}

func (OAuthTokens) String() string   { return "OAuthTokens([redacted])" }
func (OAuthTokens) GoString() string { return "OAuthTokens([redacted])" }

// OAuthTokenError is an error the token endpoint returns in OAuth terms.
type OAuthTokenError struct{ Code, Description string }

func (e *OAuthTokenError) Error() string { return "oauth " + e.Code }

func tokenError(code, description string) error { return &OAuthTokenError{code, description} }

var errInvalidGrant = &OAuthTokenError{"invalid_grant", "The code or refresh token is invalid, expired, used, or was issued to another client."}

// Token answers a token request: an authorization code exchange or a refresh.
func (s *OAuthService) Token(ctx context.Context, in OAuthTokenInput) (OAuthTokens, error) {
	if s.issuer == "" || s.clients == nil || s.grants == nil {
		return OAuthTokens{}, domain.ErrUnavailable
	}
	if in.ClientID == "" {
		return OAuthTokens{}, tokenError("invalid_request", "client_id is required.")
	}
	if in.Resource != "" && in.Resource != s.issuer+OAuthResourcePath {
		return OAuthTokens{}, tokenError("invalid_target", "resource must be kb's MCP endpoint.")
	}
	switch in.GrantType {
	case "authorization_code":
		return s.exchange(ctx, in)
	case "refresh_token":
		return s.refresh(ctx, in)
	case "":
		return OAuthTokens{}, tokenError("invalid_request", "grant_type is required.")
	default:
		return OAuthTokens{}, tokenError("unsupported_grant_type", "kb supports authorization_code and refresh_token.")
	}
}

func (s *OAuthService) exchange(ctx context.Context, in OAuthTokenInput) (OAuthTokens, error) {
	if in.Code == "" || len(in.Code) > 256 || in.RedirectURI == "" {
		return OAuthTokens{}, tokenError("invalid_request", "code and redirect_uri are required.")
	}
	if !validCodeVerifier(in.CodeVerifier) {
		return OAuthTokens{}, tokenError("invalid_request", "code_verifier must be 43 to 128 unreserved characters.")
	}
	now := s.now().UTC()
	var tokens OAuthTokens
	var refused error
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		if err := s.pruneGrants(ctx, tx, now); err != nil {
			return err
		}
		grantID := rand.Text()
		req, err := s.grants.ClaimCode(ctx, tx, oauthDigest(in.Code), grantID)
		switch {
		case errors.Is(err, domain.ErrConflict):
			// A second use of a code means a copy is in someone else's
			// hands: the grant it produced ends now (OAuth 2.1 §4.1.2).
			refused = errInvalidGrant
			return s.revokeGrantByID(ctx, tx, req.GrantID)
		case errors.Is(err, domain.ErrForbidden):
			refused = errInvalidGrant
			return nil
		case err != nil:
			return err
		}
		// Every check runs after the claim, so a code presented wrongly is
		// spent as well: it can never be retried with other guesses.
		if !now.Before(req.CodeExpiresAt) ||
			subtle.ConstantTimeCompare([]byte(in.ClientID), []byte(req.ClientID)) != 1 ||
			in.RedirectURI != req.RedirectURI ||
			subtle.ConstantTimeCompare([]byte(pkceChallenge(in.CodeVerifier)), []byte(req.CodeChallenge)) != 1 {
			refused = errInvalidGrant
			return nil
		}
		owner, err := hostOwner(ctx, tx, s.clients)
		if err != nil {
			return err
		}
		// The grant's lifetime started when the owner approved it; a slow
		// browser or client cannot stretch it.
		grantExpires := req.GrantExpiresAt
		if !now.Before(grantExpires) {
			refused = errInvalidGrant
			return nil
		}
		access, err := newBearer()
		if err != nil {
			return err
		}
		refresh, err := newBearer()
		if err != nil {
			return err
		}
		accessExpires := accessExpiry(now, grantExpires)
		clientID := domain.OAuthClientPrefix + grantID
		p := domain.Principal{ClientID: clientID, OwnerID: owner.OwnerID, Role: domain.RoleAgent, ProjectIDs: slices.Clone(req.Granted.Projects), Capabilities: slices.Clone(req.Granted.Capabilities), Resource: domain.OAuthResourceMCP}
		// The grant goes first: if its registration is gone, the code is
		// spent and nothing else is created.
		switch err := s.grants.AddGrant(ctx, tx, domain.OAuthGrant{ID: grantID, ClientID: clientID, RegistrationID: req.ClientID, RedirectURI: req.RedirectURI, RefreshDigest: TokenDigest(refresh), CreatedAt: now, ExpiresAt: grantExpires}); {
		case errors.Is(err, domain.ErrForbidden):
			refused = errInvalidGrant
			return nil
		case err != nil:
			return err
		}
		if err := s.clients.PutClient(ctx, tx, ClientRecord{Principal: p, TokenDigest: TokenDigest(access), ExpiresAt: &accessExpires}); err != nil {
			return err
		}
		tokens = OAuthTokens{AccessToken: access, ExpiresIn: int64(accessExpires.Sub(now) / time.Second), RefreshToken: refresh, Scope: req.Granted.String()}
		return nil
	})
	if err != nil {
		return OAuthTokens{}, err
	}
	if refused != nil {
		return OAuthTokens{}, refused
	}
	return tokens, nil
}

func (s *OAuthService) refresh(ctx context.Context, in OAuthTokenInput) (OAuthTokens, error) {
	if len(in.RefreshToken) != 64 {
		return OAuthTokens{}, errInvalidGrant
	}
	now := s.now().UTC()
	var tokens OAuthTokens
	var refused error
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		old := TokenDigest(in.RefreshToken)
		g, err := s.grants.FindGrantByRefresh(ctx, tx, old)
		switch {
		case errors.Is(err, domain.ErrConflict):
			// A refresh token used twice means a copy is in someone
			// else's hands: end the grant (RFC 9700 §4.14).
			refused = errInvalidGrant
			return s.revokeGrant(ctx, tx, g)
		case errors.Is(err, domain.ErrForbidden):
			refused = errInvalidGrant
			return nil
		case err != nil:
			return err
		}
		if subtle.ConstantTimeCompare([]byte(in.ClientID), []byte(g.RegistrationID)) != 1 {
			refused = errInvalidGrant
			return nil
		}
		if !now.Before(g.ExpiresAt) {
			refused = errInvalidGrant
			return s.revokeGrant(ctx, tx, g)
		}
		access, err := newBearer()
		if err != nil {
			return err
		}
		refresh, err := newBearer()
		if err != nil {
			return err
		}
		if err := s.grants.RotateRefresh(ctx, tx, g.ID, old, TokenDigest(refresh)); err != nil {
			if errors.Is(err, domain.ErrConflict) {
				refused = errInvalidGrant
				return nil
			}
			return err
		}
		accessExpires := accessExpiry(now, g.ExpiresAt)
		if err := s.clients.RotateClient(ctx, tx, g.ClientID, TokenDigest(access), &accessExpires); err != nil {
			if errors.Is(err, domain.ErrForbidden) {
				// The owner revoked the client on the host: the grant is over.
				refused = errInvalidGrant
				return s.grants.DeleteGrant(ctx, tx, g.ID)
			}
			return err
		}
		tokens = OAuthTokens{AccessToken: access, ExpiresIn: int64(accessExpires.Sub(now) / time.Second), RefreshToken: refresh}
		return nil
	})
	if err != nil {
		return OAuthTokens{}, err
	}
	if refused != nil {
		return OAuthTokens{}, refused
	}
	return tokens, nil
}

// Revoke ends the grant behind an access or refresh token issued to
// clientID (RFC 7009). An unknown token, or one issued to another client,
// changes nothing and is not an error, so revocation reveals nothing.
func (s *OAuthService) Revoke(ctx context.Context, clientID, token string) error {
	if s.issuer == "" || s.clients == nil || s.grants == nil {
		return domain.ErrUnavailable
	}
	if clientID == "" || token == "" {
		return tokenError("invalid_request", "client_id and token are required.")
	}
	if len(token) != 64 {
		return nil
	}
	return s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		digest := TokenDigest(token)
		g, err := s.grants.FindGrantByRefresh(ctx, tx, digest)
		if errors.Is(err, domain.ErrForbidden) {
			record, findErr := s.clients.FindToken(ctx, tx, digest)
			if findErr != nil || !strings.HasPrefix(record.Principal.ClientID, domain.OAuthClientPrefix) {
				return nil
			}
			g, err = s.grants.FindGrantByClient(ctx, tx, record.Principal.ClientID)
		}
		switch {
		case err == nil, errors.Is(err, domain.ErrConflict):
		case errors.Is(err, domain.ErrForbidden):
			return nil
		default:
			return err
		}
		if subtle.ConstantTimeCompare([]byte(clientID), []byte(g.RegistrationID)) != 1 {
			return nil
		}
		return s.revokeGrant(ctx, tx, g)
	})
}

// revokeGrant revokes the grant's client record, so its access token fails on
// the next request, and deletes the grant, so its refresh token is unknown.
func (s *OAuthService) revokeGrant(ctx context.Context, tx Tx, g domain.OAuthGrant) error {
	if err := s.clients.RevokeClient(ctx, tx, g.ClientID); err != nil && !errors.Is(err, domain.ErrForbidden) {
		return err
	}
	return s.grants.DeleteGrant(ctx, tx, g.ID)
}

func (s *OAuthService) revokeGrantByID(ctx context.Context, tx Tx, grantID string) error {
	if grantID == "" {
		return nil
	}
	clientID := domain.OAuthClientPrefix + grantID
	g, err := s.grants.FindGrantByClient(ctx, tx, clientID)
	if errors.Is(err, domain.ErrForbidden) {
		// The grant is gone already; make sure its client is too.
		if err := s.clients.RevokeClient(ctx, tx, clientID); err != nil && !errors.Is(err, domain.ErrForbidden) {
			return err
		}
		return nil
	}
	if err != nil {
		return err
	}
	return s.revokeGrant(ctx, tx, g)
}

// pruneGrants ends a few expired grants on each exchange. Their access tokens
// already stopped working at the grant's expiry; this removes what is left.
func (s *OAuthService) pruneGrants(ctx context.Context, tx Tx, now time.Time) error {
	expired, err := s.grants.ExpiredGrants(ctx, tx, now, oauthGrantPruneBatch)
	if err != nil {
		return err
	}
	for _, g := range expired {
		if err := s.revokeGrant(ctx, tx, g); err != nil {
			return err
		}
	}
	return nil
}

// accessExpiry is one hour from now, or the grant's own expiry if sooner, so
// refreshing can never carry access past the grant.
func accessExpiry(now, grantExpires time.Time) time.Time {
	if at := now.Add(domain.OAuthAccessTokenTTL); at.Before(grantExpires) {
		return at
	}
	return grantExpires
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// validCodeVerifier reports whether v is a PKCE code verifier: 43 to 128
// characters from the unreserved set (RFC 7636 §4.1).
func validCodeVerifier(v string) bool {
	if len(v) < 43 || len(v) > 128 {
		return false
	}
	for _, r := range v {
		if !unreserved(r) {
			return false
		}
	}
	return true
}

func unreserved(r rune) bool {
	return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("-._~", r)
}

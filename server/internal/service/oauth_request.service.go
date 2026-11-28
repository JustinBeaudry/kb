package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/url"
	"slices"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// OAuthResourcePath is the one resource an OAuth grant can name.
const OAuthResourcePath = "/mcp"

const (
	oauthRequestPruneBatch = 100
	oauthRequestKeep       = time.Hour
	maxListedOAuthRequests = 100
)

// OAuthAuthorizeInput is an authorization request's query parameters.
type OAuthAuthorizeInput struct {
	ClientID, RedirectURI, ResponseType, CodeChallenge, CodeChallengeMethod, State, Resource, Scope string
}

// OAuthRedirectError is an error the client learns at its redirect URI
// (RFC 6749 §4.1.2.1). Every other Authorize error is shown on kb's own page
// and never redirected, because the redirect URI is not yet known to be the
// client's.
type OAuthRedirectError struct{ Code, Description string }

func (e *OAuthRedirectError) Error() string { return "oauth " + e.Code }

// OAuthAuthorization is a stored grant request and the cookie that binds it to
// the browser that started it. The cookie is returned only here.
type OAuthAuthorization struct {
	Request domain.OAuthRequest
	Cookie  string
}

// OAuthPoll is what the approval page learns: still pending, or where to send
// the browser.
type OAuthPoll struct {
	Pending  bool
	Redirect string
}

// Issuer is kb's public origin, empty when grants are disabled.
func (s *OAuthService) Issuer() string { return s.issuer }

// Authorize validates an authorization request and stores it for the owner to
// decide. The client and redirect URI are checked first; until both match a
// registration, errors are domain.ErrUnauthorized and must not be redirected.
func (s *OAuthService) Authorize(ctx context.Context, in OAuthAuthorizeInput) (OAuthAuthorization, error) {
	if s.issuer == "" {
		return OAuthAuthorization{}, domain.ErrUnavailable
	}
	now := s.now().UTC()
	var reg domain.OAuthRegistration
	var err error
	if domain.IsClientMetadataID(in.ClientID) {
		reg, err = s.metadataRegistration(ctx, in.ClientID, in.RedirectURI, now)
	} else {
		err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) (err error) {
			reg, err = s.repo.FindRegistration(ctx, tx, in.ClientID, now)
			return err
		})
	}
	if err != nil {
		return OAuthAuthorization{}, err
	}
	if in.RedirectURI == "" || !slices.Contains(reg.RedirectURIs, in.RedirectURI) {
		return OAuthAuthorization{}, domain.ErrUnauthorized
	}
	if in.ResponseType != "code" {
		return OAuthAuthorization{}, &OAuthRedirectError{"unsupported_response_type", "kb supports response_type=code only."}
	}
	if !domain.ValidOAuthState(in.State) {
		return OAuthAuthorization{}, &OAuthRedirectError{"invalid_request", "state is required: up to 512 printable ASCII characters."}
	}
	if in.CodeChallengeMethod != "S256" || !domain.ValidCodeChallenge(in.CodeChallenge) {
		return OAuthAuthorization{}, &OAuthRedirectError{"invalid_request", "PKCE is required: code_challenge_method=S256 with a 43-character code_challenge."}
	}
	if in.Resource != s.issuer+OAuthResourcePath {
		return OAuthAuthorization{}, &OAuthRedirectError{"invalid_target", "resource must be kb's MCP endpoint."}
	}
	scope, err := domain.ParseOAuthScope(in.Scope)
	if err != nil {
		return OAuthAuthorization{}, &OAuthRedirectError{"invalid_scope", "scope holds project:<id> entries and agent capabilities only."}
	}
	cookie := rand.Text()
	req := domain.OAuthRequest{
		ID: "or_" + rand.Text(), ClientID: reg.ID, ClientName: reg.ClientName, RedirectURI: in.RedirectURI,
		State: in.State, CodeChallenge: in.CodeChallenge, Requested: scope, Status: domain.OAuthRequestPending,
		CookieDigest: oauthDigest(cookie), CreatedAt: now, ExpiresAt: now.Add(domain.OAuthRequestTTL),
	}
	err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := s.repo.PruneRequests(ctx, tx, now.Add(-oauthRequestKeep), oauthRequestPruneBatch); err != nil {
			return err
		}
		for range 5 {
			req.ApprovalCode = newApprovalCode()
			err := s.repo.AddRequest(ctx, tx, req, domain.MaxPendingOAuthRequests)
			if !errors.Is(err, domain.ErrConflict) {
				return err
			}
		}
		return domain.ErrUnavailable
	})
	if errors.Is(err, domain.ErrBudget) {
		return OAuthAuthorization{}, &OAuthRedirectError{"temporarily_unavailable", "Too many requests are waiting for the owner. Try again later."}
	}
	if err != nil {
		return OAuthAuthorization{}, err
	}
	return OAuthAuthorization{Request: req, Cookie: cookie}, nil
}

// Poll answers the browser that started a request, identified by its cookie.
// Once the owner decides, or the request expires, it returns the redirect for
// the browser exactly once: with a fresh authorization code, or with
// error=access_denied. Anyone else, or a second ask, gets ErrForbidden.
func (s *OAuthService) Poll(ctx context.Context, id, cookie string) (OAuthPoll, error) {
	if s.issuer == "" {
		return OAuthPoll{}, domain.ErrUnavailable
	}
	now := s.now().UTC()
	var poll OAuthPoll
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		req, err := s.repo.FindRequest(ctx, tx, id)
		if err != nil {
			return err
		}
		if cookie == "" || subtle.ConstantTimeCompare([]byte(oauthDigest(cookie)), []byte(req.CookieDigest)) != 1 || req.Released {
			return domain.ErrForbidden
		}
		params := url.Values{"state": {req.State}, "iss": {s.issuer}}
		switch {
		case req.Status == domain.OAuthRequestApproved:
			code := rand.Text() + rand.Text()
			if err := s.repo.ReleaseRequest(ctx, tx, req.ID, oauthDigest(code), now.Add(domain.OAuthCodeTTL)); err != nil {
				return err
			}
			params.Set("code", code)
		case req.Status == domain.OAuthRequestDenied || !now.Before(req.ExpiresAt):
			if err := s.repo.ReleaseRequest(ctx, tx, req.ID, "", time.Time{}); err != nil {
				return err
			}
			params.Set("error", "access_denied")
		default:
			poll = OAuthPoll{Pending: true}
			return nil
		}
		redirect, err := withQuery(req.RedirectURI, params)
		poll = OAuthPoll{Redirect: redirect}
		return err
	})
	if errors.Is(err, domain.ErrConflict) {
		return OAuthPoll{}, domain.ErrForbidden
	}
	return poll, err
}

// ListRequestsHost lists requests waiting for the owner, on the service host.
func (s *OAuthService) ListRequestsHost(ctx context.Context) ([]domain.OAuthRequest, error) {
	var out []domain.OAuthRequest
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) (err error) {
		if _, err := s.hostOwner(ctx, tx); err != nil {
			return err
		}
		out, err = s.repo.ListPendingRequests(ctx, tx, s.now().UTC(), maxListedOAuthRequests)
		return err
	})
	return out, err
}

// ApproveHost grants the request holding code, on the service host. The grant
// must name at least one project and capability, may narrow the request and
// never widen it, and lives at most MaxOAuthGrantTTL; zero means the default.
func (s *OAuthService) ApproveHost(ctx context.Context, code string, grant domain.OAuthScope, ttl time.Duration) (domain.OAuthRequest, error) {
	if ttl == 0 {
		ttl = domain.DefaultOAuthGrantTTL
	}
	if ttl < 0 || ttl > domain.MaxOAuthGrantTTL || len(grant.Projects) == 0 || len(grant.Projects) > 50 || len(grant.Capabilities) == 0 {
		return domain.OAuthRequest{}, domain.ErrInvalidInput
	}
	parsed, err := domain.ParseOAuthScope(grant.String())
	// The round trip must give back exactly what the owner named: an element
	// that splits into several scope tokens, or a duplicate, is refused.
	if err != nil || !slices.Equal(parsed.Projects, grant.Projects) || !slices.Equal(parsed.Capabilities, grant.Capabilities) {
		return domain.OAuthRequest{}, domain.ErrInvalidInput
	}
	return s.decideHost(ctx, code, domain.OAuthRequestApproved, parsed, ttl)
}

// DenyHost refuses the request holding code, on the service host.
func (s *OAuthService) DenyHost(ctx context.Context, code string) (domain.OAuthRequest, error) {
	return s.decideHost(ctx, code, domain.OAuthRequestDenied, domain.OAuthScope{}, 0)
}

func (s *OAuthService) decideHost(ctx context.Context, raw string, status domain.OAuthRequestStatus, grant domain.OAuthScope, ttl time.Duration) (domain.OAuthRequest, error) {
	code, ok := domain.NormalizeApprovalCode(raw)
	if !ok {
		return domain.OAuthRequest{}, domain.ErrInvalidInput
	}
	now := s.now().UTC()
	var req domain.OAuthRequest
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := s.hostOwner(ctx, tx); err != nil {
			return err
		}
		var err error
		req, err = s.repo.FindPendingRequest(ctx, tx, code, now)
		if err != nil {
			return err
		}
		if status == domain.OAuthRequestApproved && !req.Requested.Within(grant) {
			return domain.ErrForbidden
		}
		if err := s.repo.DecideRequest(ctx, tx, req.ID, status, grant, ttl, now); err != nil {
			return err
		}
		req.Status, req.Granted, req.GrantTTL = status, grant, ttl
		return nil
	})
	if err != nil {
		return domain.OAuthRequest{}, err
	}
	return req, nil
}

func (s *OAuthService) hostOwner(ctx context.Context, tx Tx) (domain.Principal, error) {
	if s.owners == nil {
		return domain.Principal{}, domain.ErrUnavailable
	}
	return hostOwner(ctx, tx, s.owners)
}

func oauthDigest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func newApprovalCode() string {
	alphabet := domain.OAuthApprovalCodeAlphabet
	out := make([]byte, domain.OAuthApprovalCodeLength)
	var b [1]byte
	for i := range out {
		for {
			_, _ = rand.Read(b[:])
			// Reject the top of the byte range so every letter is equally likely.
			if int(b[0]) < 256-256%len(alphabet) {
				out[i] = alphabet[int(b[0])%len(alphabet)]
				break
			}
		}
	}
	return string(out)
}

func withQuery(base string, params url.Values) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", domain.ErrUnavailable
	}
	q := u.Query()
	for k, v := range params {
		q[k] = v
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

package domain

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxOAuthRedirectURIs bounds the redirect URIs one registration may hold.
	MaxOAuthRedirectURIs = 5
	// MaxOAuthRedirectURILength bounds one redirect URI.
	MaxOAuthRedirectURILength = 512
	// MaxOAuthClientNameLength bounds a client's self-reported name, in runes.
	MaxOAuthClientNameLength = 100
	// OAuthRegistrationTTL is how long a registration lives unless a grant is
	// approved for it.
	OAuthRegistrationTTL = 24 * time.Hour
	// MaxPendingOAuthRegistrations bounds unapproved registrations, so
	// anonymous registration cannot grow the graph without limit.
	MaxPendingOAuthRegistrations = 256
)

var (
	// ErrInvalidRedirectURI marks a redirect URI kb will not register.
	ErrInvalidRedirectURI = fmt.Errorf("%w: redirect uri", ErrInvalidInput)
	// ErrInvalidClientMetadata marks other registration metadata kb refuses.
	ErrInvalidClientMetadata = fmt.Errorf("%w: client metadata", ErrInvalidInput)
)

// OAuthRegistration is a public OAuth client registered dynamically (RFC
// 7591). Nothing it says about itself is trusted: the name is shown to the
// owner as the client's claim, never as its identity.
type OAuthRegistration struct {
	ID           string
	RedirectURIs []string
	ClientName   string
	CreatedAt    time.Time
	ExpiresAt    time.Time
}

// CanonicalRedirectURI returns raw if kb accepts it as a redirect URI: an
// absolute https URI, or an http URI on a loopback IP literal for native apps
// (RFC 8252 §7.3), with no user info or fragment. Redirect URIs are compared
// exactly, so the accepted form is the one the client sent.
func CanonicalRedirectURI(raw string) (string, error) {
	if raw == "" || len(raw) > MaxOAuthRedirectURILength || strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\x00\r\n\t ") {
		return "", ErrInvalidRedirectURI
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Opaque != "" || u.User != nil || u.Fragment != "" || u.RawFragment != "" || strings.Contains(raw, "#") || u.Host == "" {
		return "", ErrInvalidRedirectURI
	}
	switch u.Scheme {
	case "https":
		if u.Hostname() == "" {
			return "", ErrInvalidRedirectURI
		}
	case "http":
		ip := net.ParseIP(u.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return "", ErrInvalidRedirectURI
		}
	default:
		return "", ErrInvalidRedirectURI
	}
	return raw, nil
}

// CanonicalClientName trims a self-reported client name and refuses one that
// is too long or holds characters that could disguise it on the approval page.
func CanonicalClientName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxOAuthClientNameLength {
		return "", ErrInvalidClientMetadata
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return "", ErrInvalidClientMetadata
		}
	}
	return name, nil
}

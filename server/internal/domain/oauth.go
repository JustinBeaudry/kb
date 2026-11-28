package domain

import (
	"fmt"
	"net"
	"net/url"
<<<<<<< HEAD
	"slices"
=======
>>>>>>> origin/main
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
<<<<<<< HEAD

const (
	// OAuthRequestTTL is how long the owner has to approve a grant request.
	OAuthRequestTTL = 10 * time.Minute
	// OAuthCodeTTL is how long an authorization code can be exchanged.
	OAuthCodeTTL = time.Minute
	// MaxPendingOAuthRequests bounds grant requests waiting for the owner.
	MaxPendingOAuthRequests = 64
	// MaxOAuthGrantTTL is the longest a grant may live before the owner must
	// approve it again.
	MaxOAuthGrantTTL = 90 * 24 * time.Hour
	// DefaultOAuthGrantTTL is used when the owner names no lifetime.
	DefaultOAuthGrantTTL = 30 * 24 * time.Hour
	// MaxOAuthStateLength bounds the client's opaque state.
	MaxOAuthStateLength = 512
	// OAuthApprovalCodeAlphabet has no characters that look alike (0/O, 1/I/L).
	OAuthApprovalCodeAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"
	// OAuthApprovalCodeLength is the length of the code the owner types.
	OAuthApprovalCodeLength = 8
	// OAuthProjectScopePrefix marks a project in an OAuth scope string.
	OAuthProjectScopePrefix = "project:"
)

// OAuthGrantCapabilities are the capabilities an OAuth grant may carry: those
// of an agent client, and nothing an owner, collector or verifier holds.
var OAuthGrantCapabilities = []string{"policy:read", "status:read", "retrieval:read", "evidence:read", "candidate:write", "checkpoint:write", "memory:export"}

// OAuthRequestStatus is where a grant request stands.
type OAuthRequestStatus string

const (
	OAuthRequestPending  OAuthRequestStatus = "pending"
	OAuthRequestApproved OAuthRequestStatus = "approved"
	OAuthRequestDenied   OAuthRequestStatus = "denied"
)

// OAuthScope is what a client asks for, or what the owner grants: projects and
// capabilities. A request may name neither, leaving the choice to the owner.
type OAuthScope struct {
	Projects     []string `json:"projects"`
	Capabilities []string `json:"capabilities"`
}

// OAuthRequest is one authorization request waiting for, or decided by, the
// owner. The browser cookie and the authorization code are kept only as
// SHA-256 digests.
type OAuthRequest struct {
	ID            string             `json:"id"`
	ApprovalCode  string             `json:"approval_code"`
	ClientID      string             `json:"client_id"`
	ClientName    string             `json:"client_name"`
	RedirectURI   string             `json:"redirect_uri"`
	State         string             `json:"-"`
	CodeChallenge string             `json:"-"`
	Requested     OAuthScope         `json:"requested"`
	Status        OAuthRequestStatus `json:"status"`
	Granted       OAuthScope         `json:"granted"`
	GrantTTL      time.Duration      `json:"-"`
	CookieDigest  string             `json:"-"`
	CodeDigest    string             `json:"-"`
	CodeExpiresAt time.Time          `json:"-"`
	Released      bool               `json:"-"`
	CreatedAt     time.Time          `json:"created_at"`
	ExpiresAt     time.Time          `json:"expires_at"`
}

// ParseOAuthScope reads a space-separated scope: project:<id> tokens and
// capability names. An empty scope asks for nothing in particular.
func ParseOAuthScope(raw string) (OAuthScope, error) {
	scope := OAuthScope{Projects: []string{}, Capabilities: []string{}}
	if len(raw) > 2048 {
		return OAuthScope{}, ErrInvalidInput
	}
	for token := range strings.FieldsSeq(raw) {
		if project, ok := strings.CutPrefix(token, OAuthProjectScopePrefix); ok {
			if !validScopeProject(project) {
				return OAuthScope{}, ErrInvalidInput
			}
			if !slices.Contains(scope.Projects, project) {
				scope.Projects = append(scope.Projects, project)
			}
			continue
		}
		if !slices.Contains(OAuthGrantCapabilities, token) {
			return OAuthScope{}, ErrInvalidInput
		}
		if !slices.Contains(scope.Capabilities, token) {
			scope.Capabilities = append(scope.Capabilities, token)
		}
	}
	if len(scope.Projects) > 50 {
		return OAuthScope{}, ErrInvalidInput
	}
	return scope, nil
}

// String renders the scope in the form ParseOAuthScope reads.
func (s OAuthScope) String() string {
	tokens := make([]string, 0, len(s.Projects)+len(s.Capabilities))
	for _, p := range s.Projects {
		tokens = append(tokens, OAuthProjectScopePrefix+p)
	}
	return strings.Join(append(tokens, s.Capabilities...), " ")
}

// Within reports whether grant asks for nothing beyond s. An empty side of the
// request leaves that side to the owner.
func (s OAuthScope) Within(grant OAuthScope) bool {
	for _, p := range grant.Projects {
		if len(s.Projects) > 0 && !slices.Contains(s.Projects, p) {
			return false
		}
	}
	for _, c := range grant.Capabilities {
		if !slices.Contains(OAuthGrantCapabilities, c) || (len(s.Capabilities) > 0 && !slices.Contains(s.Capabilities, c)) {
			return false
		}
	}
	return true
}

// ValidCodeChallenge reports whether c is a PKCE S256 challenge: the
// unpadded base64url encoding of a SHA-256 digest (RFC 7636 §4.2).
func ValidCodeChallenge(c string) bool {
	if len(c) != 43 {
		return false
	}
	for _, r := range c {
		if !alnumOr(r, "-_") {
			return false
		}
	}
	return true
}

// ValidOAuthState reports whether a client's state can be echoed back safely.
func ValidOAuthState(s string) bool {
	if s == "" || len(s) > MaxOAuthStateLength {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r > 0x7e {
			return false
		}
	}
	return true
}

// NormalizeApprovalCode uppercases a typed approval code and drops spaces and
// dashes, and reports whether the result is a well-formed code.
func NormalizeApprovalCode(raw string) (string, bool) {
	code := strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' {
			return -1
		}
		return unicode.ToUpper(r)
	}, raw)
	if len(code) != OAuthApprovalCodeLength {
		return "", false
	}
	for _, r := range code {
		if !strings.ContainsRune(OAuthApprovalCodeAlphabet, r) {
			return "", false
		}
	}
	return code, true
}

// validScopeProject accepts the project IDs a scope may name: short, and made
// only of letters, digits, dot, dash and underscore, so they render plainly on
// the approval page.
func validScopeProject(p string) bool {
	if p == "" || len(p) > 128 {
		return false
	}
	for _, r := range p {
		if !alnumOr(r, ".-_") {
			return false
		}
	}
	return true
}

// alnumOr reports whether r is an ASCII letter or digit, or one of extra.
func alnumOr(r rune, extra string) bool {
	return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune(extra, r)
}
=======
>>>>>>> origin/main

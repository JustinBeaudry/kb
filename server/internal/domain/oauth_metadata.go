package domain

import (
	"net"
	"net/url"
	"strings"
	"time"
)

const (
	// MaxOAuthClientIDURLLength bounds a client ID metadata document URL.
	MaxOAuthClientIDURLLength = 512
	// MaxOAuthClientMetadataBytes bounds a fetched client ID metadata document.
	MaxOAuthClientMetadataBytes = 8 << 10
	// MaxOAuthMetadataRedirectURIs bounds the redirect URIs kb reads from a
	// client ID metadata document. Published clients list more than a
	// dynamic registration usually does.
	MaxOAuthMetadataRedirectURIs = 10
	// OAuthClientMetadataTimeout bounds one metadata document fetch.
	OAuthClientMetadataTimeout = 5 * time.Second
)

// IsClientMetadataID reports whether a client_id names a client ID metadata
// document (draft-ietf-oauth-client-id-metadata-document) rather than a
// dynamic registration. Dynamic registrations never start with https://.
func IsClientMetadataID(id string) bool { return strings.HasPrefix(id, "https://") }

// ValidClientMetadataURL reports whether raw is a client ID kb will fetch: an
// https URL on the default port with a DNS host name and a path, and with no
// user info, query, fragment or dot segments. The URL must already be in the
// form kb would write it, so one client cannot appear under two IDs.
func ValidClientMetadataURL(raw string) bool {
	if len(raw) > MaxOAuthClientIDURLLength || strings.ContainsAny(raw, "\x00\r\n\t #\\") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || u.Port() != "" || strings.HasSuffix(u.Host, ":") {
		return false
	}
	host := u.Hostname()
	if host == "" || host != strings.ToLower(host) || strings.HasSuffix(host, ".") || !strings.Contains(host, ".") || net.ParseIP(host) != nil {
		return false
	}
	if u.Path == "" || u.Path == "/" {
		return false
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return u.String() == raw
}

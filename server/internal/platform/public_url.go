package platform

import (
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// LoadPublicURL returns the origin clients use to reach this api, such as
// https://kb.example.com, for OAuth discovery metadata. KB_PUBLIC_URL wins;
// otherwise Railway's RAILWAY_PUBLIC_DOMAIN, which the platform sets, is used
// over https. Both are operator settings: the origin is never taken from a
// request's Host or forwarded headers. Empty means discovery is off.
func LoadPublicURL() (string, error) {
	if raw := os.Getenv("KB_PUBLIC_URL"); raw != "" {
		return PublicOrigin(raw)
	}
	if domain := os.Getenv("RAILWAY_PUBLIC_DOMAIN"); domain != "" {
		return PublicOrigin("https://" + domain)
	}
	return "", nil
}

// PublicOrigin accepts an https origin, or an http origin on a loopback
// address for local use, with no user info, path, query or fragment, and
// returns it in canonical form.
func PublicOrigin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSuffix(raw, "/"))
	if err != nil || u.Host == "" || u.Opaque != "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || len(raw) > 255 {
		return "", domain.ErrInvalidInput
	}
	host := u.Hostname()
	if host == "" || strings.ContainsAny(host, "\"\\ ") {
		return "", domain.ErrInvalidInput
	}
	switch u.Scheme {
	case "https":
	case "http":
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return "", domain.ErrInvalidInput
		}
	default:
		return "", domain.ErrInvalidInput
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}

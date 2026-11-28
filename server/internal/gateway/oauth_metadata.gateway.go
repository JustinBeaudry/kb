package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// ClientMetadataFetcher fetches OAuth client ID metadata documents. Because
// anyone can make kb fetch a URL by naming it as a client_id, it only connects
// to public unicast addresses (checked on the address actually dialed, after
// DNS, so a name that resolves to an internal address is refused), never uses
// a proxy, follows no redirects, sends no cookies or credentials, and reads at
// most domain.MaxOAuthClientMetadataBytes within domain.OAuthClientMetadataTimeout.
type ClientMetadataFetcher struct {
	client *http.Client
}

// NewClientMetadataFetcher returns a fetcher that dials public addresses only.
func NewClientMetadataFetcher() *ClientMetadataFetcher {
	return newClientMetadataFetcher(PublicAddress, nil)
}

func newClientMetadataFetcher(allow func(netip.Addr) bool, roots *tls.Config) *ClientMetadataFetcher {
	dialer := &net.Dialer{
		Timeout: domain.OAuthClientMetadataTimeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || !allow(ap.Addr().Unmap()) {
				return errNotPublic
			}
			return nil
		},
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if roots != nil {
		tlsConfig = roots.Clone()
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            dialer.DialContext,
		TLSClientConfig:        tlsConfig,
		TLSHandshakeTimeout:    domain.OAuthClientMetadataTimeout,
		ResponseHeaderTimeout:  domain.OAuthClientMetadataTimeout,
		MaxResponseHeaderBytes: 16 << 10,
		MaxIdleConns:           4,
		IdleConnTimeout:        30 * time.Second,
		DisableCompression:     true,
		ForceAttemptHTTP2:      true,
	}
	return &ClientMetadataFetcher{client: &http.Client{
		Transport:     transport,
		Timeout:       domain.OAuthClientMetadataTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errRedirect },
	}}
}

var (
	errNotPublic       = errors.New("client metadata: address is not public")
	errRedirect        = errors.New("client metadata: redirects are not followed")
	errMetadataRefused = errors.New("client metadata: document refused")
)

// FetchClientMetadata implements service.OAuthClientMetadataFetcher.
func (f *ClientMetadataFetcher) FetchClientMetadata(ctx context.Context, clientID string) ([]byte, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clientID, nil)
	if err != nil || req.URL.Scheme != "https" {
		return nil, 0, errMetadataRefused
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kb-oauth")
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %w", errMetadataRefused, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, errMetadataRefused
	}
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || (media != "application/json" && !strings.HasSuffix(media, "+json")) {
		return nil, 0, errMetadataRefused
	}
	if resp.ContentLength > domain.MaxOAuthClientMetadataBytes {
		return nil, 0, errMetadataRefused
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, domain.MaxOAuthClientMetadataBytes+1))
	if err != nil || len(body) > domain.MaxOAuthClientMetadataBytes {
		return nil, 0, errMetadataRefused
	}
	return body, cacheLifetime(resp.Header), nil
}

// cacheLifetime reads max-age from Cache-Control. no-store and no-cache ask
// for no reuse, which the caller still bounds below to keep fetches rare.
func cacheLifetime(h http.Header) time.Duration {
	lifetime := time.Duration(0)
	for _, directive := range strings.Split(h.Get("Cache-Control"), ",") {
		name, value, _ := strings.Cut(strings.TrimSpace(directive), "=")
		switch strings.ToLower(name) {
		case "no-store", "no-cache":
			return time.Nanosecond
		case "max-age":
			if seconds, err := strconv.ParseInt(strings.Trim(value, `"`), 10, 64); err == nil && seconds >= 0 {
				lifetime = time.Duration(min(seconds, int64(365*24*time.Hour/time.Second))) * time.Second
				if lifetime == 0 {
					lifetime = time.Nanosecond
				}
			}
		}
	}
	return lifetime
}

var nonPublicPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/3",
		"::/128", "::1/128", "::ffff:0:0/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23",
		"2001:db8::/32", "2002::/16", "3fff::/20", "5f00::/16", "fc00::/7", "fe80::/10", "ff00::/8",
	} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

// PublicAddress reports whether addr is a globally routable unicast address,
// excluding private, shared, loopback, link-local, documentation, benchmark,
// multicast, reserved and translation ranges (which could reach an internal
// IPv4 address through a gateway).
func PublicAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsPrivate() {
		return false
	}
	for _, p := range nonPublicPrefixes {
		if p.Contains(addr) {
			return false
		}
	}
	return true
}

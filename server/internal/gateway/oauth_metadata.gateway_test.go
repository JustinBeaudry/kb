package gateway

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func metadataServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *tls.Config) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return srv, &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

func allowAll(netip.Addr) bool { return true }

func TestClientMetadataFetcherReadsADocumentAndItsLifetime(t *testing.T) {
	var got *http.Request
	srv, roots := metadataServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=600")
		_, _ = w.Write([]byte(`{"client_id":"x"}`))
	})
	body, maxAge, err := newClientMetadataFetcher(allowAll, roots).FetchClientMetadata(t.Context(), srv.URL+"/client.json")
	require.NoError(t, err)
	assert.JSONEq(t, `{"client_id":"x"}`, string(body))
	assert.Equal(t, 10*time.Minute, maxAge)
	assert.Equal(t, "application/json", got.Header.Get("Accept"))
	assert.Empty(t, got.Header.Get("Cookie"))
	assert.Empty(t, got.Header.Get("Authorization"))
}

func TestClientMetadataFetcherRefusesWhatIsNotAPlainDocument(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"redirect": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		},
		"not found": func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
		"html": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("{}"))
		},
		"declared too large": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Length", "100000")
			_, _ = w.Write([]byte("{}"))
		},
		"streamed too large": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.(http.Flusher).Flush()
			_, _ = w.Write([]byte(strings.Repeat(" ", domain.MaxOAuthClientMetadataBytes+1)))
		},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			srv, roots := metadataServer(t, h)
			_, _, err := newClientMetadataFetcher(allowAll, roots).FetchClientMetadata(t.Context(), srv.URL+"/client.json")
			assert.ErrorIs(t, err, errMetadataRefused)
		})
	}
}

func TestClientMetadataFetcherNeverDialsANonPublicAddress(t *testing.T) {
	called := false
	srv, roots := metadataServer(t, func(w http.ResponseWriter, r *http.Request) { called = true })
	_, _, err := newClientMetadataFetcher(PublicAddress, roots).FetchClientMetadata(t.Context(), srv.URL+"/client.json")
	require.Error(t, err)
	assert.False(t, called, "a loopback test server is never reached")

	_, _, err = NewClientMetadataFetcher().FetchClientMetadata(t.Context(), "http://app.example/client.json")
	assert.ErrorIs(t, err, errMetadataRefused, "plain http is never fetched")
}

func TestPublicAddressExcludesInternalAndSpecialRanges(t *testing.T) {
	for _, public := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111", "::ffff:8.8.8.8"} {
		assert.True(t, PublicAddress(netip.MustParseAddr(public)), public)
	}
	for _, internal := range []string{
		"0.0.0.0", "10.1.2.3", "100.64.0.1", "127.0.0.1", "169.254.169.254", "172.16.0.1", "192.0.0.8", "192.0.2.1",
		"192.168.1.1", "198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "240.0.0.1", "255.255.255.255",
		"::", "::1", "::ffff:10.0.0.1", "::ffff:127.0.0.1", "64:ff9b::a00:1", "2001:db8::1", "2002:a00:1::1",
		"fc00::1", "fd12:3456::1", "fe80::1", "fec0::1", "4000::1", "ff02::1",
	} {
		assert.False(t, PublicAddress(netip.MustParseAddr(internal)), internal)
	}
}

func TestCacheLifetimeReadsMaxAgeAndRefusals(t *testing.T) {
	for header, want := range map[string]time.Duration{
		"":                       0,
		"max-age=300":            5 * time.Minute,
		"public, Max-Age=60":     time.Minute,
		`max-age="120"`:          2 * time.Minute,
		"max-age=0":              time.Nanosecond,
		"no-store":               time.Nanosecond,
		"max-age=600, no-cache":  time.Nanosecond,
		"max-age=-5":             0,
		"max-age=99999999999999": 365 * 24 * time.Hour,
	} {
		h := http.Header{}
		h.Set("Cache-Control", header)
		assert.Equal(t, want, cacheLifetime(h), header)
	}
	h := http.Header{}
	h.Add("Cache-Control", "max-age=86400")
	h.Add("Cache-Control", "no-store")
	assert.Equal(t, time.Nanosecond, cacheLifetime(h), "every Cache-Control field counts")
}

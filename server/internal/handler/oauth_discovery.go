package handler

import (
	"encoding/json"
	"net/http"
)

// ProtectedResourcePath is where OAuth protected resource metadata (RFC 9728)
// is served. The path with /mcp appended is the address RFC 9728 derives for
// the /mcp resource; both answer with the same document.
const ProtectedResourcePath = "/.well-known/oauth-protected-resource"

type protectedResourceMetadata struct {
	Resource               string   `json:"resource"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
	ResourceName           string   `json:"resource_name"`
	ResourceDocumentation  string   `json:"resource_documentation,omitempty"`
}

// WithOAuthDiscovery serves protected resource metadata for /mcp and adds the
// RFC 9728 WWW-Authenticate challenge to every 401 from /mcp, so MCP clients
// can discover how to authenticate. It runs in front of the public guard
// because metadata is public and constant: it reads nothing, holds no state
// and never touches the graph. origin is the operator-configured public origin
// and is never taken from the request; with none, next is returned unchanged.
//
// The metadata names no authorization server until kb issues OAuth grants, so
// clients learn that /mcp takes a bearer and, for now, nothing more.
func WithOAuthDiscovery(next http.Handler, origin string) http.Handler {
	if origin == "" {
		return next
	}
	metadataURL := origin + ProtectedResourcePath + RemoteMCPPath
	body, err := json.Marshal(protectedResourceMetadata{
		Resource:               origin + RemoteMCPPath,
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "kb",
		ResourceDocumentation:  "https://github.com/JustinBeaudry/kb",
	})
	if err != nil {
		return next
	}
	return &oauthDiscovery{next: next, metadata: body, challenge: `Bearer resource_metadata="` + metadataURL + `"`}
}

type oauthDiscovery struct {
	next      http.Handler
	metadata  []byte
	challenge string
}

func (d *oauthDiscovery) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case ProtectedResourcePath, ProtectedResourcePath + RemoteMCPPath:
		d.serveMetadata(w, r)
	case RemoteMCPPath:
		d.next.ServeHTTP(&challengeWriter{ResponseWriter: w, challenge: d.challenge}, r)
	default:
		d.next.ServeHTTP(w, r)
	}
}

func (d *oauthDiscovery) serveMetadata(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	// Public, credential-free metadata that browser-based MCP clients fetch
	// cross-origin. No credentials are ever accepted or returned here.
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("X-Content-Type-Options", "nosniff")
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		h.Set("Content-Type", "application/json")
		h.Set("Cache-Control", "public, max-age=3600")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(d.metadata)
		}
	case http.MethodOptions:
		h.Set("Access-Control-Allow-Methods", "GET, HEAD")
		w.WriteHeader(http.StatusNoContent)
	default:
		h.Set("Allow", "GET, HEAD, OPTIONS")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// challengeWriter adds the discovery challenge to a 401 unless the handler
// already set one.
type challengeWriter struct {
	http.ResponseWriter
	challenge string
	wrote     bool
}

func (c *challengeWriter) WriteHeader(code int) {
	if !c.wrote {
		c.wrote = true
		if code == http.StatusUnauthorized && c.Header().Get("WWW-Authenticate") == "" {
			c.Header().Set("WWW-Authenticate", c.challenge)
		}
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *challengeWriter) Write(b []byte) (int, error) {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	return c.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (c *challengeWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

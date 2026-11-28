package handler

import (
	"encoding/json"
	"net/http"
	"slices"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// MCPResourceMetadataPath is where OAuth protected resource metadata (RFC 9728)
// for the /mcp resource is served: the address RFC 9728 §3.1 derives from the
// resource identifier https://<origin>/mcp. The root well-known address is not
// served, because its document would have to name the origin itself as the
// resource (§3.3), and kb protects only /mcp with this metadata.
const MCPResourceMetadataPath = "/.well-known/oauth-protected-resource" + RemoteMCPPath

// AuthorizationServerMetadataPath is where OAuth authorization server metadata
// (RFC 8414) is served. kb is its own authorization server, with the public
// origin as issuer, so the address has no path suffix.
const AuthorizationServerMetadataPath = "/.well-known/oauth-authorization-server"

type protectedResourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	ScopesSupported        []string `json:"scopes_supported"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
	ResourceName           string   `json:"resource_name"`
	ResourceDocumentation  string   `json:"resource_documentation,omitempty"`
}

type authorizationServerMetadata struct {
	Issuer                                     string   `json:"issuer"`
	AuthorizationEndpoint                      string   `json:"authorization_endpoint"`
	TokenEndpoint                              string   `json:"token_endpoint"`
	RegistrationEndpoint                       string   `json:"registration_endpoint"`
	RevocationEndpoint                         string   `json:"revocation_endpoint"`
	ScopesSupported                            []string `json:"scopes_supported"`
	ResponseTypesSupported                     []string `json:"response_types_supported"`
	ResponseModesSupported                     []string `json:"response_modes_supported"`
	GrantTypesSupported                        []string `json:"grant_types_supported"`
	TokenEndpointAuthMethodsSupported          []string `json:"token_endpoint_auth_methods_supported"`
	RevocationEndpointAuthMethodsSupported     []string `json:"revocation_endpoint_auth_methods_supported"`
	CodeChallengeMethodsSupported              []string `json:"code_challenge_methods_supported"`
	AuthorizationResponseIssParameterSupported bool     `json:"authorization_response_iss_parameter_supported"`
	ServiceDocumentation                       string   `json:"service_documentation,omitempty"`
}

// WithOAuthDiscovery serves protected resource metadata for /mcp and adds the
// RFC 9728 WWW-Authenticate challenge to every 401 from /mcp, so MCP clients
// can discover how to authenticate. It runs in front of the public guard
// because metadata is public and constant: it reads nothing, holds no state
// and never touches the graph. origin is the operator-configured public origin
// and is never taken from the request; with none, next is returned unchanged.
//
// It also serves authorization server metadata (RFC 8414), naming kb itself
// as the authorization server for /mcp.
func WithOAuthDiscovery(next http.Handler, origin string) http.Handler {
	if origin == "" {
		return next
	}
	metadataURL := origin + MCPResourceMetadataPath
	scopes := slices.Clone(domain.OAuthGrantCapabilities)
	body, err := json.Marshal(protectedResourceMetadata{
		Resource:               origin + RemoteMCPPath,
		AuthorizationServers:   []string{origin},
		ScopesSupported:        scopes,
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "kb",
		ResourceDocumentation:  "https://github.com/JustinBeaudry/kb",
	})
	if err != nil {
		return next
	}
	server, err := json.Marshal(authorizationServerMetadata{
		Issuer:                                     origin,
		AuthorizationEndpoint:                      origin + OAuthAuthorizePath,
		TokenEndpoint:                              origin + OAuthTokenPath,
		RegistrationEndpoint:                       origin + OAuthRegisterPath,
		RevocationEndpoint:                         origin + OAuthRevokePath,
		ScopesSupported:                            scopes,
		ResponseTypesSupported:                     []string{"code"},
		ResponseModesSupported:                     []string{"query"},
		GrantTypesSupported:                        []string{"authorization_code", "refresh_token"},
		TokenEndpointAuthMethodsSupported:          []string{"none"},
		RevocationEndpointAuthMethodsSupported:     []string{"none"},
		CodeChallengeMethodsSupported:              []string{"S256"},
		AuthorizationResponseIssParameterSupported: true,
		ServiceDocumentation:                       "https://github.com/JustinBeaudry/kb",
	})
	if err != nil {
		return next
	}
	return &oauthDiscovery{next: next, metadata: body, server: server, challenge: `Bearer resource_metadata="` + metadataURL + `"`}
}

type oauthDiscovery struct {
	next      http.Handler
	metadata  []byte
	server    []byte
	challenge string
}

func (d *oauthDiscovery) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case MCPResourceMetadataPath:
		d.serveMetadata(w, r, d.metadata)
	case AuthorizationServerMetadataPath:
		d.serveMetadata(w, r, d.server)
	case RemoteMCPPath:
		d.next.ServeHTTP(&challengeWriter{ResponseWriter: w, challenge: d.challenge}, r)
	default:
		d.next.ServeHTTP(w, r)
	}
}

func (d *oauthDiscovery) serveMetadata(w http.ResponseWriter, r *http.Request, body []byte) {
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
			_, _ = w.Write(body)
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

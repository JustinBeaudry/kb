package handler

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/service"
)

const (
	// OAuthPathPrefix holds every OAuth endpoint kb serves. Requests under it
	// have no bearer yet, so they pass GuardOAuth instead of the public guard.
	OAuthPathPrefix = "/oauth/"
	// OAuthRegisterPath is dynamic client registration (RFC 7591).
	OAuthRegisterPath = OAuthPathPrefix + "register"

	oauthRequestsPerSecond  = 2
	oauthRequestBurst       = 8
	oauthConcurrentRequests = 4
	maxOAuthRegisterBody    = 16 << 10
)

// OAuthRegistrar registers public OAuth clients.
type OAuthRegistrar interface {
	Register(context.Context, service.OAuthRegistrationInput) (domain.OAuthRegistration, error)
}

// WithOAuthEndpoints sends every request under /oauth/ to oauth and all others
// to next. With a nil oauth, next is returned unchanged.
func WithOAuthEndpoints(next, oauth http.Handler) http.Handler {
	if oauth == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, OAuthPathPrefix) {
			oauth.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// GuardOAuth bounds the unauthenticated OAuth endpoints before any graph work:
// a small global rate and concurrency limit, separate from the bearer guard so
// OAuth traffic cannot take capacity from clients that hold a credential. It
// keeps no per-source limit, because the api sits behind a proxy and kb takes
// nothing about the caller from request headers.
func GuardOAuth(next http.Handler, logger *slog.Logger) http.Handler {
	return newOAuthGuard(next, logger, time.Now)
}

// RefuseOAuthMaintenance answers an OAuth request that maintenance admission
// refused, in OAuth terms a client can read.
func RefuseOAuthMaintenance(logger *slog.Logger) http.HandlerFunc {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		r = withOAuthRequestID(w, r)
		setOAuthHeaders(w.Header())
		w.Header().Set("Retry-After", "30")
		writeOAuthError(w, r, logger, started, time.Now, http.StatusServiceUnavailable, "temporarily_unavailable", "kb is in maintenance. Retry later.")
	}
}

type oauthGuard struct {
	next   http.Handler
	logger *slog.Logger
	now    func() time.Time
	mu     sync.Mutex
	last   time.Time
	tokens float64
	active int
}

func newOAuthGuard(next http.Handler, logger *slog.Logger, now func() time.Time) *oauthGuard {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &oauthGuard{next: next, logger: logger, now: now, last: now(), tokens: oauthRequestBurst}
}

func (g *oauthGuard) acquire() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	refill(&g.tokens, &g.last, g.now(), oauthRequestsPerSecond, oauthRequestBurst)
	if g.active >= oauthConcurrentRequests || g.tokens < 1 {
		return false
	}
	g.tokens--
	g.active++
	return true
}

func (g *oauthGuard) release() {
	g.mu.Lock()
	g.active--
	g.mu.Unlock()
}

func (g *oauthGuard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := g.now()
	r = withOAuthRequestID(w, r)
	setOAuthHeaders(w.Header())
	if !g.acquire() {
		w.Header().Set("Retry-After", "1")
		writeOAuthError(w, r, g.logger, started, g.now, http.StatusTooManyRequests, "temporarily_unavailable", "Too many OAuth requests. Retry after one second.")
		return
	}
	defer g.release()
	g.next.ServeHTTP(w, r)
}

// setOAuthHeaders applies the headers every OAuth response carries, early
// responses included. OAuth responses can hold client identifiers and, later,
// codes and tokens, so none is cached. Every OAuth endpoint is credential-free
// (the request cookie is SameSite=Strict and never sent cross-site), so
// browser clients may read responses cross-origin.
func setOAuthHeaders(h http.Header) {
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Access-Control-Allow-Origin", "*")
}

// NewOAuthHandler serves the OAuth endpoints that exist so far: dynamic client
// registration. Everything else under /oauth/ is 404.
func NewOAuthHandler(registrar OAuthRegistrar, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &oauthHandler{registrar: registrar, logger: logger, now: time.Now}
}

type oauthHandler struct {
	registrar OAuthRegistrar
	logger    *slog.Logger
	now       func() time.Time
}

type oauthRegistrationRequest struct {
	RedirectURIs            []string `json:"redirect_uris"`
	ClientName              string   `json:"client_name"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
}

type oauthRegistrationResponse struct {
	ClientID                string   `json:"client_id"`
	ClientIDIssuedAt        int64    `json:"client_id_issued_at"`
	RedirectURIs            []string `json:"redirect_uris"`
	ClientName              string   `json:"client_name,omitempty"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
}

func (h *oauthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := h.now()
	r = withOAuthRequestID(w, r)
	setOAuthHeaders(w.Header())
	if grants, ok := h.registrar.(OAuthGrants); ok && grants.Issuer() != "" {
		if h.serveGrants(w, r, grants, started) {
			return
		}
	}
	if r.URL.Path != OAuthRegisterPath {
		writeOAuthError(w, r, h.logger, started, h.now, http.StatusNotFound, "not_found", "No such OAuth endpoint.")
		return
	}
	switch r.Method {
	case http.MethodPost:
		h.register(w, r, started)
	case http.MethodOptions:
		w.Header().Set("Access-Control-Allow-Methods", "POST")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusNoContent)
		h.log(r, http.StatusNoContent, started)
	default:
		w.Header().Set("Allow", "POST, OPTIONS")
		writeOAuthError(w, r, h.logger, started, h.now, http.StatusMethodNotAllowed, "invalid_request", "Use POST.")
	}
}

func (h *oauthHandler) register(w http.ResponseWriter, r *http.Request, started time.Time) {
	if media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || media != "application/json" {
		writeOAuthError(w, r, h.logger, started, h.now, http.StatusUnsupportedMediaType, "invalid_client_metadata", "Send client metadata as application/json.")
		return
	}
	var req oauthRegistrationRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxOAuthRegisterBody))
	if err := dec.Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeOAuthError(w, r, h.logger, started, h.now, http.StatusRequestEntityTooLarge, "invalid_client_metadata", "Client metadata is too large.")
			return
		}
		writeOAuthError(w, r, h.logger, started, h.now, http.StatusBadRequest, "invalid_client_metadata", "Client metadata is not a valid JSON object.")
		return
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		writeOAuthError(w, r, h.logger, started, h.now, http.StatusBadRequest, "invalid_client_metadata", "Client metadata is not a single JSON object.")
		return
	}
	reg, err := h.registrar.Register(r.Context(), service.OAuthRegistrationInput{
		RedirectURIs:            req.RedirectURIs,
		ClientName:              req.ClientName,
		TokenEndpointAuthMethod: req.TokenEndpointAuthMethod,
		GrantTypes:              req.GrantTypes,
		ResponseTypes:           req.ResponseTypes,
	})
	switch {
	case err == nil:
	case errors.Is(err, domain.ErrInvalidRedirectURI):
		writeOAuthError(w, r, h.logger, started, h.now, http.StatusBadRequest, "invalid_redirect_uri", "Redirect URIs must be https, or http on a loopback IP address, with no fragment or user info.")
		return
	case errors.Is(err, domain.ErrInvalidInput):
		writeOAuthError(w, r, h.logger, started, h.now, http.StatusBadRequest, "invalid_client_metadata", "kb registers public clients that use the authorization code grant with PKCE.")
		return
	case errors.Is(err, domain.ErrBudget):
		w.Header().Set("Retry-After", "60")
		writeOAuthError(w, r, h.logger, started, h.now, http.StatusTooManyRequests, "temporarily_unavailable", "Too many registrations are waiting for approval. Retry later.")
		return
	default:
		writeOAuthError(w, r, h.logger, started, h.now, http.StatusServiceUnavailable, "temporarily_unavailable", "Registration is unavailable. Retry later.")
		return
	}
	grants := []string{"authorization_code", "refresh_token"}
	body := oauthRegistrationResponse{
		ClientID:                reg.ID,
		ClientIDIssuedAt:        reg.CreatedAt.Unix(),
		RedirectURIs:            reg.RedirectURIs,
		ClientName:              reg.ClientName,
		TokenEndpointAuthMethod: "none",
		GrantTypes:              grants,
		ResponseTypes:           []string{"code"},
	}
	writeOAuthJSON(w, r, http.StatusCreated, body)
	h.log(r, http.StatusCreated, started)
}

func (h *oauthHandler) log(r *http.Request, status int, started time.Time) {
	logOAuth(r, h.logger, status, started, h.now)
}

// withOAuthRequestID gives the request an ID for its response header and log
// line, keeping one already set.
func withOAuthRequestID(w http.ResponseWriter, r *http.Request) *http.Request {
	id := domain.RequestID(r.Context())
	if id == "" {
		id = rand.Text()
		r = r.WithContext(domain.WithRequestID(r.Context(), id))
	}
	w.Header().Set("X-Request-Id", id)
	return r
}

func writeOAuthJSON(w http.ResponseWriter, _ *http.Request, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

type oauthErrorBody struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func writeOAuthError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, started time.Time, now func() time.Time, status int, code, description string) {
	writeOAuthJSON(w, r, status, oauthErrorBody{Error: code, ErrorDescription: description})
	logOAuth(r, logger, status, started, now)
}

func logOAuth(r *http.Request, logger *slog.Logger, status int, started time.Time, now func() time.Time) {
	logger.InfoContext(r.Context(), "http_request", "request_id", domain.RequestID(r.Context()), "method", r.Method, "status", status, "duration_ms", max(0, now().Sub(started).Milliseconds()))
}

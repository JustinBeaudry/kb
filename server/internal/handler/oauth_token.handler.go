package handler

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/service"
)

const (
	// OAuthTokenPath exchanges authorization codes and refresh tokens.
	OAuthTokenPath = OAuthPathPrefix + "token"
	// OAuthRevokePath revokes a grant from either of its tokens (RFC 7009).
	OAuthRevokePath = OAuthPathPrefix + "revoke"

	maxOAuthFormBody = 8 << 10
)

// OAuthTokenIssuer issues and revokes OAuth tokens.
type OAuthTokenIssuer interface {
	Token(context.Context, service.OAuthTokenInput) (service.OAuthTokens, error)
	Revoke(ctx context.Context, clientID, token string) error
}

type oauthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope,omitempty"`
}

// serveTokens answers the token and revocation endpoints and reports whether
// the path was one of them.
func (h *oauthHandler) serveTokens(w http.ResponseWriter, r *http.Request, issuer OAuthTokenIssuer, started time.Time) bool {
	if r.URL.Path != OAuthTokenPath && r.URL.Path != OAuthRevokePath {
		return false
	}
	switch r.Method {
	case http.MethodPost:
	case http.MethodOptions:
		w.Header().Set("Access-Control-Allow-Methods", "POST")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusNoContent)
		h.log(r, http.StatusNoContent, started)
		return true
	default:
		w.Header().Set("Allow", "POST, OPTIONS")
		writeOAuthError(w, r, h.logger, started, h.now, http.StatusMethodNotAllowed, "invalid_request", "Use POST.")
		return true
	}
	// Pragma is for HTTP/1.0 caches, as RFC 6749 §5.1 asks.
	w.Header().Set("Pragma", "no-cache")
	form, ok := h.readForm(w, r, started)
	if !ok {
		return true
	}
	if r.URL.Path == OAuthRevokePath {
		h.revoke(w, r, issuer, form, started)
		return true
	}
	tokens, err := issuer.Token(r.Context(), service.OAuthTokenInput{
		GrantType: form.Get("grant_type"), ClientID: form.Get("client_id"), Code: form.Get("code"),
		RedirectURI: form.Get("redirect_uri"), CodeVerifier: form.Get("code_verifier"),
		RefreshToken: form.Get("refresh_token"), Resource: form.Get("resource"),
	})
	var refused *service.OAuthTokenError
	switch {
	case err == nil:
	case errors.As(err, &refused):
		writeOAuthError(w, r, h.logger, started, h.now, http.StatusBadRequest, refused.Code, refused.Description)
		return true
	default:
		w.Header().Set("Retry-After", "30")
		writeOAuthError(w, r, h.logger, started, h.now, http.StatusServiceUnavailable, "temporarily_unavailable", "Token issuance is unavailable. Retry later.")
		return true
	}
	writeOAuthJSON(w, r, http.StatusOK, oauthTokenResponse{AccessToken: tokens.AccessToken, TokenType: "Bearer", ExpiresIn: tokens.ExpiresIn, RefreshToken: tokens.RefreshToken, Scope: tokens.Scope})
	h.log(r, http.StatusOK, started)
	return true
}

func (h *oauthHandler) revoke(w http.ResponseWriter, r *http.Request, issuer OAuthTokenIssuer, form url.Values, started time.Time) {
	err := issuer.Revoke(r.Context(), form.Get("client_id"), form.Get("token"))
	var refused *service.OAuthTokenError
	switch {
	case err == nil:
		w.WriteHeader(http.StatusOK)
		h.log(r, http.StatusOK, started)
	case errors.As(err, &refused):
		writeOAuthError(w, r, h.logger, started, h.now, http.StatusBadRequest, refused.Code, refused.Description)
	default:
		w.Header().Set("Retry-After", "30")
		writeOAuthError(w, r, h.logger, started, h.now, http.StatusServiceUnavailable, "temporarily_unavailable", "Revocation is unavailable. Retry later.")
	}
}

// readForm reads a form-encoded body. kb's clients are public, so a request
// that tries to authenticate the client some other way is refused rather
// than silently ignored, and parameters are taken from the body only, never
// the query string, so tokens do not end up in access logs.
func (h *oauthHandler) readForm(w http.ResponseWriter, r *http.Request, started time.Time) (url.Values, bool) {
	if media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || media != "application/x-www-form-urlencoded" {
		writeOAuthError(w, r, h.logger, started, h.now, http.StatusUnsupportedMediaType, "invalid_request", "Send parameters as application/x-www-form-urlencoded.")
		return nil, false
	}
	if r.URL.RawQuery != "" {
		writeOAuthError(w, r, h.logger, started, h.now, http.StatusBadRequest, "invalid_request", "Send parameters in the request body, not the URL.")
		return nil, false
	}
	if r.Header.Get("Authorization") != "" {
		writeOAuthError(w, r, h.logger, started, h.now, http.StatusUnauthorized, "invalid_client", "kb registers public clients only. Send client_id without client authentication.")
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxOAuthFormBody)
	if err := r.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeOAuthError(w, r, h.logger, started, h.now, http.StatusRequestEntityTooLarge, "invalid_request", "The request body is too large.")
			return nil, false
		}
		writeOAuthError(w, r, h.logger, started, h.now, http.StatusBadRequest, "invalid_request", "The request body is not a valid form.")
		return nil, false
	}
	for key, values := range r.PostForm {
		if len(values) != 1 {
			writeOAuthError(w, r, h.logger, started, h.now, http.StatusBadRequest, "invalid_request", "Each parameter may appear once.")
			return nil, false
		}
		if key == "client_secret" || key == "client_assertion" {
			writeOAuthError(w, r, h.logger, started, h.now, http.StatusUnauthorized, "invalid_client", "kb registers public clients only. Send client_id without client authentication.")
			return nil, false
		}
	}
	return r.PostForm, true
}

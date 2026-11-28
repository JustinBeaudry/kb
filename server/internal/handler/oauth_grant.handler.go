package handler

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/service"
)

const (
	// OAuthAuthorizePath starts a grant request and shows the approval page.
	OAuthAuthorizePath = OAuthPathPrefix + "authorize"
	// OAuthRequestsPath is polled by the approval page.
	OAuthRequestsPath = OAuthPathPrefix + "requests/"
	oauthStaticPath   = OAuthPathPrefix + "static/"
	// OAuthRequestCookie binds a request to the browser that started it.
	OAuthRequestCookie = "__Secure-kb_oauth_request"

	oauthPageCSP = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
)

// OAuthGrants starts grant requests and answers the approval page's poll.
type OAuthGrants interface {
	Issuer() string
	Authorize(context.Context, service.OAuthAuthorizeInput) (service.OAuthAuthorization, error)
	Poll(ctx context.Context, id, cookie string) (service.OAuthPoll, error)
}

//go:embed oauthpage/approve.js oauthpage/approve.css
var oauthStatic embed.FS

var oauthPages = template.Must(template.New("approve").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Approve access to kb</title><link rel="stylesheet" href="/oauth/static/approve.css"></head>
<body><main id="kb-request" data-request="{{.ID}}">
<h1>An app is asking for access to kb</h1>
<p>It will send you back to</p>
<p class="host">{{.Host}}</p>
<p>It calls itself <strong>{{if .Name}}{{.Name}}{{else}}(no name){{end}}</strong> <span class="unverified">(not verified)</span>.</p>
<p>It asks for projects: <strong>{{if .Projects}}{{.Projects}}{{else}}none named, you choose{{end}}</strong>,
and capabilities: <strong>{{if .Capabilities}}{{.Capabilities}}{{else}}none named, you choose{{end}}</strong>.</p>
<p>Approval code</p>
<p class="code">{{.Code}}</p>
<p>Nothing is granted from this page. If you started this and the host above is the app you expect, approve it on the kb host:</p>
<pre>kb-admin oauth-approve --code {{.Code}} --project PROJECT --capabilities retrieval:read --ttl 720h</pre>
<p>or refuse it with <code>kb-admin oauth-deny --code {{.Code}}</code>. If you did not start this, close this page. The request expires in 10 minutes.</p>
<p id="kb-status" role="status">Waiting for your decision.</p>
</main><script src="/oauth/static/approve.js"></script></body></html>
{{define "invalid"}}<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Invalid request</title><link rel="stylesheet" href="/oauth/static/approve.css"></head>
<body><main><h1>This sign-in link isn't valid</h1>
<p>kb doesn't recognize the app or the address it asked to return to, so nothing was sent back to it. Start again from your app.</p></main></body></html>
{{end}}`))

type approvalPage struct{ ID, Host, Name, Projects, Capabilities, Code string }

// serveGrants answers the grant endpoints and reports whether the path was
// one of them.
func (h *oauthHandler) serveGrants(w http.ResponseWriter, r *http.Request, grants OAuthGrants, started time.Time) bool {
	switch {
	case r.URL.Path == OAuthAuthorizePath:
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeOAuthError(w, r, h.logger, started, h.now, http.StatusMethodNotAllowed, "invalid_request", "Use GET.")
			return true
		}
		h.authorize(w, r, grants, started)
	case strings.HasPrefix(r.URL.Path, OAuthRequestsPath):
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeOAuthError(w, r, h.logger, started, h.now, http.StatusMethodNotAllowed, "invalid_request", "Use GET.")
			return true
		}
		h.poll(w, r, grants, strings.TrimPrefix(r.URL.Path, OAuthRequestsPath), started)
	case r.URL.Path == oauthStaticPath+"approve.js" || r.URL.Path == oauthStaticPath+"approve.css":
		h.static(w, r, strings.TrimPrefix(r.URL.Path, oauthStaticPath), started)
	default:
		return false
	}
	return true
}

func (h *oauthHandler) authorize(w http.ResponseWriter, r *http.Request, grants OAuthGrants, started time.Time) {
	q := r.URL.Query()
	in := service.OAuthAuthorizeInput{
		ClientID: q.Get("client_id"), RedirectURI: q.Get("redirect_uri"), ResponseType: q.Get("response_type"),
		CodeChallenge: q.Get("code_challenge"), CodeChallengeMethod: q.Get("code_challenge_method"),
		State: q.Get("state"), Resource: q.Get("resource"), Scope: q.Get("scope"),
	}
	setPageHeaders(w.Header())
	a, err := grants.Authorize(r.Context(), in)
	var redirect *service.OAuthRedirectError
	switch {
	case err == nil:
	case errors.As(err, &redirect):
		params := url.Values{"error": {redirect.Code}, "error_description": {redirect.Description}, "iss": {grants.Issuer()}}
		if domain.ValidOAuthState(in.State) {
			params.Set("state", in.State)
		}
		target, ok := appendQuery(in.RedirectURI, params)
		if !ok {
			h.page(w, r, http.StatusBadRequest, "invalid", nil, started)
			return
		}
		http.Redirect(w, r, target, http.StatusFound)
		h.log(r, http.StatusFound, started)
		return
	case errors.Is(err, domain.ErrUnauthorized), errors.Is(err, domain.ErrInvalidInput):
		h.page(w, r, http.StatusBadRequest, "invalid", nil, started)
		return
	default:
		w.Header().Set("Retry-After", "30")
		writeOAuthError(w, r, h.logger, started, h.now, http.StatusServiceUnavailable, "temporarily_unavailable", "Authorization is unavailable. Retry later.")
		return
	}
	req := a.Request
	host := ""
	if u, err := url.Parse(req.RedirectURI); err == nil {
		host = u.Host
	}
	http.SetCookie(w, &http.Cookie{Name: OAuthRequestCookie, Value: a.Cookie, Path: OAuthRequestsPath + req.ID, MaxAge: int(domain.OAuthRequestTTL.Seconds()), Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	code := req.ApprovalCode
	if len(code) == domain.OAuthApprovalCodeLength {
		code = code[:4] + "-" + code[4:]
	}
	h.page(w, r, http.StatusOK, "approve", approvalPage{
		ID: req.ID, Host: host, Name: req.ClientName, Code: code,
		Projects: strings.Join(req.Requested.Projects, ", "), Capabilities: strings.Join(req.Requested.Capabilities, ", "),
	}, started)
}

func (h *oauthHandler) page(w http.ResponseWriter, r *http.Request, status int, name string, data any, started time.Time) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = oauthPages.ExecuteTemplate(w, name, data)
	h.log(r, status, started)
}

func (h *oauthHandler) poll(w http.ResponseWriter, r *http.Request, grants OAuthGrants, id string, started time.Time) {
	cookie, _ := r.Cookie(OAuthRequestCookie)
	value := ""
	if cookie != nil {
		value = cookie.Value
	}
	if id == "" || len(id) > 64 || strings.Contains(id, "/") || value == "" {
		writeOAuthError(w, r, h.logger, started, h.now, http.StatusNotFound, "not_found", "No such request.")
		return
	}
	poll, err := grants.Poll(r.Context(), id, value)
	switch {
	case err == nil:
	case errors.Is(err, domain.ErrForbidden):
		writeOAuthError(w, r, h.logger, started, h.now, http.StatusNotFound, "not_found", "No such request.")
		return
	default:
		w.Header().Set("Retry-After", "5")
		writeOAuthError(w, r, h.logger, started, h.now, http.StatusServiceUnavailable, "temporarily_unavailable", "Retry later.")
		return
	}
	body := map[string]string{"status": "pending"}
	if !poll.Pending {
		body = map[string]string{"status": "done", "redirect": poll.Redirect}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
	h.log(r, http.StatusOK, started)
}

func (h *oauthHandler) static(w http.ResponseWriter, r *http.Request, name string, started time.Time) {
	body, err := oauthStatic.ReadFile("oauthpage/" + name)
	if err != nil {
		writeOAuthError(w, r, h.logger, started, h.now, http.StatusNotFound, "not_found", "No such file.")
		return
	}
	kind := "text/css; charset=utf-8"
	if strings.HasSuffix(name, ".js") {
		kind = "text/javascript; charset=utf-8"
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	h.log(r, http.StatusOK, started)
}

// setPageHeaders locks the approval page down: no framing, no inline script,
// no referrer, and nothing but kb itself to load or fetch from.
func setPageHeaders(h http.Header) {
	h.Set("Content-Security-Policy", oauthPageCSP)
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Del("Access-Control-Allow-Origin")
}

// appendQuery adds params to a registered redirect URI, keeping its own query.
func appendQuery(base string, params url.Values) (string, bool) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", false
	}
	q := u.Query()
	for k, v := range params {
		q[k] = v
	}
	u.RawQuery = q.Encode()
	return u.String(), true
}

package handler

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

const (
	publicRequestsPerSecond  = 8
	publicRequestBurst       = 16
	publicConcurrentRequests = 8

	// One credential gets at most half of the shared capacity, so a runaway or
	// compromised client can't starve every other client.
	credentialRequestsPerSecond  = publicRequestsPerSecond / 2
	credentialRequestBurst       = publicRequestBurst / 2
	credentialConcurrentRequests = publicConcurrentRequests / 2
	// maxTrackedCredentials bounds the per-credential table. Only credentials
	// with requests in flight are kept when it fills.
	maxTrackedCredentials = 4096
)

// credentialBudget is one presented credential's share of the guard. It is
// keyed by the credential's SHA-256, so the guard never holds a bearer.
type credentialBudget struct {
	last   time.Time
	tokens float64
	active int
}

// GuardPublicAPI rejects impossible credentials without I/O and bounds entry to
// durable maintenance admission. It does not authenticate or cache credentials.
// next must include admission, authoritative authentication, and joined Finish.
func GuardPublicAPI(next http.Handler, logger *slog.Logger) http.Handler {
	return newPublicAPIGuard(next, logger, time.Now)
}

type publicAPIGuard struct {
	next   http.Handler
	logger *slog.Logger
	now    func() time.Time
	mu     sync.Mutex
	last   time.Time
	tokens float64
	active int
	byKey  map[[32]byte]*credentialBudget
}

func newPublicAPIGuard(next http.Handler, logger *slog.Logger, now func() time.Time) http.Handler {
	return &publicAPIGuard{next: next, logger: logger, now: now, last: now(), tokens: publicRequestBurst, byKey: map[[32]byte]*credentialBudget{}}
}

func refill(tokens *float64, last *time.Time, now time.Time, perSecond, burst float64) {
	if elapsed := now.Sub(*last); elapsed > 0 {
		*tokens = min(burst, *tokens+elapsed.Seconds()*perSecond)
		*last = now
	}
}

// acquire takes one request from the credential's share and from the shared
// capacity, or neither.
func (g *publicAPIGuard) acquire(key [32]byte) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	refill(&g.tokens, &g.last, now, publicRequestsPerSecond, publicRequestBurst)
	if g.active >= publicConcurrentRequests || g.tokens < 1 {
		return false
	}
	b, ok := g.byKey[key]
	if !ok {
		if len(g.byKey) >= maxTrackedCredentials {
			// Forgetting an idle credential can only loosen its limit.
			for k, idle := range g.byKey {
				if idle.active == 0 {
					delete(g.byKey, k)
				}
			}
		}
		b = &credentialBudget{last: now, tokens: credentialRequestBurst}
		g.byKey[key] = b
	}
	refill(&b.tokens, &b.last, now, credentialRequestsPerSecond, credentialRequestBurst)
	if b.active >= credentialConcurrentRequests || b.tokens < 1 {
		return false
	}
	g.tokens--
	g.active++
	b.tokens--
	b.active++
	return true
}

func (g *publicAPIGuard) release(key [32]byte) {
	g.mu.Lock()
	g.active--
	if b, ok := g.byKey[key]; ok {
		b.active--
	}
	g.mu.Unlock()
}

func (g *publicAPIGuard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := g.now()
	bearer, ok := bearerHeader(r.Header)
	// Mirror the existing IdentityService's cheap rejection rules. Actual token
	// lookup, revocation and auth epoch checks remain inside maintenance admission.
	if !ok || len(bearer) != 64 || strings.TrimSpace(bearer) != bearer {
		g.reject(w, r, http.StatusUnauthorized, started)
		return
	}
	key := sha256.Sum256([]byte(bearer))
	if !g.acquire(key) {
		w.Header().Set("Retry-After", "1")
		g.reject(w, r, http.StatusTooManyRequests, started)
		return
	}
	// Includes downstream Finish and releases capacity even if a panic escapes.
	// Releasing local capacity never finishes or reconciles a durable permit.
	defer g.release(key)
	g.next.ServeHTTP(w, r)
}

func (g *publicAPIGuard) reject(w http.ResponseWriter, r *http.Request, status int, started time.Time) {
	id := domain.RequestID(r.Context())
	if id == "" {
		id = rand.Text()
		r = r.WithContext(domain.WithRequestID(r.Context(), id))
	}
	detail := problemFor(domain.ErrUnauthorized)
	if status == http.StatusTooManyRequests {
		detail = problemFor(domain.ErrUnavailable)
		detail.action = "Retry after one second."
	}
	w.Header().Set("X-Request-Id", id)
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Problem{SchemaVersion: "3", Type: "about:blank", Title: http.StatusText(status), Status: status, Code: ProblemCode(detail.code), Message: detail.message, Action: detail.action, RequestId: id, Runbook: "memory-operations#identity"})
	g.logger.InfoContext(r.Context(), "http_request", "request_id", id, "method", r.Method, "status", status, "duration_ms", max(0, g.now().Sub(started).Milliseconds()))
}

package handler

import (
	"crypto/rand"
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
)

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
}

func newPublicAPIGuard(next http.Handler, logger *slog.Logger, now func() time.Time) http.Handler {
	return &publicAPIGuard{next: next, logger: logger, now: now, last: now(), tokens: publicRequestBurst}
}

func (g *publicAPIGuard) acquire() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if elapsed := now.Sub(g.last); elapsed > 0 {
		g.tokens = min(publicRequestBurst, g.tokens+elapsed.Seconds()*publicRequestsPerSecond)
		g.last = now
	}
	if g.active >= publicConcurrentRequests || g.tokens < 1 {
		return false
	}
	g.tokens--
	g.active++
	return true
}

func (g *publicAPIGuard) release() {
	g.mu.Lock()
	g.active--
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
	if !g.acquire() {
		w.Header().Set("Retry-After", "1")
		g.reject(w, r, http.StatusTooManyRequests, started)
		return
	}
	// Includes downstream Finish and releases capacity even if a panic escapes.
	// Releasing local capacity never finishes or reconciles a durable permit.
	defer g.release()
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

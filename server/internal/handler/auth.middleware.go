package handler

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/labstack/echo/v4"
)

type principalKey struct{}

func principal(ctx context.Context) domain.Principal {
	p, _ := ctx.Value(principalKey{}).(domain.Principal)
	return p
}

type Authenticator interface {
	Authenticate(context.Context, string) (domain.Principal, error)
}

// Header parsing is shared by the I/O-free public preflight and authoritative
// Echo authentication. Token validity remains the identity service's decision.
func bearerHeader(header http.Header) (string, bool) {
	value := header.Get("Authorization")
	if !strings.HasPrefix(value, "Bearer ") || len(header.Values("Authorization")) != 1 {
		return "", false
	}
	return strings.TrimPrefix(value, "Bearer "), true
}

func Authentication(identity Authenticator) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			bearer, ok := bearerHeader(c.Request().Header)
			if !ok {
				return problem(c, domain.ErrUnauthorized)
			}
			p, err := identity.Authenticate(c.Request().Context(), bearer)
			if err != nil {
				return problem(c, err)
			}
			c.SetRequest(c.Request().WithContext(context.WithValue(c.Request().Context(), principalKey{}, p)))
			return next(c)
		}
	}
}
func Capability(capability string, projects func(echo.Context) []string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			var scope []string
			if projects != nil {
				scope = projects(c)
			}
			if err := service.Authorize(principal(c.Request().Context()), capability, scope); err != nil {
				return problem(c, err)
			}
			return next(c)
		}
	}
}
func requestMetadata(logger *slog.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			id := domain.RequestID(c.Request().Context())
			if id == "" {
				id = rand.Text()
				c.SetRequest(c.Request().WithContext(domain.WithRequestID(c.Request().Context(), id)))
			}
			c.Response().Header().Set("X-Request-Id", id)
			c.Set("request_id", id)
			err := next(c)
			if err != nil {
				c.Error(err)
			}
			logger.InfoContext(c.Request().Context(), "http_request", "request_id", id, "method", c.Request().Method, "status", c.Response().Status, "duration_ms", time.Since(start).Milliseconds())
			return nil
		}
	}
}
func problem(c echo.Context, err error) error {
	p := problemFor(err)
	return writeProblem(c, p.status, p.code, p.message, p.action)
}

type problemDetail struct {
	status                int
	code, message, action string
}

// problemFor is the single closed mapping from domain errors to safe codes,
// shared by HTTP problems and MCP tool errors. Remote or wrapped error text is
// never exposed.
func problemFor(err error) problemDetail {
	switch {
	case errors.Is(err, domain.ErrUnauthorized):
		return problemDetail{401, "unauthorized", "Credential unavailable.", "Reconnect this profile through owner enrollment."}
	case errors.Is(err, domain.ErrForbidden):
		return problemDetail{403, "forbidden", "Operation not authorized.", "Use an authorized project and capability."}
	case errors.Is(err, domain.ErrInvalidInput):
		return problemDetail{400, "invalid_input", "Invalid request.", "Check the documented request schema."}
	case errors.Is(err, domain.ErrStale):
		return problemDetail{409, "stale", "Results changed.", "Rerun the original scoped retrieval without its continuation."}
	case errors.Is(err, domain.ErrBudget):
		return problemDetail{413, "budget", "Output exceeds the requested budget.", "Rerun with a larger budget within the documented limit."}
	case errors.Is(err, domain.ErrIncomplete):
		return problemDetail{503, "incomplete", "Evidence is incomplete.", "Complete capture or retry when the source is available."}
	case errors.Is(err, domain.ErrDeleted):
		// Permanent: owner deletion must not look like a retryable outage.
		return problemDetail{410, "deleted", "Record deleted.", "Do not retry; capture a new event if still needed."}
	case errors.Is(err, domain.ErrUnknownOutcome):
		// Only remote write adapters produce this; the server never does.
		return problemDetail{http.StatusBadGateway, "unknown_outcome", "The write may have committed.", "The write may have committed but its receipt was lost. Reread current state (working retrieval or checkpoint) before retrying; a retry with expected_revision fails safely on conflict."}
	case errors.Is(err, domain.ErrConflict):
		return problemDetail{409, "conflict", "Operation conflicts with current state.", "Refresh state before retrying."}
	}
	return problemDetail{http.StatusServiceUnavailable, "unavailable", "Operation unavailable.", "Consult the service runbook and retry later."}
}
func writeProblem(c echo.Context, status int, code, message, action string) error {
	id, _ := c.Get("request_id").(string)
	if id == "" {
		id = rand.Text()
	}
	return c.JSON(status, Problem{SchemaVersion: "3", Type: "about:blank", Title: http.StatusText(status), Status: status, Code: ProblemCode(code), Message: message, Action: action, RequestId: id, Runbook: "memory-operations#identity"})
}

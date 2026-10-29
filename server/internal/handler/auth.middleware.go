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

func Authentication(identity Authenticator) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			header := c.Request().Header.Get("Authorization")
			if !strings.HasPrefix(header, "Bearer ") || len(c.Request().Header.Values("Authorization")) != 1 {
				return problem(c, domain.ErrUnauthorized)
			}
			p, err := identity.Authenticate(c.Request().Context(), strings.TrimPrefix(header, "Bearer "))
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
			id := rand.Text()
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
	status, code, message, action := http.StatusServiceUnavailable, "unavailable", "Operation unavailable.", "Consult the service runbook and retry later."
	switch {
	case errors.Is(err, domain.ErrUnauthorized):
		status, code, message, action = 401, "unauthorized", "Credential unavailable.", "Reconnect this profile through owner enrollment."
	case errors.Is(err, domain.ErrForbidden):
		status, code, message, action = 403, "forbidden", "Operation not authorized.", "Use an authorized project and capability."
	case errors.Is(err, domain.ErrInvalidInput):
		status, code, message, action = 400, "invalid_input", "Invalid request.", "Check the documented request schema."
	case errors.Is(err, domain.ErrConflict):
		status, code, message, action = 409, "conflict", "Operation conflicts with current state.", "Refresh state before retrying."
	}
	return writeProblem(c, status, code, message, action)
}
func writeProblem(c echo.Context, status int, code, message, action string) error {
	id, _ := c.Get("request_id").(string)
	if id == "" {
		id = rand.Text()
	}
	return c.JSON(status, Problem{SchemaVersion: "3", Type: "about:blank", Title: http.StatusText(status), Status: status, Code: ProblemCode(code), Message: message, Action: action, RequestId: id, Runbook: "memory-operations#identity"})
}

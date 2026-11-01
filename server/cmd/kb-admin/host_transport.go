package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
)

// Production callers cannot compose services directly. Only a child holding a
// verified inherited supervisor channel reaches the factory; local callers use
// the protected host socket. Injected runAdmin tests have no production bypass.
func productionAdminRoute(ctx context.Context, out io.Writer) (adminRoute, func(error) error) {
	var child *gateway.HostAdminChildSession
	route := func(req domain.HostAdminRequest, sink *os.File) (bool, error) {
		if hint := os.Getenv("KB_HOST_EXECUTION_FD"); hint != "" {
			if hint != "4" {
				return false, domain.ErrForbidden
			}
			var err error
			child, err = gateway.BeginHostAdminChild(ctx, req, os.NewFile(4, "host-execution"))
			return false, err
		}
		path := os.Getenv("KB_HOST_ADMIN_SOCKET")
		if path == "" {
			path = "/data/kb/control/admin.sock"
		}
		client, err := gateway.NewUnixHostAdminClient(path, os.Geteuid())
		if err != nil {
			return true, err
		}
		req.RequestID = rand.Text()
		result, err := client.Run(ctx, req, sink)
		if err != nil {
			return true, hostFailure(result, err)
		}
		if !result.Completed {
			return true, hostFailure(result, domain.ErrUnknownOutcome)
		}
		_, err = out.Write(result.ResultJSON)
		return true, err
	}
	complete := func(result error) error {
		if child == nil {
			return nil
		}
		defer func() { _ = child.Close() }()
		code := completedAdminOutcome(ctx, result)
		if code == "" {
			return nil
		}
		return child.CompleteWithCode(context.WithoutCancel(ctx), code)
	}
	return route, complete
}
func completedAdminCode(err error) string {
	if err == nil {
		return "ok"
	}
	for _, item := range []struct {
		err  error
		code string
	}{
		{domain.ErrConflict, "conflict"}, {domain.ErrStale, "stale"}, {domain.ErrInvalidInput, "invalid_input"},
		{domain.ErrUnavailable, "unavailable"}, {domain.ErrForbidden, "forbidden"}, {domain.ErrUnauthorized, "unauthorized"}, {domain.ErrIncomplete, "incomplete"}, {domain.ErrDeleted, "deleted"},
	} {
		if errors.Is(err, item.err) {
			return item.code
		}
	}
	return ""
}

// Only private host failures carry reconciliation identity. Never retain payloads
// or remote error text in this diagnostic.
type hostOperationError struct {
	operation, stage string
	cause            error
}

func (e hostOperationError) Error() string {
	return fmt.Sprintf("unknown_outcome: operation=%s stage=%s; reconcile this operation before retry", e.operation, e.stage)
}
func (e hostOperationError) Unwrap() error { return e.cause }
func hostFailure(result domain.HostAdminResult, err error) error {
	if result.OperationID == "" || len(result.OperationID) > 128 {
		return err
	}
	for _, c := range result.OperationID {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			continue
		}
		return err
	}
	switch result.Stage {
	case "reconcile_operation", "finish":
	default:
		return err
	}
	return hostOperationError{operation: result.OperationID, stage: result.Stage, cause: domain.ErrUnknownOutcome}
}

func completedAdminOutcome(ctx context.Context, err error) string {
	if !domain.OperationSettled(ctx) {
		return ""
	}
	return completedAdminCode(err)
}

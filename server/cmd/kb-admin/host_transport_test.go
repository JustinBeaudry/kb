package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHostRouteRunsAfterInputValidationBeforeAnyComposition(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		called      bool
	}{
		{"valid", `{"ProjectID":"fixture"}`, true},
		{"unknown input rejected", `{"ProjectID":"fixture","arbitrary_path":"private"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			route := func(req domain.HostAdminRequest, sink *os.File) (bool, error) {
				called = true
				assert.Equal(t, "policy", req.Action)
				assert.JSONEq(t, tc.input, string(req.InputJSON))
				assert.Nil(t, sink)
				return true, nil
			}
			factory := func(context.Context, bool) (administration, func(), error) {
				t.Error("route bypassed supervisor")
				return administration{}, func() {}, domain.ErrUnavailable
			}
			err := runAdminRouted(t.Context(), []string{"policy"}, &bytes.Buffer{}, strings.NewReader(tc.input), factory, route)
			assert.Equal(t, tc.called, called)
			if tc.called {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, domain.ErrInvalidInput)
			}
		})
	}
}
func TestProductionHostRouteRejectsUnsignedDescriptorHint(t *testing.T) {
	t.Setenv("KB_HOST_EXECUTION_FD", "5")
	route, complete := productionAdminRoute(t.Context(), &bytes.Buffer{})
	handled, err := route(domain.HostAdminRequest{Version: 1, Action: "list"}, nil)
	assert.False(t, handled)
	require.ErrorIs(t, err, domain.ErrForbidden)
	require.NoError(t, complete(err))
}
func TestUnknownAdminOutcomeCannotCompletePermit(t *testing.T) {
	for _, err := range []error{domain.ErrUnknownOutcome, context.Canceled, errors.New("private fixture error")} {
		assert.Empty(t, completedAdminCode(err))
	}
	assert.Equal(t, "unavailable", completedAdminCode(domain.ErrUnavailable))
	assert.Equal(t, "ok", completedAdminCode(nil))
	assert.Equal(t, "conflict", completedAdminCode(domain.ErrConflict))
}

func TestPrivateHostFailureRetainsOnlyReconciliationIdentity(t *testing.T) {
	for _, stage := range []string{"reconcile_operation", "finish"} {
		result := domain.HostAdminResult{OperationID: "operation-123", Stage: stage, Code: "unknown_outcome", ResultJSON: []byte("bearer-private-source")}
		err := hostFailure(result, errors.New("secret remote response"))
		output := safeError(commandError{command: "setup", cause: err})
		assert.Contains(t, output, "operation-123")
		assert.Contains(t, output, stage)
		assert.NotContains(t, output, "bearer")
		assert.NotContains(t, output, "secret")
	}
}

func TestMaintenanceFailureHasDistinctSafeRetryCode(t *testing.T) {
	output := safeError(commandError{command: "setup", cause: domain.ErrMaintenance})
	assert.Equal(t, "setup: maintenance: admission refused; retry after the current maintenance operation completes", output)
	assert.NotContains(t, safeError(domain.ErrUnknownOutcome), "maintenance:")
}

func TestHostCompletionUsesStickyEffectsRatherThanFinalBusinessError(t *testing.T) {
	for _, err := range []error{nil, domain.ErrForbidden, domain.ErrStale, domain.ErrDeleted, domain.ErrIncomplete, domain.ErrUnavailable} {
		assert.Empty(t, completedAdminOutcome(t.Context(), err))
		ctx := domain.WithOperationOutcome(t.Context())
		assert.NotEmpty(t, completedAdminOutcome(ctx, err))
		domain.MarkEffectUncertain(ctx)
		assert.Empty(t, completedAdminOutcome(ctx, err))
	}
}

type failedHostOperationRunner struct{ stage string }

func (r failedHostOperationRunner) Run(_ context.Context, request domain.HostAdminRequest, _ *os.File) (domain.HostAdminResult, error) {
	return domain.HostAdminResult{RequestID: request.RequestID, OperationID: "operation-123", Stage: r.stage, Code: "unknown_outcome", ResultJSON: []byte("bearer-private-source"), Completed: r.stage == "finish"}, domain.ErrUnknownOutcome
}
func TestProductionHostRoutePreservesFailedOperationFromUnixTransport(t *testing.T) {
	for _, stage := range []string{"reconcile_operation", "finish"} {
		t.Run(stage, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "kb-outcome-")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
			dir, err = filepath.EvalSymlinks(dir)
			require.NoError(t, err)
			path := filepath.Join(dir, "admin.sock")
			server, err := gateway.NewUnixHostAdminServer(path, os.Geteuid(), failedHostOperationRunner{stage: stage})
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- server.Serve(ctx) }()
			t.Cleanup(func() { cancel(); _ = server.Close(); require.NoError(t, <-done) })
			t.Setenv("KB_HOST_EXECUTION_FD", "")
			t.Setenv("KB_HOST_ADMIN_SOCKET", path)
			var output bytes.Buffer
			route, _ := productionAdminRoute(ctx, &output)
			handled, err := route(domain.HostAdminRequest{Version: 1, Action: "list"}, nil)
			require.True(t, handled)
			require.ErrorIs(t, err, domain.ErrUnknownOutcome)
			diagnostic := safeError(err)
			assert.Contains(t, diagnostic, "operation-123")
			assert.Contains(t, diagnostic, stage)
			assert.Empty(t, output.String())
			assert.NotContains(t, diagnostic, "bearer-private-source")
		})
	}
}

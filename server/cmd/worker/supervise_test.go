package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var fastRetry = startupRetry{initial: time.Millisecond, max: 4 * time.Millisecond, giveUp: time.Second, healthy: time.Hour}

func TestSuperviseRetriesUnreachableGraphUntilItReturns(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	calls := 0
	err := superviseStartup(t.Context(), logger, fastRetry, func(context.Context, startupHooks) error {
		calls++
		if calls < 4 {
			return platform.AtStage("graph_connect", domain.ErrUnavailable)
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 4, calls)
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	require.Len(t, lines, 3)
	var first map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	assert.Equal(t, "worker_startup_retry", first["msg"])
	assert.Equal(t, "graph_connect", first["stage"])
	assert.Equal(t, "unavailable", first["error_class"])
}

func TestSuperviseFailsClosedOnConfiguration(t *testing.T) {
	calls := 0
	err := superviseStartup(t.Context(), slog.New(slog.DiscardHandler), fastRetry, func(context.Context, startupHooks) error {
		calls++
		return platform.AtStage("config", domain.ErrInvalidInput)
	})
	require.ErrorIs(t, err, domain.ErrInvalidInput)
	assert.Equal(t, 1, calls)
	assert.Equal(t, "config", platform.FailureStage(err))
}

func TestSuperviseGivesUpAfterSustainedOutage(t *testing.T) {
	policy := fastRetry
	policy.giveUp = 20 * time.Millisecond
	err := superviseStartup(t.Context(), slog.New(slog.DiscardHandler), policy, func(context.Context, startupHooks) error {
		return platform.AtStage("graph_connect", domain.ErrUnavailable)
	})
	require.ErrorIs(t, err, domain.ErrUnavailable)
}

func TestSuperviseStopsQuietlyOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	policy := fastRetry
	policy.initial, policy.max = time.Hour, time.Hour
	done := make(chan error, 1)
	go func() {
		done <- superviseStartup(ctx, slog.New(slog.DiscardHandler), policy, func(context.Context, startupHooks) error {
			return domain.ErrUnavailable
		})
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not stop on shutdown")
	}
}

func TestSuperviseResetsWindowOnlyAfterServingHealthy(t *testing.T) {
	policy := startupRetry{initial: time.Millisecond, max: time.Millisecond, giveUp: 30 * time.Millisecond, healthy: 5 * time.Millisecond}
	calls := 0
	err := superviseStartup(t.Context(), slog.New(slog.DiscardHandler), policy, func(_ context.Context, h startupHooks) error {
		calls++
		if calls == 10 {
			return nil
		}
		h.ready()
		time.Sleep(6 * time.Millisecond) // each run serves long enough to count as healthy
		return platform.AtStage("worker_run", errors.Join(domain.ErrUnavailable))
	})
	require.NoError(t, err)
	assert.Equal(t, 10, calls)
}

func TestSuperviseSlowFailuresBeforeReadyStillGiveUp(t *testing.T) {
	// Each attempt runs longer than healthy but never becomes ready, as when
	// admission waits a long time and then the graph refuses the connection.
	policy := startupRetry{initial: time.Millisecond, max: time.Millisecond, giveUp: 40 * time.Millisecond, healthy: 5 * time.Millisecond}
	calls := 0
	err := superviseStartup(t.Context(), slog.New(slog.DiscardHandler), policy, func(context.Context, startupHooks) error {
		calls++
		time.Sleep(15 * time.Millisecond)
		return platform.AtStage("graph_connect", domain.ErrUnavailable)
	})
	require.ErrorIs(t, err, domain.ErrUnavailable)
	assert.Less(t, calls, 10)
}

func TestSuperviseBoundsAnAttemptThatNeverReturns(t *testing.T) {
	policy := fastRetry
	policy.giveUp = 20 * time.Millisecond
	done := make(chan error, 1)
	go func() {
		done <- superviseStartup(t.Context(), slog.New(slog.DiscardHandler), policy, func(_ context.Context, h startupHooks) error {
			<-h.admission.Done() // admission retrying forever
			return platform.AtStage("admission", h.admission.Err())
		})
	}()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Equal(t, "startup_deadline", platform.FailureStage(err))
		assert.Equal(t, "deadline", platform.ErrorClass(err))
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not bound a blocked startup")
	}
}

func TestSuperviseDoesNotCancelAReadyAttempt(t *testing.T) {
	policy := fastRetry
	policy.giveUp = 10 * time.Millisecond
	err := superviseStartup(t.Context(), slog.New(slog.DiscardHandler), policy, func(ctx context.Context, h startupHooks) error {
		h.ready()
		select {
		case <-ctx.Done():
			return platform.AtStage("worker_run", ctx.Err())
		case <-time.After(40 * time.Millisecond): // served past the startup deadline
			return nil
		}
	})
	require.NoError(t, err)
}

func TestSuperviseDoesNotCancelWorkAfterAdmission(t *testing.T) {
	// Admission came in time, but recovery under the permit outlasts the window.
	policy := fastRetry
	policy.giveUp = 10 * time.Millisecond
	err := superviseStartup(t.Context(), slog.New(slog.DiscardHandler), policy, func(ctx context.Context, h startupHooks) error {
		h.admitted()
		select {
		case <-ctx.Done():
			return platform.AtStage("lifecycle_recover", ctx.Err())
		case <-time.After(40 * time.Millisecond):
		}
		h.ready()
		return nil
	})
	require.NoError(t, err)
}

func TestSuperviseKeepsAPermitGrantedAsTheDeadlineFires(t *testing.T) {
	// The permit arrives in the same instant the deadline fires, before the
	// attempt can report admitted. The work under the permit must still run.
	policy := fastRetry
	policy.giveUp = 10 * time.Millisecond
	err := superviseStartup(t.Context(), slog.New(slog.DiscardHandler), policy, func(ctx context.Context, h startupHooks) error {
		<-h.admission.Done()
		h.admitted()
		time.Sleep(20 * time.Millisecond) // recovery under the permit
		if ctx.Err() != nil {
			return platform.AtStage("lifecycle_recover", ctx.Err())
		}
		h.ready()
		return nil
	})
	require.NoError(t, err)
}

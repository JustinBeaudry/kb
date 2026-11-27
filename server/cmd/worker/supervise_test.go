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
	err := superviseStartup(t.Context(), logger, fastRetry, func(context.Context) error {
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
	err := superviseStartup(t.Context(), slog.New(slog.DiscardHandler), fastRetry, func(context.Context) error {
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
	err := superviseStartup(t.Context(), slog.New(slog.DiscardHandler), policy, func(context.Context) error {
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
		done <- superviseStartup(ctx, slog.New(slog.DiscardHandler), policy, func(context.Context) error {
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

func TestSuperviseResetsBackoffAfterHealthyRun(t *testing.T) {
	policy := startupRetry{initial: time.Millisecond, max: time.Millisecond, giveUp: 30 * time.Millisecond, healthy: 5 * time.Millisecond}
	calls := 0
	err := superviseStartup(t.Context(), slog.New(slog.DiscardHandler), policy, func(context.Context) error {
		calls++
		if calls == 10 {
			return nil
		}
		time.Sleep(6 * time.Millisecond) // each run counts as healthy, so the outage clock resets
		return platform.AtStage("worker_run", errors.Join(domain.ErrUnavailable))
	})
	require.NoError(t, err)
	assert.Equal(t, 10, calls)
}

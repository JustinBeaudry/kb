package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/platform"
)

// startupRetry bounds how long the worker keeps retrying a composition that
// fails on an unreachable dependency, such as the graph restarting during a
// deploy. Configuration and policy failures are never retried.
type startupRetry struct {
	initial time.Duration // first backoff
	max     time.Duration // backoff ceiling
	giveUp  time.Duration // continuous failure before the process exits
	healthy time.Duration // an attempt that ran this long resets the backoff
}

var defaultStartupRetry = startupRetry{initial: time.Second, max: 30 * time.Second, giveUp: 15 * time.Minute, healthy: 2 * time.Minute}

// superviseStartup runs attempt until it returns nil, the context ends, it
// fails with a non-transient error, or transient failures continue for longer
// than giveUp. Before this, one graph restart stopped the worker process and
// relied on the platform restart budget to bring it back.
func superviseStartup(ctx context.Context, logger *slog.Logger, policy startupRetry, attempt func(context.Context) error) error {
	backoff := policy.initial
	var failingSince time.Time
	for n := 1; ; n++ {
		started := time.Now()
		err := attempt(ctx)
		if err == nil || ctx.Err() != nil {
			return nil
		}
		if !platform.Transient(err) {
			return err
		}
		if time.Since(started) >= policy.healthy {
			backoff, failingSince = policy.initial, time.Time{}
		}
		if failingSince.IsZero() {
			failingSince = started
		}
		if time.Since(failingSince) >= policy.giveUp {
			return err
		}
		logger.Warn("worker_startup_retry", "stage", platform.FailureStage(err), "error_class", platform.ErrorClass(err), "attempt", n, "retry_after_ms", backoff.Milliseconds())
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		backoff = min(backoff*2, policy.max)
	}
}

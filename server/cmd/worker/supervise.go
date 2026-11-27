package main

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
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
	healthy time.Duration // time serving after ready that resets the failure window
}

var defaultStartupRetry = startupRetry{initial: time.Second, max: 30 * time.Second, giveUp: 15 * time.Minute, healthy: 2 * time.Minute}

const (
	attemptStarting int32 = iota
	attemptAdmitted
	attemptReady
	attemptTimedOut
)

// startupHooks lets an attempt report its progress. admitted disarms the
// startup deadline once maintenance admission is granted, so the work that
// runs under the permit (graph connection, lifecycle recovery and releasing
// the permit) is never cancelled part way. ready marks the worker serving.
type startupHooks struct {
	admitted func()
	ready    func()
}

// superviseStartup runs attempt until it returns nil, the context ends, it
// fails with a non-transient error, or transient failures continue for longer
// than giveUp. Before this, one graph restart stopped the worker process and
// relied on the platform restart budget to bring it back.
//
// Until an attempt reports admitted (or ready), it is bounded by what is left
// of the failure window, so an admission wait that blocks forever (admission
// retries indefinitely on its own) still ends in worker_stopped.
// The window and backoff reset only after an attempt served for healthy.
func superviseStartup(ctx context.Context, logger *slog.Logger, policy startupRetry, attempt func(context.Context, startupHooks) error) error {
	backoff := policy.initial
	var failingSince time.Time
	for n := 1; ; n++ {
		started := time.Now()
		if failingSince.IsZero() {
			failingSince = started
		}
		result := runAttempt(ctx, failingSince.Add(policy.giveUp), attempt)
		err, readyAt, timedOut := result.err, result.readyAt, result.timedOut
		if ctx.Err() != nil {
			return nil
		}
		if err == nil && !timedOut {
			return nil
		}
		if timedOut {
			// A real failure from the attempt explains more than the deadline; the
			// cancellation the deadline itself caused does not.
			if platform.FailureStage(err) == "" || errors.Is(err, context.Canceled) {
				err = platform.AtStage("startup_deadline", context.DeadlineExceeded)
			}
			return err
		}
		if !platform.Transient(err) {
			return err
		}
		if !readyAt.IsZero() && time.Since(readyAt) >= policy.healthy {
			backoff, failingSince = policy.initial, time.Time{}
		} else if time.Since(failingSince) >= policy.giveUp {
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

type attemptResult struct {
	err      error
	readyAt  time.Time // zero when the attempt never became ready
	timedOut bool      // the deadline passed before admission or ready
}

// runAttempt cancels the attempt at deadline unless it was admitted or became
// ready first.
func runAttempt(ctx context.Context, deadline time.Time, attempt func(context.Context, startupHooks) error) attemptResult {
	attemptCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var state atomic.Int32
	var readyAt atomic.Pointer[time.Time]
	admitted := func() { state.CompareAndSwap(attemptStarting, attemptAdmitted) }
	ready := func() {
		if state.CompareAndSwap(attemptStarting, attemptReady) || state.CompareAndSwap(attemptAdmitted, attemptReady) {
			now := time.Now()
			readyAt.Store(&now)
		}
	}
	timer := time.AfterFunc(time.Until(deadline), func() {
		if state.CompareAndSwap(attemptStarting, attemptTimedOut) {
			cancel()
		}
	})
	defer timer.Stop()
	err := attempt(attemptCtx, startupHooks{admitted: admitted, ready: ready})
	var at time.Time
	if p := readyAt.Load(); p != nil {
		at = *p
	}
	return attemptResult{err: err, readyAt: at, timedOut: state.Load() == attemptTimedOut}
}

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

// startupHooks lets an attempt report its progress. The startup deadline
// cancels only admission, the context the attempt waits for maintenance
// admission on, so work that runs under a granted permit (graph connection,
// lifecycle recovery and releasing the permit) is never cancelled part way,
// even when the deadline fires just as the permit arrives. admitted records
// that the permit was granted; ready marks the worker serving.
type startupHooks struct {
	admission context.Context
	admitted  func()
	ready     func()
}

// superviseStartup runs attempt until it returns nil, the context ends, it
// fails with a non-transient error, or transient failures continue for longer
// than giveUp. Before this, one graph restart stopped the worker process and
// relied on the platform restart budget to bring it back.
//
// An attempt's admission wait is bounded by what is left of the failure window,
// so an admission wait that blocks forever (admission retries indefinitely on
// its own) still ends in worker_stopped.
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

// runAttempt cancels the attempt's admission context at deadline unless it was
// admitted or became ready first. An attempt that reports admitted after the
// deadline fired got its permit in time and is not treated as timed out.
func runAttempt(ctx context.Context, deadline time.Time, attempt func(context.Context, startupHooks) error) attemptResult {
	admissionCtx, cancelAdmission := context.WithCancel(ctx)
	defer cancelAdmission()
	var state atomic.Int32
	var readyAt atomic.Pointer[time.Time]
	admitted := func() {
		_ = state.CompareAndSwap(attemptStarting, attemptAdmitted) || state.CompareAndSwap(attemptTimedOut, attemptAdmitted)
	}
	ready := func() {
		for _, from := range []int32{attemptStarting, attemptAdmitted, attemptTimedOut} {
			if state.CompareAndSwap(from, attemptReady) {
				now := time.Now()
				readyAt.Store(&now)
				return
			}
		}
	}
	timer := time.AfterFunc(time.Until(deadline), func() {
		if state.CompareAndSwap(attemptStarting, attemptTimedOut) {
			cancelAdmission()
		}
	})
	defer timer.Stop()
	err := attempt(ctx, startupHooks{admission: admissionCtx, admitted: admitted, ready: ready})
	var at time.Time
	if p := readyAt.Load(); p != nil {
		at = *p
	}
	return attemptResult{err: err, readyAt: at, timedOut: state.Load() == attemptTimedOut}
}

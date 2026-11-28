package domain

import (
	"context"
	"testing"
	"time"
)

// The watchdog stays armed when the held operation's outcome is uncertain, so
// its process ends before the supervisor's HoldUntil and grace pass.
func TestHoldWatchdogFiresUnlessTheOutcomeSettled(t *testing.T) {
	fired := make(chan string, 4)
	SetHoldOverrun(func(kind string) { fired <- kind })
	t.Cleanup(func() { SetHoldOverrun(nil) })
	run := func(uncertain, granted bool) bool {
		held, release := withPermitHoldFor(context.Background(), "api", 10*time.Millisecond, 10*time.Millisecond)
		op := WithOperationOutcome(held)
		if uncertain {
			MarkEffectUncertain(op)
		}
		if granted {
			release(op)
		} else {
			release(nil)
		}
		select {
		case <-fired:
			return true
		case <-time.After(200 * time.Millisecond):
			return false
		}
	}
	if run(false, true) {
		t.Fatal("a settled operation must disarm the watchdog")
	}
	if run(true, false) {
		t.Fatal("an operation that was never admitted must disarm the watchdog")
	}
	if !run(true, true) {
		t.Fatal("an uncertain operation must leave the watchdog armed")
	}
	held, _ := withPermitHoldFor(context.Background(), "api", 10*time.Millisecond, 10*time.Millisecond)
	_ = held
	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("an operation that never returned must trip the watchdog")
	}
}

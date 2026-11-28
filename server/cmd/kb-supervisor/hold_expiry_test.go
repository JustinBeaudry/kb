package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

type fakeExpirer struct {
	mu    sync.Mutex
	calls int
	out   []domain.OperationPermit
	err   error
}

func (f *fakeExpirer) ExpireHolds(context.Context, time.Time) ([]domain.OperationPermit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	out := f.out
	f.out = nil
	return out, f.err
}

func (f *fakeExpirer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// The scheduled backup blocks the supervisor's main loop while it drains, so
// hold expiry has to keep running on its own until the context ends.
func TestHoldExpiryRunsOnItsOwnLoopUntilCancelled(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	expirer := &fakeExpirer{out: []domain.OperationPermit{{ID: "p1", Kind: "worker"}}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- expireHolds(ctx, expirer, logger, time.Millisecond) }()
	deadline := time.Now().Add(5 * time.Second)
	for expirer.count() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("hold expiry ran %d times, want at least 3", expirer.count())
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("expireHolds() = %v after cancel, want nil", err)
	}
	if got := strings.Count(logs.String(), "permit_hold_expired"); got != 1 {
		t.Fatalf("logged %d expiries, want 1: %s", got, logs.String())
	}
	if !strings.Contains(logs.String(), "operation_kind=worker") {
		t.Fatalf("expiry log lacks the operation kind: %s", logs.String())
	}
}

func TestHoldExpiryReportsAFailedSave(t *testing.T) {
	want := errors.New("save failed")
	expirer := &fakeExpirer{err: want}
	err := expireHolds(context.Background(), expirer, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), time.Millisecond)
	if !errors.Is(err, want) {
		t.Fatalf("expireHolds() = %v, want %v", err, want)
	}
}

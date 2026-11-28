package domain_test

import (
	"context"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func installNoopWatchdog(t *testing.T) {
	t.Helper()
	domain.SetHoldOverrun(func(string) {})
	t.Cleanup(func() { domain.SetHoldOverrun(nil) })
}

func TestPermitHoldIsDeclaredOnlyWithItsOwnDeadline(t *testing.T) {
	installNoopWatchdog(t)
	ctx := context.Background()
	held, release := domain.WithPermitHold(ctx, "worker")
	defer release(nil)
	deadline, ok := held.Deadline()
	require.True(t, ok)
	assert.WithinDuration(t, time.Now().Add(15*time.Minute), deadline, time.Second)
	assert.Equal(t, 15*time.Minute, domain.DeclaredPermitHold(held, "worker"))
	assert.Zero(t, domain.DeclaredPermitHold(held, "api"), "a hold covers only the kind it was set for")
	assert.Zero(t, domain.DeclaredPermitHold(ctx, "worker"))
	shorter, stop := context.WithTimeout(held, time.Second)
	defer stop()
	assert.Equal(t, 15*time.Minute, domain.DeclaredPermitHold(shorter, "worker"), "an earlier deadline still keeps the hold")
	none, releaseNone := domain.WithPermitHold(ctx, "custom")
	defer releaseNone(nil)
	_, ok = none.Deadline()
	assert.False(t, ok, "kinds without a hold get no deadline")
	assert.Zero(t, domain.DeclaredPermitHold(none, "custom"))
}

func TestPermitHoldIsNotDeclaredWithoutTheWatchdog(t *testing.T) {
	held, release := domain.WithPermitHold(context.Background(), "api")
	defer release(nil)
	_, ok := held.Deadline()
	assert.True(t, ok, "the operation is still bounded")
	assert.Zero(t, domain.DeclaredPermitHold(held, "api"), "no process exit backs the hold, so it is not declared")
}

func TestEveryServiceRoleKindHasAHold(t *testing.T) {
	for _, kind := range []string{"api", "api-startup", "worker", "worker-startup", "sweep", "access-audit", "health"} {
		assert.Positive(t, domain.PermitHold(kind), kind)
		assert.LessOrEqual(t, domain.PermitHold(kind), time.Hour, kind)
	}
	assert.Zero(t, domain.PermitHold("host-admin:list"))
	assert.Zero(t, domain.PermitHold("startup-recovery"))
}

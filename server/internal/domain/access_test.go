package domain

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestAccessCollectorCountsEachAccessOnceAndDenialWins(t *testing.T) {
	p := Principal{ClientID: "agent", ProjectIDs: []string{"alpha", "beta"}}
	c := NewAccessCollector()
	ctx := WithAccessCollector(context.Background(), c)
	ObserveAccess(ctx, p, "retrieval:read", []string{"alpha"}, true)
	ObserveAccess(ctx, p, "retrieval:read", []string{"alpha"}, true)
	ObserveAccess(ctx, p, "candidate:write", []string{"beta"}, true)
	ObserveAccess(ctx, p, "candidate:write", []string{"beta"}, false)
	ObserveAccess(ctx, p, "candidate:write", []string{"beta"}, true)
	ObserveAccess(ctx, p, "status:read", nil, true)
	assert.Equal(t, []AccessObservation{
		{ClientID: "agent", ProjectID: "", Capability: "status:read", Allowed: true},
		{ClientID: "agent", ProjectID: "alpha", Capability: "retrieval:read", Allowed: true},
		{ClientID: "agent", ProjectID: "beta", Capability: "candidate:write", Allowed: false},
	}, c.Observations())
}

func TestAccessCollectorNeverStoresUngrantedProjectIDs(t *testing.T) {
	p := Principal{ClientID: "agent", ProjectIDs: []string{"alpha"}}
	c := NewAccessCollector()
	ctx := WithAccessCollector(context.Background(), c)
	ObserveAccess(ctx, p, "retrieval:read", []string{"secret-project-name"}, false)
	ObserveAccess(ctx, p, "retrieval:read", []string{"other\x1fname"}, false)
	assert.Equal(t, []AccessObservation{{ClientID: "agent", ProjectID: AccessOutsideGrant, Capability: "retrieval:read", Allowed: false}}, c.Observations())
}

func TestAccessCollectorIgnoresUnidentifiedAndBoundsRequests(t *testing.T) {
	c := NewAccessCollector()
	ctx := WithAccessCollector(context.Background(), c)
	ObserveAccess(ctx, Principal{}, "status:read", nil, false)
	assert.Empty(t, c.Observations())

	p := Principal{ClientID: "agent"}
	for i := range maxAccessPerRequest + 10 {
		ObserveAccess(ctx, p, "cap-"+time.Duration(i).String(), nil, true)
	}
	assert.Len(t, c.Observations(), maxAccessPerRequest)

	// No collector on the context is a no-op, not a panic.
	ObserveAccess(context.Background(), p, "status:read", nil, true)
}

func TestAccessRollupKeySortsNewestDayFirst(t *testing.T) {
	keys := []string{
		AccessRollup{Day: "2026-11-26", ClientID: "z", ProjectID: "p", Capability: "c"}.Key(),
		AccessRollup{Day: "2026-11-27", ClientID: "a", ProjectID: "p", Capability: "c"}.Key(),
	}
	slices.Sort(keys)
	slices.Reverse(keys)
	assert.Contains(t, keys[0], "2026-11-27")
}

func TestValidAccessDay(t *testing.T) {
	assert.True(t, ValidAccessDay("2026-11-27"))
	for _, bad := range []string{"", "2026-11-7", "2026-13-01", "2026-11-27T00:00:00Z", "yesterday"} {
		assert.False(t, ValidAccessDay(bad), bad)
	}
	assert.Equal(t, "2026-11-27", AccessDay(time.Date(2026, 11, 27, 23, 59, 0, 0, time.UTC)))
}

func TestDenyAccessMarksTheRequestsDecisionsDenied(t *testing.T) {
	c := NewAccessCollector()
	ctx := WithAccessCollector(context.Background(), c)
	p := Principal{ClientID: "agent", ProjectIDs: []string{"demo"}}
	ObserveAccess(ctx, p, "checkpoint:write", []string{"demo"}, true)
	DenyAccess(ctx)
	DenyAccess(context.Background()) // no collector: nothing to mark
	assert.Equal(t, []AccessObservation{{ClientID: "agent", ProjectID: "demo", Capability: "checkpoint:write", Allowed: false}}, c.Observations())
}

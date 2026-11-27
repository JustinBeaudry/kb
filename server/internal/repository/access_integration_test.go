package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAccessRollupsRealGraph(t *testing.T) {
	store, _, _ := jobsGraph(t)
	repo := repository.NewAccessRepository(store)
	ctx := t.Context()
	at := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	row := func(day, client, project string, allowed, denied int64, first, last time.Time) domain.AccessRollup {
		return domain.AccessRollup{Day: day, ClientID: client, ProjectID: project, Capability: "retrieval:read", Allowed: allowed, Denied: denied, FirstAt: first, LastAt: last}
	}
	within := func(fn func(context.Context, domain.Tx) error) error { return store.Within(ctx, fn) }

	require.NoError(t, within(func(ctx context.Context, tx domain.Tx) error {
		return repo.AddAccess(ctx, tx, []domain.AccessRollup{
			row("2026-11-27", "a", "p", 2, 0, at, at.Add(time.Minute)),
			row("2026-11-26", "a", "p", 1, 0, at, at),
			row("2026-11-27", "b", "*", 0, 3, at, at),
			row("2024-01-01", "old", "p", 1, 0, at, at),
		})
	}))
	// A second flush for the same key adds counts and widens the time range.
	require.NoError(t, within(func(ctx context.Context, tx domain.Tx) error {
		return repo.AddAccess(ctx, tx, []domain.AccessRollup{row("2026-11-27", "a", "p", 1, 1, at.Add(-time.Hour), at.Add(time.Hour))})
	}))
	assert.ErrorIs(t, within(func(ctx context.Context, tx domain.Tx) error {
		return repo.AddAccess(ctx, tx, []domain.AccessRollup{row("not-a-day", "a", "p", 1, 0, at, at)})
	}), domain.ErrInvalidInput)

	var got []domain.AccessRollup
	list := func(q domain.AccessQuery) {
		t.Helper()
		require.NoError(t, within(func(ctx context.Context, tx domain.Tx) (err error) {
			got, err = repo.ListAccess(ctx, tx, q)
			return err
		}))
	}
	list(domain.AccessQuery{Since: "2026-01-01", Limit: 10})
	require.Len(t, got, 3)
	assert.Equal(t, "b", got[0].ClientID, "newest day first, then key descending")
	assert.Equal(t, domain.AccessRollup{Day: "2026-11-27", ClientID: "a", ProjectID: "p", Capability: "retrieval:read", Allowed: 3, Denied: 1, FirstAt: at.Add(-time.Hour), LastAt: at.Add(time.Hour)}, got[1])
	assert.Equal(t, "2026-11-26", got[2].Day)

	list(domain.AccessQuery{Since: "2026-01-01", Limit: 10, ClientID: "a", ProjectID: "p"})
	assert.Len(t, got, 2)
	list(domain.AccessQuery{Since: "2026-01-01", Limit: 10, After: got[0].Key()})
	require.Len(t, got, 1)
	assert.Equal(t, "2026-11-26", got[0].Day)

	var pruned int
	require.NoError(t, within(func(ctx context.Context, tx domain.Tx) (err error) {
		pruned, err = repo.PruneAccess(ctx, tx, "2026-01-01", 100)
		return err
	}))
	assert.Equal(t, 1, pruned)
	list(domain.AccessQuery{Since: "2000-01-01", Limit: 10})
	assert.Len(t, got, 3)
}

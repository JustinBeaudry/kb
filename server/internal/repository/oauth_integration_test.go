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

func TestOAuthRegistrationsRealGraph(t *testing.T) {
	store, _, _ := jobsGraph(t)
	repo := repository.NewOAuthRepository(store)
	ctx := t.Context()
	at := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	within := func(fn func(context.Context, domain.Tx) error) error { return store.Within(ctx, fn) }
	reg := func(id string, created time.Time) domain.OAuthRegistration {
		return domain.OAuthRegistration{ID: id, RedirectURIs: []string{"https://client.example/cb", "http://127.0.0.1:7777/cb"}, ClientName: "Example", CreatedAt: created, ExpiresAt: created.Add(domain.OAuthRegistrationTTL)}
	}
	add := func(r domain.OAuthRegistration, max int) error {
		return within(func(ctx context.Context, tx domain.Tx) error { return repo.AddRegistration(ctx, tx, r, max) })
	}
	find := func(id string, now time.Time) (got domain.OAuthRegistration, err error) {
		err = within(func(ctx context.Context, tx domain.Tx) (err error) {
			got, err = repo.FindRegistration(ctx, tx, id, now)
			return err
		})
		return got, err
	}

	require.NoError(t, add(reg("oc_old", at.Add(-25*time.Hour)), 2))
	require.NoError(t, add(reg("oc_a", at), 2))
	// The expired registration does not count toward the cap.
	require.NoError(t, add(reg("oc_b", at), 2))
	assert.ErrorIs(t, add(reg("oc_c", at), 2), domain.ErrBudget, "two pending registrations fill a cap of two")
	assert.Error(t, add(reg("oc_a", at.Add(time.Minute)), 10), "an ID is registered once")

	got, err := find("oc_a", at.Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, reg("oc_a", at), got)
	_, err = find("oc_old", at)
	assert.ErrorIs(t, err, domain.ErrUnauthorized, "an expired registration is an unknown client")
	_, err = find("oc_missing", at)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)

	var pruned int
	require.NoError(t, within(func(ctx context.Context, tx domain.Tx) (err error) {
		pruned, err = repo.PruneRegistrations(ctx, tx, at, 10)
		return err
	}))
	assert.Equal(t, 1, pruned)
	require.NoError(t, within(func(ctx context.Context, tx domain.Tx) (err error) {
		pruned, err = repo.PruneRegistrations(ctx, tx, at, 10)
		return err
	}))
	assert.Zero(t, pruned)
	// Once the pending ones expire, a new registration fits again.
	later := at.Add(domain.OAuthRegistrationTTL + time.Minute)
	require.NoError(t, add(reg("oc_c", later), 2))
}

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

func TestOAuthRequestsRealGraph(t *testing.T) {
	store, _, _ := jobsGraph(t)
	repo := repository.NewOAuthRepository(store)
	ctx := t.Context()
	at := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	within := func(fn func(context.Context, domain.Tx) error) error { return store.Within(ctx, fn) }
	request := func(id, code string, created time.Time) domain.OAuthRequest {
		return domain.OAuthRequest{
			ID: id, ApprovalCode: code, ClientID: "oc_1", ClientName: "Example", RedirectURI: "https://client.example/cb",
			State: "state-" + id, CodeChallenge: "challenge-" + id, CookieDigest: "cookie-" + id,
			Requested: domain.OAuthScope{Projects: []string{"demo"}, Capabilities: []string{}},
			Status:    domain.OAuthRequestPending, Granted: domain.OAuthScope{Projects: []string{}, Capabilities: []string{}},
			CreatedAt: created, ExpiresAt: created.Add(domain.OAuthRequestTTL),
		}
	}
	add := func(r domain.OAuthRequest, max int) error {
		return within(func(ctx context.Context, tx domain.Tx) error { return repo.AddRequest(ctx, tx, r, max) })
	}
	find := func(id string) (got domain.OAuthRequest, err error) {
		err = within(func(ctx context.Context, tx domain.Tx) (err error) {
			got, err = repo.FindRequest(ctx, tx, id)
			return err
		})
		return got, err
	}

	require.NoError(t, add(request("or_old", "AAAAAAAA", at.Add(-time.Hour)), 2))
	require.NoError(t, add(request("or_a", "BBBBBBBB", at), 2))
	assert.ErrorIs(t, add(request("or_b", "BBBBBBBB", at), 2), domain.ErrConflict, "two live requests never share an approval code")
	require.NoError(t, add(request("or_b", "AAAAAAAA", at), 2), "an expired request's code can be reused")
	assert.ErrorIs(t, add(request("or_c", "CCCCCCCC", at), 2), domain.ErrBudget, "two pending requests fill a cap of two")

	got, err := find("or_a")
	require.NoError(t, err)
	assert.Equal(t, request("or_a", "BBBBBBBB", at), got)
	_, err = find("or_missing")
	assert.ErrorIs(t, err, domain.ErrForbidden)

	var pending []domain.OAuthRequest
	require.NoError(t, within(func(ctx context.Context, tx domain.Tx) (err error) {
		pending, err = repo.ListPendingRequests(ctx, tx, at, 10)
		return err
	}))
	require.Len(t, pending, 2)
	assert.ElementsMatch(t, []string{"or_a", "or_b"}, []string{pending[0].ID, pending[1].ID})

	require.NoError(t, within(func(ctx context.Context, tx domain.Tx) error {
		pending, err := repo.FindPendingRequest(ctx, tx, "BBBBBBBB", at)
		if err != nil {
			return err
		}
		return repo.DecideRequest(ctx, tx, pending.ID, domain.OAuthRequestApproved, domain.OAuthScope{Projects: []string{"demo"}, Capabilities: []string{"retrieval:read"}}, 720*time.Hour, at)
	}))
	assert.ErrorIs(t, within(func(ctx context.Context, tx domain.Tx) error {
		return repo.DecideRequest(ctx, tx, "or_a", domain.OAuthRequestDenied, domain.OAuthScope{}, 0, at)
	}), domain.ErrConflict, "a decision is final")
	assert.ErrorIs(t, within(func(ctx context.Context, tx domain.Tx) error {
		_, err := repo.FindPendingRequest(ctx, tx, "BBBBBBBB", at)
		return err
	}), domain.ErrForbidden, "a decided request no longer takes a code")
	assert.ErrorIs(t, within(func(ctx context.Context, tx domain.Tx) error {
		return repo.DecideRequest(ctx, tx, "or_b", domain.OAuthRequestDenied, domain.OAuthScope{}, 0, at.Add(domain.OAuthRequestTTL))
	}), domain.ErrConflict, "an expired request cannot be decided")

	got, err = find("or_a")
	require.NoError(t, err)
	assert.Equal(t, domain.OAuthRequestApproved, got.Status)
	assert.Equal(t, []string{"retrieval:read"}, got.Granted.Capabilities)
	assert.Equal(t, 720*time.Hour, got.GrantTTL)

	codeExpires := at.Add(domain.OAuthCodeTTL)
	require.NoError(t, within(func(ctx context.Context, tx domain.Tx) error {
		return repo.ReleaseRequest(ctx, tx, "or_a", "code-digest", codeExpires)
	}))
	assert.ErrorIs(t, within(func(ctx context.Context, tx domain.Tx) error {
		return repo.ReleaseRequest(ctx, tx, "or_a", "other-digest", codeExpires)
	}), domain.ErrConflict, "an outcome is released once")
	got, err = find("or_a")
	require.NoError(t, err)
	assert.True(t, got.Released)
	assert.Equal(t, "code-digest", got.CodeDigest)
	assert.True(t, codeExpires.Equal(got.CodeExpiresAt))

	// or_b is still pending. A code is released only for an approved request,
	// and once its denial is released the owner can no longer approve it.
	release := func(id, digest string) error {
		return within(func(ctx context.Context, tx domain.Tx) error {
			return repo.ReleaseRequest(ctx, tx, id, digest, codeExpires)
		})
	}
	assert.ErrorIs(t, release("or_b", "code-digest"), domain.ErrConflict, "no code for a request nobody approved")
	require.NoError(t, release("or_b", ""))
	assert.ErrorIs(t, within(func(ctx context.Context, tx domain.Tx) error {
		return repo.DecideRequest(ctx, tx, "or_b", domain.OAuthRequestApproved, domain.OAuthScope{Projects: []string{"demo"}, Capabilities: []string{"retrieval:read"}}, time.Hour, at)
	}), domain.ErrConflict, "a request whose denial reached the browser can't be approved")
	got, err = find("or_b")
	require.NoError(t, err)
	assert.Equal(t, domain.OAuthRequestPending, got.Status)
	assert.Empty(t, got.CodeDigest)

	var pruned int
	require.NoError(t, within(func(ctx context.Context, tx domain.Tx) (err error) {
		pruned, err = repo.PruneRequests(ctx, tx, at, 10)
		return err
	}))
	assert.Equal(t, 1, pruned)
	_, err = find("or_old")
	assert.ErrorIs(t, err, domain.ErrForbidden)
}

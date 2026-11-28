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
	assert.True(t, at.Add(720*time.Hour).Equal(got.GrantExpiresAt), "the grant's lifetime starts at approval")

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

func TestOAuthGrantsRealGraph(t *testing.T) {
	store, _, _ := jobsGraph(t)
	repo := repository.NewOAuthRepository(store)
	identities := repository.NewIdentityRepository(store)
	ctx := t.Context()
	at := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	within := func(fn func(context.Context, domain.Tx) error) error { return store.Within(ctx, fn) }

	require.NoError(t, within(func(ctx context.Context, tx domain.Tx) error {
		return repo.AddRegistration(ctx, tx, domain.OAuthRegistration{ID: "oc_1", RedirectURIs: []string{"https://client.example/cb"}, ClientName: "Example", CreatedAt: at, ExpiresAt: at.Add(domain.OAuthRegistrationTTL)}, 10)
	}))
	req := domain.OAuthRequest{
		ID: "or_a", ApprovalCode: "BBBBBBBB", ClientID: "oc_1", ClientName: "Example", RedirectURI: "https://client.example/cb",
		State: "s", CodeChallenge: "c", CookieDigest: "k",
		Requested: domain.OAuthScope{Projects: []string{"demo"}, Capabilities: []string{}},
		Status:    domain.OAuthRequestPending, Granted: domain.OAuthScope{Projects: []string{}, Capabilities: []string{}},
		CreatedAt: at, ExpiresAt: at.Add(domain.OAuthRequestTTL),
	}
	require.NoError(t, within(func(ctx context.Context, tx domain.Tx) error { return repo.AddRequest(ctx, tx, req, 10) }))
	claim := func(digest, grant string) (got domain.OAuthRequest, err error) {
		err = within(func(ctx context.Context, tx domain.Tx) (err error) {
			got, err = repo.ClaimCode(ctx, tx, digest, grant)
			return err
		})
		return got, err
	}
	_, err := claim("code-digest", "og_1")
	assert.ErrorIs(t, err, domain.ErrForbidden, "a pending request has no code to claim")

	require.NoError(t, within(func(ctx context.Context, tx domain.Tx) error {
		if err := repo.DecideRequest(ctx, tx, "or_a", domain.OAuthRequestApproved, domain.OAuthScope{Projects: []string{"demo"}, Capabilities: []string{"retrieval:read"}}, 720*time.Hour, at); err != nil {
			return err
		}
		return repo.ReleaseRequest(ctx, tx, "or_a", "code-digest", at.Add(domain.OAuthCodeTTL))
	}))
	_, err = claim("", "og_1")
	assert.ErrorIs(t, err, domain.ErrForbidden)
	_, err = claim("other-digest", "og_1")
	assert.ErrorIs(t, err, domain.ErrForbidden)
	claimed, err := claim("code-digest", "og_1")
	require.NoError(t, err)
	assert.True(t, claimed.CodeUsed)
	assert.Equal(t, "og_1", claimed.GrantID)
	again, err := claim("code-digest", "og_2")
	assert.ErrorIs(t, err, domain.ErrConflict, "a code is claimed once")
	assert.Equal(t, "og_1", again.GrantID, "the second claim learns which grant to revoke")

	grant := domain.OAuthGrant{ID: "og_1", ClientID: "oauth-og_1", RegistrationID: "oc_1", RedirectURI: "https://client.example/cb", RefreshDigest: "r1", CreatedAt: at, ExpiresAt: at.Add(720 * time.Hour)}
	assert.ErrorIs(t, within(func(ctx context.Context, tx domain.Tx) error {
		g := grant
		g.ID, g.RegistrationID = "og_x", "oc_missing"
		return repo.AddGrant(ctx, tx, g)
	}), domain.ErrForbidden, "a grant needs its registration")
	require.NoError(t, within(func(ctx context.Context, tx domain.Tx) error { return repo.AddGrant(ctx, tx, grant) }))

	var pruned int
	require.NoError(t, within(func(ctx context.Context, tx domain.Tx) (err error) {
		pruned, err = repo.PruneRegistrations(ctx, tx, at.Add(domain.OAuthRegistrationTTL+time.Hour), 10)
		return err
	}))
	assert.Zero(t, pruned, "an approved registration is never pruned")

	byRefresh := func(digest string) (got domain.OAuthGrant, err error) {
		err = within(func(ctx context.Context, tx domain.Tx) (err error) {
			got, err = repo.FindGrantByRefresh(ctx, tx, digest)
			return err
		})
		return got, err
	}
	got, err := byRefresh("r1")
	require.NoError(t, err)
	assert.Equal(t, grant.ID, got.ID)
	assert.True(t, grant.ExpiresAt.Equal(got.ExpiresAt))
	require.NoError(t, within(func(ctx context.Context, tx domain.Tx) error {
		g, err := repo.FindGrantByClient(ctx, tx, "oauth-og_1")
		assert.Equal(t, "og_1", g.ID)
		return err
	}))

	require.NoError(t, within(func(ctx context.Context, tx domain.Tx) error { return repo.RotateRefresh(ctx, tx, "og_1", "r1", "r2") }))
	assert.ErrorIs(t, within(func(ctx context.Context, tx domain.Tx) error { return repo.RotateRefresh(ctx, tx, "og_1", "r1", "r3") }), domain.ErrConflict, "a used refresh token cannot rotate again")
	got, err = byRefresh("r2")
	require.NoError(t, err)
	assert.Equal(t, "og_1", got.ID)
	got, err = byRefresh("r1")
	assert.ErrorIs(t, err, domain.ErrConflict, "a used refresh token names its grant")
	assert.Equal(t, "og_1", got.ID)
	_, err = byRefresh("unknown")
	assert.ErrorIs(t, err, domain.ErrForbidden)

	var expired []domain.OAuthGrant
	require.NoError(t, within(func(ctx context.Context, tx domain.Tx) (err error) {
		expired, err = repo.ExpiredGrants(ctx, tx, at, 10)
		return err
	}))
	assert.Empty(t, expired)
	require.NoError(t, within(func(ctx context.Context, tx domain.Tx) (err error) {
		expired, err = repo.ExpiredGrants(ctx, tx, grant.ExpiresAt, 10)
		return err
	}))
	require.Len(t, expired, 1)
	assert.Equal(t, "og_1", expired[0].ID)

	require.NoError(t, within(func(ctx context.Context, tx domain.Tx) error { return repo.DeleteGrant(ctx, tx, "og_1") }))
	require.NoError(t, within(func(ctx context.Context, tx domain.Tx) error { return repo.DeleteGrant(ctx, tx, "og_1") }), "deleting is idempotent")
	_, err = byRefresh("r2")
	assert.ErrorIs(t, err, domain.ErrForbidden)
	_, err = byRefresh("r1")
	assert.ErrorIs(t, err, domain.ErrForbidden, "used digests go with their grant")

	// An access token client carries the resource it is bound to.
	expires := at.Add(time.Hour)
	require.NoError(t, within(func(ctx context.Context, tx domain.Tx) error {
		return identities.PutClient(ctx, tx, domain.ClientRecord{Principal: domain.Principal{ClientID: "oauth-og_2", OwnerID: "owner", Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: []string{"retrieval:read"}, Resource: domain.OAuthResourceMCP}, TokenDigest: "access-digest", ExpiresAt: &expires})
	}))
	require.NoError(t, within(func(ctx context.Context, tx domain.Tx) error {
		record, err := identities.FindToken(ctx, tx, "access-digest")
		assert.Equal(t, domain.OAuthResourceMCP, record.Principal.Resource)
		return err
	}))
}

func TestOAuthGrantsAppearInTheClientListRealGraph(t *testing.T) {
	store, _, _ := jobsGraph(t)
	repo := repository.NewOAuthRepository(store)
	identities := repository.NewIdentityRepository(store)
	ctx := t.Context()
	at := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	within := func(fn func(context.Context, domain.Tx) error) error { return store.Within(ctx, fn) }
	grantEnds := at.Add(720 * time.Hour)
	tokenEnds := at.Add(time.Hour)
	require.NoError(t, within(func(ctx context.Context, tx domain.Tx) error {
		if err := repo.AddRegistration(ctx, tx, domain.OAuthRegistration{ID: "oc_1", RedirectURIs: []string{"https://client.example:8443/cb"}, ClientName: "Example", CreatedAt: at, ExpiresAt: at.Add(domain.OAuthRegistrationTTL)}, 10); err != nil {
			return err
		}
		if err := repo.AddGrant(ctx, tx, domain.OAuthGrant{ID: "og_1", ClientID: "oauth-og_1", RegistrationID: "oc_1", RedirectURI: "https://client.example:8443/cb?keep=1", RefreshDigest: "r1", CreatedAt: at, ExpiresAt: grantEnds}); err != nil {
			return err
		}
		if err := identities.PutClient(ctx, tx, domain.ClientRecord{Principal: domain.Principal{ClientID: "oauth-og_1", OwnerID: "owner", Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: []string{"status:read"}, Resource: domain.OAuthResourceMCP}, TokenDigest: "d1", ExpiresAt: &tokenEnds}); err != nil {
			return err
		}
		return identities.PutClient(ctx, tx, domain.ClientRecord{Principal: domain.Principal{ClientID: "agent", OwnerID: "owner", Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: []string{"status:read"}}, TokenDigest: "d2"})
	}))
	var listed []domain.ClientMetadata
	require.NoError(t, within(func(ctx context.Context, tx domain.Tx) (err error) {
		listed, err = identities.ListClients(ctx, tx, "owner", 10, "")
		return err
	}))
	require.Len(t, listed, 2)
	assert.Equal(t, "agent", listed[0].Principal.ClientID)
	assert.Nil(t, listed[0].OAuth, "a client no grant created has no OAuth summary")
	assert.Empty(t, listed[0].Principal.Resource)
	oauth := listed[1]
	assert.Equal(t, "oauth-og_1", oauth.Principal.ClientID)
	assert.Equal(t, domain.OAuthResourceMCP, oauth.Principal.Resource)
	require.NotNil(t, oauth.OAuth)
	assert.Equal(t, "client.example:8443", oauth.OAuth.RedirectHost, "only the host, never the path or query")
	assert.True(t, grantEnds.Equal(oauth.OAuth.GrantExpiresAt))
	require.NotNil(t, oauth.ExpiresAt)
	assert.True(t, tokenEnds.Equal(*oauth.ExpiresAt), "the client's own expiry is its current access token")
}

func TestOAuthRegistrationsOutliveTheRequestsThatNeedThemRealGraph(t *testing.T) {
	store, _, _ := jobsGraph(t)
	repo := repository.NewOAuthRepository(store)
	ctx := t.Context()
	at := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	within := func(fn func(context.Context, domain.Tx) error) error { return store.Within(ctx, fn) }
	regExpires := at.Add(domain.OAuthRegistrationTTL)
	for _, id := range []string{"oc_live", "oc_approved", "oc_free"} {
		r := domain.OAuthRegistration{ID: id, RedirectURIs: []string{"https://client.example/cb"}, ClientName: "Example", CreatedAt: at, ExpiresAt: regExpires}
		require.NoError(t, within(func(ctx context.Context, tx domain.Tx) error { return repo.AddRegistration(ctx, tx, r, 10) }))
	}
	// Both requests start shortly before their registrations expire.
	started := regExpires.Add(-domain.OAuthRequestTTL / 2)
	for i, client := range []string{"oc_live", "oc_approved"} {
		id, code := "or_"+client, []string{"AAAAAAAA", "BBBBBBBB"}[i]
		r := domain.OAuthRequest{
			ID: id, ApprovalCode: code, ClientID: client, ClientName: "Example", RedirectURI: "https://client.example/cb",
			State: "s", CodeChallenge: "c", CookieDigest: "cookie-" + id,
			Requested: domain.OAuthScope{Projects: []string{"demo"}, Capabilities: []string{}},
			Status:    domain.OAuthRequestPending, Granted: domain.OAuthScope{Projects: []string{}, Capabilities: []string{}},
			CreatedAt: started, ExpiresAt: started.Add(domain.OAuthRequestTTL),
		}
		require.NoError(t, within(func(ctx context.Context, tx domain.Tx) error { return repo.AddRequest(ctx, tx, r, 10) }))
	}
	require.NoError(t, within(func(ctx context.Context, tx domain.Tx) error {
		return repo.DecideRequest(ctx, tx, "or_oc_approved", domain.OAuthRequestApproved, domain.OAuthScope{Projects: []string{"demo"}, Capabilities: []string{}}, time.Hour, started)
	}))
	prune := func(now time.Time) (n int) {
		require.NoError(t, within(func(ctx context.Context, tx domain.Tx) (err error) {
			n, err = repo.PruneRegistrations(ctx, tx, now, 10)
			return err
		}))
		return n
	}
	exists := func(id string) bool {
		err := within(func(ctx context.Context, tx domain.Tx) error {
			_, err := repo.FindRegistration(ctx, tx, id, at)
			return err
		})
		return err == nil
	}

	assert.Equal(t, 1, prune(regExpires), "only the registration no request needs goes")
	assert.False(t, exists("oc_free"))
	assert.True(t, exists("oc_live"), "a live request keeps its registration")
	assert.True(t, exists("oc_approved"))

	requestsOver := started.Add(domain.OAuthRequestTTL)
	assert.Equal(t, 1, prune(requestsOver), "an expired pending request no longer holds its registration")
	assert.False(t, exists("oc_live"))
	assert.True(t, exists("oc_approved"), "an approved request holds its registration until it is pruned")

	require.NoError(t, within(func(ctx context.Context, tx domain.Tx) error {
		_, err := repo.PruneRequests(ctx, tx, requestsOver, 10)
		return err
	}))
	assert.Equal(t, 1, prune(requestsOver))
	assert.False(t, exists("oc_approved"))
}

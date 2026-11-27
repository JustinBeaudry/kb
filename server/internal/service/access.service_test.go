package service

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type accessFake struct {
	owner   OwnerIdentity
	stored  map[string]domain.AccessRollup
	failAdd int
	pruned  []string
	listed  []domain.AccessQuery
	list    []domain.AccessRollup
}

func newAccessFake() *accessFake {
	return &accessFake{owner: OwnerIdentity{ID: "owner", PublicKey: "key"}, stored: map[string]domain.AccessRollup{}}
}

func (f *accessFake) Owner(context.Context, Tx) (OwnerIdentity, error) { return f.owner, nil }

func (f *accessFake) AddAccess(_ context.Context, _ Tx, rows []domain.AccessRollup) error {
	if f.failAdd > 0 {
		f.failAdd--
		return domain.ErrUnavailable
	}
	for _, r := range rows {
		prior, ok := f.stored[r.Key()]
		if ok {
			r.Allowed += prior.Allowed
			r.Denied += prior.Denied
		}
		f.stored[r.Key()] = r
	}
	return nil
}

func (f *accessFake) ListAccess(_ context.Context, _ Tx, q domain.AccessQuery) ([]domain.AccessRollup, error) {
	f.listed = append(f.listed, q)
	if len(f.list) > q.Limit {
		return f.list[:q.Limit], nil
	}
	return f.list, nil
}

func (f *accessFake) PruneAccess(_ context.Context, _ Tx, before string, _ int) (int, error) {
	f.pruned = append(f.pruned, before)
	return 0, nil
}

func accessFixture(t *testing.T) (*AccessService, *accessFake) {
	t.Helper()
	f := newAccessFake()
	now := func() time.Time { return time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC) }
	return NewAccessService(directTx{}, f, f, now, nil), f
}

func TestAccessServiceFlushesMergedRollupsAndPrunes(t *testing.T) {
	s, f := accessFixture(t)
	s.Record([]domain.AccessObservation{{ClientID: "a", ProjectID: "p", Capability: "retrieval:read", Allowed: true}})
	s.Record([]domain.AccessObservation{{ClientID: "a", ProjectID: "p", Capability: "retrieval:read", Allowed: false}})
	s.Record(nil)
	require.NoError(t, s.Flush(context.Background()))
	require.Len(t, f.stored, 1)
	for _, r := range f.stored {
		assert.Equal(t, int64(1), r.Allowed)
		assert.Equal(t, int64(1), r.Denied)
		assert.Equal(t, "2026-11-27", r.Day)
	}
	assert.Equal(t, []string{"2025-10-23"}, f.pruned)

	// An empty ledger flushes nothing new.
	require.NoError(t, s.Flush(context.Background()))
	assert.Len(t, f.stored, 1)
}

func TestAccessServiceRequeuesFailedWrites(t *testing.T) {
	s, f := accessFixture(t)
	f.failAdd = 1
	s.Record([]domain.AccessObservation{{ClientID: "a", ProjectID: "p", Capability: "c", Allowed: true}})
	require.ErrorIs(t, s.Flush(context.Background()), domain.ErrUnavailable)
	assert.Empty(t, f.stored)
	s.Record([]domain.AccessObservation{{ClientID: "a", ProjectID: "p", Capability: "c", Allowed: true}})
	require.NoError(t, s.Flush(context.Background()))
	require.Len(t, f.stored, 1)
	for _, r := range f.stored {
		assert.Equal(t, int64(2), r.Allowed)
	}
}

func TestAccessServiceBoundsPendingLedger(t *testing.T) {
	s, f := accessFixture(t)
	obs := make([]domain.AccessObservation, 0, MaxPendingAccess+5)
	for i := range MaxPendingAccess + 5 {
		obs = append(obs, domain.AccessObservation{ClientID: "a", ProjectID: "p", Capability: "c" + time.Duration(i).String(), Allowed: true})
	}
	s.Record(obs)
	// Existing keys keep counting after the cap.
	s.Record(obs[:1])
	assert.Equal(t, int64(5), s.dropped)
	require.NoError(t, s.Flush(context.Background()))
	assert.Len(t, f.stored, MaxPendingAccess)
	assert.Equal(t, int64(0), s.dropped)
}

func TestAccessServiceListHostValidatesAndPages(t *testing.T) {
	s, f := accessFixture(t)
	ctx := context.Background()
	for _, q := range []domain.AccessQuery{
		{Limit: -1}, {Limit: 501}, {Since: "27-11-2026"}, {After: "!!"}, {After: strings.Repeat("a", 1025)},
		{ClientID: " padded"}, {ProjectID: "bad\nid"}, {ClientID: strings.Repeat("a", 257)},
	} {
		_, err := s.ListHost(ctx, q)
		assert.ErrorIs(t, err, domain.ErrInvalidInput, "%+v", q)
	}
	assert.Empty(t, f.listed)

	page, err := s.ListHost(ctx, domain.AccessQuery{})
	require.NoError(t, err)
	assert.NotNil(t, page.Rollups)
	assert.Empty(t, page.Next)
	assert.Equal(t, domain.AccessQuery{Since: "2026-10-28", Limit: 101}, f.listed[0])

	f.list = []domain.AccessRollup{
		{Day: "2026-11-27", ClientID: "b", ProjectID: "p", Capability: "c"},
		{Day: "2026-11-27", ClientID: "a", ProjectID: "p", Capability: "c"},
		{Day: "2026-11-26", ClientID: "a", ProjectID: "p", Capability: "c"},
	}
	page, err = s.ListHost(ctx, domain.AccessQuery{Limit: 2, ClientID: "a"})
	require.NoError(t, err)
	assert.Len(t, page.Rollups, 2)
	require.NotEmpty(t, page.Next)
	cursor, err := base64.RawURLEncoding.DecodeString(page.Next)
	require.NoError(t, err)
	assert.Equal(t, f.list[1].Key(), string(cursor))

	_, err = s.ListHost(ctx, domain.AccessQuery{Limit: 2, After: page.Next})
	require.NoError(t, err)
	assert.Equal(t, f.list[1].Key(), f.listed[len(f.listed)-1].After)
}

func TestAccessServiceListHostRequiresEnrolledOwner(t *testing.T) {
	s, f := accessFixture(t)
	f.owner = OwnerIdentity{}
	_, err := s.ListHost(context.Background(), domain.AccessQuery{})
	assert.ErrorIs(t, err, domain.ErrForbidden)
	assert.Empty(t, f.listed)
}

func TestAccessServiceRunFlushesOnShutdown(t *testing.T) {
	s, f := accessFixture(t)
	s.Record([]domain.AccessObservation{{ClientID: "a", ProjectID: "p", Capability: "c", Allowed: true}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx, time.Hour, nil); close(done) }()
	cancel()
	<-done
	assert.Len(t, f.stored, 1)
}

func TestAuthorizeRecordsTheDecision(t *testing.T) {
	c := domain.NewAccessCollector()
	ctx := domain.WithAccessCollector(context.Background(), c)
	p := domain.Principal{ClientID: "a", Role: domain.RoleAgent, ProjectIDs: []string{"p"}, Capabilities: []string{"retrieval:read"}}
	require.NoError(t, authorize(ctx, p, "retrieval:read", []string{"p"}))
	err := authorize(ctx, p, "candidate:write", []string{"p"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrForbidden))
	assert.Equal(t, []domain.AccessObservation{
		{ClientID: "a", ProjectID: "p", Capability: "candidate:write", Allowed: false},
		{ClientID: "a", ProjectID: "p", Capability: "retrieval:read", Allowed: true},
	}, c.Observations())
}

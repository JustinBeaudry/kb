package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
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
	batches map[string]bool
	tried   []string
	failAdd int
	// lostCommit stores the next batch, then reports failure, like a commit
	// whose acknowledgement never arrived.
	lostCommit int
	pruned     []string
	markersAt  []time.Time
	listed  []domain.AccessQuery
	list    []domain.AccessRollup
}

func newAccessFake() *accessFake {
	return &accessFake{owner: OwnerIdentity{ID: "owner", PublicKey: "key"}, stored: map[string]domain.AccessRollup{}, batches: map[string]bool{}}
}

func (f *accessFake) Owner(context.Context, Tx) (OwnerIdentity, error) { return f.owner, nil }

func (f *accessFake) AddAccess(_ context.Context, _ Tx, b domain.AccessBatch) error {
	f.tried = append(f.tried, b.ID)
	if f.failAdd > 0 {
		f.failAdd--
		return domain.ErrUnavailable
	}
	if f.batches[b.ID] {
		return nil
	}
	f.batches[b.ID] = true
	defer func() {
		if f.lostCommit > 0 {
			f.lostCommit--
		}
	}()
	for _, r := range b.Rollups {
		prior, ok := f.stored[r.Key()]
		if ok {
			r.Allowed += prior.Allowed
			r.Denied += prior.Denied
		}
		f.stored[r.Key()] = r
	}
	if f.lostCommit > 0 {
		return domain.ErrUnknownOutcome
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

func (f *accessFake) PruneAccess(_ context.Context, _ Tx, before string, markers time.Time, _ int) (int, error) {
	f.pruned = append(f.pruned, before)
	f.markersAt = append(f.markersAt, markers)
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
	assert.Equal(t, []time.Time{time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}, f.markersAt)

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

func TestAccessServiceNeverCountsAnUncertainBatchTwice(t *testing.T) {
	s, f := accessFixture(t)
	f.lostCommit = 1
	s.Record([]domain.AccessObservation{{ClientID: "a", ProjectID: "p", Capability: "c", Allowed: true}})
	require.Error(t, s.Flush(context.Background()))
	require.Len(t, f.stored, 1, "the write committed even though the flush could not tell")

	s.Record([]domain.AccessObservation{{ClientID: "a", ProjectID: "p", Capability: "c", Allowed: true}})
	require.NoError(t, s.Flush(context.Background()))
	require.Len(t, f.tried, 3)
	assert.Equal(t, f.tried[0], f.tried[1], "the uncertain batch is retried under its own ID")
	assert.NotEqual(t, f.tried[1], f.tried[2], "newer observations go in a new batch")
	for _, r := range f.stored {
		assert.Equal(t, int64(2), r.Allowed, "one access per request, never three")
	}
	assert.Empty(t, s.retry)
}

func TestAccessServiceBoundsAndExpiresRetries(t *testing.T) {
	f := newAccessFake()
	at := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	s := NewAccessService(directTx{}, f, f, func() time.Time { return at }, nil)
	f.failAdd = 100
	for i := range 3 {
		obs := make([]domain.AccessObservation, 0, MaxPendingAccess/2)
		for j := range MaxPendingAccess / 2 {
			obs = append(obs, domain.AccessObservation{ClientID: "a", ProjectID: "p", Capability: fmt.Sprint("c", i, "-", j), Allowed: true})
		}
		s.Record(obs)
		require.Error(t, s.Flush(context.Background()))
	}
	rows := 0
	for _, b := range s.retry {
		rows += len(b.Rollups)
	}
	assert.Equal(t, MaxPendingAccess, rows, "retries stay within the ledger bound")
	assert.Equal(t, int64(MaxPendingAccess/2), s.dropped, "the oldest batches are dropped and counted")

	// A batch older than the retry window is dropped rather than written late.
	f.failAdd = 0
	at = at.Add(accessBatchRetry + time.Minute)
	require.NoError(t, s.Flush(context.Background()))
	assert.Empty(t, f.stored)
	assert.Empty(t, s.retry)
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

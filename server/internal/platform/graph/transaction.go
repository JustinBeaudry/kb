package graph

import (
	"context"
	"crypto/rand"
	"sync/atomic"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
)

type transaction struct {
	id     string
	owner  *Store
	native neo4j.ExplicitTransaction
	active atomic.Bool
}

func (t *transaction) TransactionID() string { return t.id }

// Unwrap is for repositories only. Handles from other stores or completed
// callbacks are rejected. Never pass driver handles into the service layer.
func (s *Store) Unwrap(handle domain.Tx) (neo4j.ExplicitTransaction, error) {
	tx, ok := handle.(*transaction)
	if !ok || tx.owner != s || !tx.active.Load() {
		return nil, domain.ErrInvalidInput
	}
	return tx.native, nil
}

func (s *Store) Within(ctx context.Context, fn func(context.Context, domain.Tx) error) error {
	return s.traced(ctx, "graph_transaction", func(ctx context.Context) error { return s.within(ctx, fn) })
}
func (s *Store) within(ctx context.Context, fn func(context.Context, domain.Tx) error) error {
	if fn == nil {
		return domain.ErrInvalidInput
	}
	session := s.driver.NewSession(ctx, neo4j.SessionConfig{DatabaseName: s.database})
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := session.Close(cleanup); err != nil {
			domain.MarkEffectUncertain(ctx)
		}
	}()
	native, err := session.BeginTransaction(ctx, neo4j.WithTxTimeout(15*time.Second))
	if err != nil {
		return domain.ErrUnavailable
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := native.Close(cleanup); err != nil {
			domain.MarkEffectUncertain(ctx)
		}
	}()
	tx := &transaction{id: rand.Text(), owner: s, native: native}
	tx.active.Store(true)
	defer tx.active.Store(false)
	// Taking a real write lock before the callback prevents read-committed lost
	// updates. REMOVE does not release the node lock before transaction completion.
	// All shared mutable decisions acquire this single lock first.
	lockResult, err := native.Run(ctx, "MATCH (n:OwnerState {id:'owner'}) SET n._lock=true REMOVE n._lock RETURN n.id", nil)
	if err != nil {
		return domain.ErrUnavailable
	}
	if _, err = lockResult.Single(ctx); err != nil {
		return domain.ErrUnavailable
	}
	if err = fn(ctx, tx); err != nil {
		return err
	}
	if err = native.Commit(ctx); err != nil {
		domain.MarkEffectUncertain(ctx)
		return domain.ErrUnavailable
	}
	return nil
}

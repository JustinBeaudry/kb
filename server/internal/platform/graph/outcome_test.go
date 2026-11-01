package graph

import (
	"context"
	"errors"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

type outcomeDriver struct {
	neo4j.Driver
	session  *outcomeSession
	database string
}

func (d *outcomeDriver) NewSession(_ context.Context, c neo4j.SessionConfig) neo4j.Session {
	d.database = c.DatabaseName
	return d.session
}
func (d *outcomeDriver) VerifyConnectivity(context.Context) error { return nil }

type outcomeSession struct {
	neo4j.Session
	tx                *outcomeTx
	readErr, closeErr error
}

func (s *outcomeSession) BeginTransaction(context.Context, ...func(*neo4j.TransactionConfig)) (neo4j.ExplicitTransaction, error) {
	return s.tx, nil
}
func (s *outcomeSession) Close(context.Context) error { return s.closeErr }
func (s *outcomeSession) Run(context.Context, string, map[string]any, ...func(*neo4j.TransactionConfig)) (neo4j.Result, error) {
	return outcomeResult{}, s.readErr
}

type outcomeTx struct {
	neo4j.ExplicitTransaction
	commitErr, closeErr error
	commits             int
}

func (t *outcomeTx) Run(context.Context, string, map[string]any) (neo4j.Result, error) {
	return outcomeResult{}, nil
}
func (t *outcomeTx) Commit(context.Context) error { t.commits++; return t.commitErr }
func (t *outcomeTx) Close(context.Context) error  { return t.closeErr }

type outcomeResult struct{ neo4j.Result }

func (outcomeResult) Single(context.Context) (*neo4j.Record, error)        { return &neo4j.Record{}, nil }
func (outcomeResult) Consume(context.Context) (neo4j.ResultSummary, error) { return nil, nil }
func TestGraphUnknownCommitSurvivesLaterBusinessError(t *testing.T) {
	for _, schema := range []bool{false, true} {
		tx := &outcomeTx{commitErr: errors.New("lost acknowledgement")}
		driver := &outcomeDriver{session: &outcomeSession{tx: tx}}
		store := &Store{driver: driver, database: "selected"}
		ctx := domain.WithOperationOutcome(t.Context())
		var err error
		if schema {
			err = store.schemaStatement(ctx, "CREATE CONSTRAINT fixture")
		} else {
			err = store.Within(ctx, func(context.Context, domain.Tx) error { return nil })
		}
		require.ErrorIs(t, err, domain.ErrUnavailable)
		assert.Equal(t, 1, tx.commits)
		tx.commitErr = nil
		require.ErrorIs(t, store.Within(ctx, func(context.Context, domain.Tx) error { return domain.ErrDeleted }), domain.ErrDeleted)
		assert.False(t, domain.OperationSettled(ctx))
	}
}
func TestGraphRollbackCleanupFailureIsUncertain(t *testing.T) {
	tx := &outcomeTx{closeErr: errors.New("rollback acknowledgement lost")}
	store := &Store{driver: &outcomeDriver{session: &outcomeSession{tx: tx}}}
	ctx := domain.WithOperationOutcome(t.Context())
	require.ErrorIs(t, store.Within(ctx, func(context.Context, domain.Tx) error { return domain.ErrConflict }), domain.ErrConflict)
	assert.False(t, domain.OperationSettled(ctx))
}
func TestProbeRequiresSelectedDatabaseAndDoesNotMutate(t *testing.T) {
	driver := &outcomeDriver{session: &outcomeSession{readErr: errors.New("database unavailable")}}
	store := &Store{driver: driver, database: "selected"}
	ctx := domain.WithOperationOutcome(t.Context())
	require.ErrorIs(t, store.Probe(ctx), domain.ErrUnavailable)
	assert.Equal(t, "selected", driver.database)
	assert.True(t, domain.OperationSettled(ctx))
}

package repository

import (
	"context"
	"encoding/json"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
)

// ModelBudgetRepository stores spend reservations. Callers hold the OwnerState
// lock through the service transaction, so sum-then-insert is serialized.
type ModelBudgetRepository struct{ store *graph.Store }

func NewModelBudgetRepository(store *graph.Store) *ModelBudgetRepository {
	return &ModelBudgetRepository{store: store}
}

func (r *ModelBudgetRepository) query(ctx context.Context, tx domain.Tx, statement string, params map[string]any) ([]*neo4j.Record, error) {
	native, err := r.store.Unwrap(tx)
	if err != nil {
		return nil, err
	}
	result, err := native.Run(ctx, statement, params)
	if err != nil {
		return nil, domain.ErrUnavailable
	}
	rows, err := result.Collect(ctx)
	if err != nil {
		return nil, domain.ErrUnavailable
	}
	return rows, nil
}

// Committed never treats unknown cost as zero: reserved and unreconciled
// holds count their worst case.
func (r *ModelBudgetRepository) Committed(ctx context.Context, tx domain.Tx, day string) (int64, error) {
	rows, err := r.query(ctx, tx, `MATCH (m:ModelReservation {day:$day}) RETURN coalesce(sum(CASE WHEN m.state='settled' THEN m.costMicroUSD ELSE m.maxCostMicroUSD END),0)`, map[string]any{"day": day})
	if err != nil {
		return 0, err
	}
	if len(rows) != 1 {
		return 0, domain.ErrUnavailable
	}
	total, ok := rows[0].Values[0].(int64)
	if !ok || total < 0 {
		return 0, domain.ErrUnavailable
	}
	return total, nil
}

func reservationParams(m domain.Reservation) (map[string]any, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return nil, domain.ErrInvalidInput
	}
	return map[string]any{"id": m.ID, "day": m.Day, "route": m.Route, "project": m.Scope.ProjectID, "state": string(m.State), "max": m.MaxCostMicroUSD, "cost": m.Usage.CostMicroUSD, "costKnown": m.Usage.CostKnown, "data": string(data), "created": m.CreatedAt.UnixNano()}, nil
}

func (r *ModelBudgetRepository) PutReservation(ctx context.Context, tx domain.Tx, m domain.Reservation) error {
	p, err := reservationParams(m)
	if err != nil {
		return err
	}
	rows, err := r.query(ctx, tx, `CREATE (m:ModelReservation {id:$id,day:$day,route:$route,projectID:$project,state:$state,maxCostMicroUSD:$max,costMicroUSD:$cost,costKnown:$costKnown,data:$data,createdAt:$created}) RETURN m.id`, p)
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrConflict
	}
	return nil
}

func (r *ModelBudgetRepository) Reservation(ctx context.Context, tx domain.Tx, id string) (domain.Reservation, error) {
	rows, err := r.query(ctx, tx, `MATCH (m:ModelReservation {id:$id}) RETURN m.data`, map[string]any{"id": id})
	if err != nil {
		return domain.Reservation{}, err
	}
	if len(rows) != 1 {
		return domain.Reservation{}, domain.ErrConflict
	}
	var m domain.Reservation
	if decodeCapture(rows[0], 0, &m) != nil {
		return m, domain.ErrUnavailable
	}
	return m, nil
}

func (r *ModelBudgetRepository) UpdateReservation(ctx context.Context, tx domain.Tx, m domain.Reservation) error {
	p, err := reservationParams(m)
	if err != nil {
		return err
	}
	rows, err := r.query(ctx, tx, `MATCH (m:ModelReservation {id:$id}) SET m.state=$state,m.costMicroUSD=$cost,m.costKnown=$costKnown,m.data=$data RETURN m.id`, p)
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrConflict
	}
	return nil
}

func (r *ModelBudgetRepository) Unreconciled(ctx context.Context, tx domain.Tx) (int, error) {
	rows, err := r.query(ctx, tx, `MATCH (m:ModelReservation {state:'unreconciled'}) RETURN count(m)`, nil)
	if err != nil {
		return 0, err
	}
	if len(rows) != 1 {
		return 0, domain.ErrUnavailable
	}
	n, ok := rows[0].Values[0].(int64)
	if !ok {
		return 0, domain.ErrUnavailable
	}
	return int(n), nil
}

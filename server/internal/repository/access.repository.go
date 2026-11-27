package repository

import (
	"context"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
)

// AccessRepository stores daily access audit rollups as AccessRollup nodes,
// one per client, project, capability and UTC day, merged by key.
type AccessRepository struct{ store *graph.Store }

func NewAccessRepository(store *graph.Store) *AccessRepository {
	return &AccessRepository{store: store}
}

func (r *AccessRepository) query(ctx context.Context, tx domain.Tx, statement string, params map[string]any) ([]*neo4j.Record, error) {
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

// AddAccess merges one batch of rollups unless a batch with the same ID was
// already stored. The AccessBatch marker is created in the same transaction
// as the counts, so a retried batch either finds it and adds nothing or
// commits marker and counts together.
func (r *AccessRepository) AddAccess(ctx context.Context, tx domain.Tx, batch domain.AccessBatch) error {
	if !domain.ValidAccessID(batch.ID) || batch.At.IsZero() {
		return domain.ErrInvalidInput
	}
	if len(batch.Rollups) == 0 {
		return nil
	}
	rows := make([]map[string]any, len(batch.Rollups))
	for i, a := range batch.Rollups {
		if a.Allowed < 0 || a.Denied < 0 || !domain.ValidAccessDay(a.Day) {
			return domain.ErrInvalidInput
		}
		rows[i] = map[string]any{"key": a.Key(), "client": a.ClientID, "project": a.ProjectID, "capability": a.Capability, "day": a.Day, "allowed": a.Allowed, "denied": a.Denied, "first": a.FirstAt.UTC().UnixNano(), "last": a.LastAt.UTC().UnixNano()}
	}
	_, err := r.query(ctx, tx, `OPTIONAL MATCH (stored:AccessBatch {id:$batch})
WITH stored WHERE stored IS NULL
CREATE (marker:AccessBatch {id:$batch, at:$at})
WITH marker
UNWIND $rows AS r
MERGE (a:AccessRollup {key:r.key})
ON CREATE SET a.clientID=r.client, a.projectID=r.project, a.capability=r.capability, a.day=r.day, a.allowed=0, a.denied=0, a.firstAt=r.first, a.lastAt=r.last
SET a.allowed=a.allowed+r.allowed, a.denied=a.denied+r.denied,
    a.firstAt=CASE WHEN r.first < a.firstAt THEN r.first ELSE a.firstAt END,
    a.lastAt=CASE WHEN r.last > a.lastAt THEN r.last ELSE a.lastAt END`, map[string]any{"batch": batch.ID, "at": batch.At.UTC().UnixNano(), "rows": rows})
	return err
}

func (r *AccessRepository) ListAccess(ctx context.Context, tx domain.Tx, q domain.AccessQuery) ([]domain.AccessRollup, error) {
	rows, err := r.query(ctx, tx, `MATCH (a:AccessRollup)
WHERE a.day >= $since AND ($client = '' OR a.clientID = $client) AND ($project = '' OR a.projectID = $project) AND ($after = '' OR a.key < $after)
RETURN a.clientID, a.projectID, a.capability, a.day, a.allowed, a.denied, a.firstAt, a.lastAt
ORDER BY a.key DESC LIMIT $limit`, map[string]any{"since": q.Since, "client": q.ClientID, "project": q.ProjectID, "after": q.After, "limit": int64(q.Limit)})
	if err != nil {
		return nil, err
	}
	out := make([]domain.AccessRollup, 0, len(rows))
	for _, row := range rows {
		v := row.Values
		client, ok1 := v[0].(string)
		project, ok2 := v[1].(string)
		capability, ok3 := v[2].(string)
		day, ok4 := v[3].(string)
		allowed, ok5 := v[4].(int64)
		denied, ok6 := v[5].(int64)
		first, ok7 := v[6].(int64)
		last, ok8 := v[7].(int64)
		if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 || !ok7 || !ok8 {
			return nil, domain.ErrUnavailable
		}
		out = append(out, domain.AccessRollup{ClientID: client, ProjectID: project, Capability: capability, Day: day, Allowed: allowed, Denied: denied, FirstAt: time.Unix(0, first).UTC(), LastAt: time.Unix(0, last).UTC()})
	}
	return out, nil
}

// PruneAccess deletes up to limit rollups from days before beforeDay, and up
// to limit batch markers written before batchesBefore. It returns the number
// of rollups deleted.
func (r *AccessRepository) PruneAccess(ctx context.Context, tx domain.Tx, beforeDay string, batchesBefore time.Time, limit int) (int, error) {
	if !domain.ValidAccessDay(beforeDay) || batchesBefore.IsZero() || limit < 1 {
		return 0, domain.ErrInvalidInput
	}
	if _, err := r.query(ctx, tx, `MATCH (b:AccessBatch) WHERE b.at < $before WITH b LIMIT $limit DELETE b`, map[string]any{"before": batchesBefore.UTC().UnixNano(), "limit": int64(limit)}); err != nil {
		return 0, err
	}
	rows, err := r.query(ctx, tx, `MATCH (a:AccessRollup) WHERE a.day < $before WITH a LIMIT $limit DELETE a RETURN count(*)`, map[string]any{"before": beforeDay, "limit": int64(limit)})
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

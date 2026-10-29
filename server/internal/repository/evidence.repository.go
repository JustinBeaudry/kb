package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
)

type EvidenceRepository struct{ store *graph.Store }

func NewEvidenceRepository(s *graph.Store) *EvidenceRepository { return &EvidenceRepository{store: s} }
func (r *EvidenceRepository) query(ctx context.Context, tx domain.Tx, q string, p map[string]any) ([]*neo4j.Record, error) {
	native, err := r.store.Unwrap(tx)
	if err != nil {
		return nil, err
	}
	res, err := native.Run(ctx, q, p)
	if err != nil {
		return nil, domain.ErrUnavailable
	}
	rows, err := res.Collect(ctx)
	if err != nil {
		return nil, domain.ErrUnavailable
	}
	return rows, nil
}
func (r *EvidenceRepository) Policy(ctx context.Context, tx domain.Tx, project string) (domain.Policy, error) {
	rows, err := r.query(ctx, tx, `MATCH (p:CapturePolicy {projectID:$project}) RETURN p.data`, map[string]any{"project": project})
	if err != nil {
		return domain.Policy{}, err
	}
	if len(rows) != 1 {
		return domain.Policy{}, domain.ErrForbidden
	}
	var p domain.Policy
	if decodeCapture(rows[0], 0, &p) != nil {
		return p, domain.ErrUnavailable
	}
	return p, nil
}
func decodeCapture(row *neo4j.Record, index int, dest any) error {
	if len(row.Values) <= index {
		return domain.ErrUnavailable
	}
	s, ok := row.Values[index].(string)
	if !ok || json.Unmarshal([]byte(s), dest) != nil {
		return domain.ErrUnavailable
	}
	return nil
}
func (r *EvidenceRepository) Artifact(ctx context.Context, tx domain.Tx, id string) (domain.Artifact, string, error) {
	rows, err := r.query(ctx, tx, `MATCH (a:Artifact {id:$id}) RETURN a.data,a.clientID,coalesce(a.available,false),coalesce(a.uploadLeaseID,''),coalesce(a.uploadLeaseUntil,0)`, map[string]any{"id": id})
	if err != nil {
		return domain.Artifact{}, "", err
	}
	if len(rows) == 0 {
		return domain.Artifact{}, "", domain.ErrIncomplete
	}
	var a domain.Artifact
	if decodeCapture(rows[0], 0, &a) != nil {
		return a, "", domain.ErrUnavailable
	}
	client, ok := rows[0].Values[1].(string)
	if !ok {
		return a, "", domain.ErrUnavailable
	}
	a.ObjectAvailable, _ = rows[0].Values[2].(bool)
	a.UploadLeaseID, _ = rows[0].Values[3].(string)
	if leaseUntil, ok := rows[0].Values[4].(int64); ok && leaseUntil > 0 {
		a.UploadLeaseUntil = time.Unix(0, leaseUntil).UTC()
	}
	return a, client, nil
}
func (r *EvidenceRepository) SaveArtifact(ctx context.Context, tx domain.Tx, a domain.Artifact, client string) error {
	data, _ := json.Marshal(a)
	_, err := r.query(ctx, tx, `MERGE (a:Artifact {id:$id}) SET a.data=$data,a.clientID=$client,a.state=$state,a.projectID=$project,a.taskID=$task,a.available=$available,a.objectKey=$key,a.uploadLeaseID=$lease,a.uploadLeaseUntil=$leaseUntil`, map[string]any{"key": a.ObjectKey, "lease": a.UploadLeaseID, "leaseUntil": leaseTime(a.UploadLeaseUntil), "available": a.ObjectAvailable, "id": a.ID, "data": string(data), "client": client, "state": string(a.State), "project": a.Scope.ProjectID, "task": a.Scope.TaskID})
	return err
}
func (r *EvidenceRepository) Event(ctx context.Context, tx domain.Tx, key string) (domain.Event, domain.Origin, error) {
	rows, err := r.query(ctx, tx, `MATCH (e:Event {captureKey:$key}) RETURN e.data,e.origin`, map[string]any{"key": key})
	if err != nil {
		return domain.Event{}, domain.Origin{}, err
	}
	if len(rows) == 0 {
		return domain.Event{}, domain.Origin{}, domain.ErrIncomplete
	}
	var e domain.Event
	var o domain.Origin
	if decodeCapture(rows[0], 0, &e) != nil || decodeCapture(rows[0], 1, &o) != nil {
		return e, o, domain.ErrUnavailable
	}
	return e, o, nil
}
func (r *EvidenceRepository) SaveEvent(ctx context.Context, tx domain.Tx, key string, e domain.Event, o domain.Origin) error {
	data, _ := json.Marshal(e)
	origin, _ := json.Marshal(o)
	_, err := r.query(ctx, tx, `CREATE (e:Event {id:$id,captureKey:$key,data:$data,origin:$origin,clientID:$client,idempotencyKey:$key,projectID:$project,taskID:$task})`, map[string]any{"id": e.ID, "key": key, "data": string(data), "origin": string(origin), "client": o.ClientID, "project": e.Scope.ProjectID, "task": e.Scope.TaskID})
	return err
}
func (r *EvidenceRepository) Pending(ctx context.Context, tx domain.Tx, limit int) ([]domain.Artifact, error) {
	rows, err := r.query(ctx, tx, `MATCH (a:Artifact) WHERE a.state IN ['staged','ready','abandoning'] RETURN a.data,coalesce(a.available,false),coalesce(a.uploadLeaseID,''),coalesce(a.uploadLeaseUntil,0) ORDER BY coalesce(a.checkedAt,0),a.id LIMIT $limit`, map[string]any{"limit": limit})
	if err != nil {
		return nil, err
	}
	artifacts := make([]domain.Artifact, 0, len(rows))
	for _, row := range rows {
		var a domain.Artifact
		if decodeCapture(row, 0, &a) != nil {
			return nil, domain.ErrUnavailable
		}
		a.ObjectAvailable, _ = row.Values[1].(bool)
		a.UploadLeaseID, _ = row.Values[2].(string)
		if until, ok := row.Values[3].(int64); ok && until > 0 {
			a.UploadLeaseUntil = time.Unix(0, until).UTC()
		}
		artifacts = append(artifacts, a)
	}
	return artifacts, nil
}

// Checked and Availability fence mutable health/scheduling to an object generation.
func (r *EvidenceRepository) Checked(ctx context.Context, tx domain.Tx, id, key string, at int64) error {
	rows, err := r.query(ctx, tx, `MATCH (a:Artifact {id:$id}) WHERE a.objectKey=$key SET a.checkedAt=$at RETURN a.id`, map[string]any{"id": id, "key": key, "at": at})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrStale
	}
	return nil
}
func (r *EvidenceRepository) Availability(ctx context.Context, tx domain.Tx, id, key string, available bool) error {
	rows, err := r.query(ctx, tx, `MATCH (a:Artifact {id:$id}) WHERE a.objectKey=$key SET a.available=$available RETURN a.id`, map[string]any{"id": id, "key": key, "available": available})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrStale
	}
	return nil
}
func (r *EvidenceRepository) UploadLease(ctx context.Context, tx domain.Tx, id, key, lease string, until time.Time) error {
	rows, err := r.query(ctx, tx, `MATCH (a:Artifact {id:$id}) WHERE a.objectKey=$key SET a.uploadLeaseID=$lease,a.uploadLeaseUntil=$until RETURN a.id`, map[string]any{"id": id, "key": key, "lease": lease, "until": leaseTime(until)})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrStale
	}
	return nil
}
func leaseTime(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

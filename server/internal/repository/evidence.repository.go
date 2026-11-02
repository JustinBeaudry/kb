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
	rows, err := r.query(ctx, tx, `MATCH (a:Artifact {id:$id}) OPTIONAL MATCH (p:CapturePolicy {projectID:a.projectID}) OPTIONAL MATCH (e:PolicyEligibility {artifactID:a.id,generation:a.generation,digest:a.digest,policyRevision:p.revision,eligible:true}) RETURN a.data,a.clientID,coalesce(a.available,false),coalesce(a.uploadLeaseID,''),coalesce(a.uploadLeaseUntil,0),a.state,EXISTS { MATCH (:Tombstone {id:a.id}) } OR EXISTS {MATCH (:SourceTombstone {projectID:a.projectID,digest:a.digest})} OR EXISTS {MATCH (:SessionTombstone {projectID:a.projectID,id:a.sessionID})} OR EXISTS {MATCH (:TaskTombstone {projectID:a.projectID,id:a.taskID})},coalesce(e.policyRevision,'')`, map[string]any{"id": id})
	if err != nil {
		return domain.Artifact{}, "", err
	}
	if len(rows) == 0 {
		return domain.Artifact{}, "", domain.ErrIncomplete
	}
	if rows[0].Values[6] == true {
		return domain.Artifact{}, "", domain.ErrDeleted
	}
	var a domain.Artifact
	if decodeCapture(rows[0], 0, &a) != nil {
		return a, "", domain.ErrUnavailable
	}
	client, ok := rows[0].Values[1].(string)
	if !ok {
		return a, "", domain.ErrUnavailable
	}
	if rows[0].Values[6] == true {
		return domain.Artifact{}, "", domain.ErrDeleted
	}
	state, ok := rows[0].Values[5].(string)
	if !ok {
		return domain.Artifact{}, "", domain.ErrUnavailable
	}
	a.CurrentPolicyRevision, _ = rows[0].Values[7].(string)
	a.State = domain.ArtifactState(state)
	a.ObjectAvailable, _ = rows[0].Values[2].(bool)
	a.UploadLeaseID, _ = rows[0].Values[3].(string)
	if leaseUntil, ok := rows[0].Values[4].(int64); ok && leaseUntil > 0 {
		a.UploadLeaseUntil = time.Unix(0, leaseUntil).UTC()
	}
	return a, client, nil
}
func (r *EvidenceRepository) SaveArtifact(ctx context.Context, tx domain.Tx, a domain.Artifact, client string) error {
	data, _ := json.Marshal(a)
	now := time.Now().UTC().UnixNano()
	rows, err := r.query(ctx, tx, `WITH 1 AS ignored WHERE NOT EXISTS {MATCH (:Tombstone {id:$id})} AND NOT EXISTS {MATCH (:SourceTombstone {projectID:$project,digest:$digest})} AND NOT EXISTS {MATCH (:SessionTombstone {projectID:$project,id:$session})} AND NOT EXISTS {MATCH (:TaskTombstone {projectID:$project,id:$task})} MERGE (a:Artifact {id:$id}) ON CREATE SET a.ingestedAt=$now SET a.data=$data,a.clientID=$client,a.state=$state,a.projectID=$project,a.taskID=$task,a.sessionID=$session,a.available=$available,a.objectKey=$key,a.generation=$generation,a.digest=$digest,a.policyRevision=$policy,a.sourcePath=$sourcePath,a.bytes=$bytes,a.lifecycleMetadataVersion=1,a.uploadLeaseID=$lease,a.uploadLeaseUntil=$leaseUntil RETURN a.id`, map[string]any{"key": a.ObjectKey, "lease": a.UploadLeaseID, "leaseUntil": leaseTime(a.UploadLeaseUntil), "available": a.ObjectAvailable, "id": a.ID, "data": string(data), "client": client, "state": string(a.State), "project": a.Scope.ProjectID, "task": a.Scope.TaskID, "session": a.Scope.SessionID, "generation": a.Generation, "digest": a.Digest, "now": now, "policy": a.PolicyRevision, "sourcePath": a.SourcePath, "bytes": a.Bytes})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrDeleted
	}
	if a.Scope.TaskID != "" {
		_, err = r.query(ctx, tx, `MATCH (a:Artifact {id:$id}) WHERE a.ingestedAt=$now MERGE (t:TaskActivity {identityDigest:$taskIdentity}) ON CREATE SET t.projectID=$project,t.taskID=$task SET t.lastActivityAt=$now`, map[string]any{"id": a.ID, "now": now, "project": a.Scope.ProjectID, "task": a.Scope.TaskID, "taskIdentity": domain.LifecycleIdentityDigest(a.Scope.ProjectID, a.Scope.TaskID)})
	}
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
	_, err := r.query(ctx, tx, `CREATE (e:Event {id:$id,captureKey:$key,data:$data,origin:$origin,clientID:$client,idempotencyKey:$key,projectID:$project,taskID:$task,sessionID:$session,payloadArtifactID:$artifact,ingestedAt:$now})`, map[string]any{"session": e.Scope.SessionID, "artifact": e.PayloadArtifactID, "now": time.Now().UTC().UnixNano(), "id": e.ID, "key": key, "data": string(data), "origin": string(origin), "client": o.ClientID, "project": e.Scope.ProjectID, "task": e.Scope.TaskID})
	if err == nil && e.Scope.TaskID != "" {
		_, err = r.query(ctx, tx, `MERGE (t:TaskActivity {identityDigest:$taskIdentity}) ON CREATE SET t.projectID=$project,t.taskID=$task SET t.lastActivityAt=$now`, map[string]any{"project": e.Scope.ProjectID, "task": e.Scope.TaskID, "taskIdentity": domain.LifecycleIdentityDigest(e.Scope.ProjectID, e.Scope.TaskID), "now": time.Now().UTC().UnixNano()})
	}
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
	rows, err := r.query(ctx, tx, `MATCH (a:Artifact {id:$id}) WHERE a.objectKey=$key AND a.state <> 'deleted' AND NOT EXISTS {MATCH (:Tombstone {id:a.id})} SET a.available=$available RETURN a.id`, map[string]any{"id": id, "key": key, "available": available})
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

// BindDerivedArtifact runs before Put in the same staging transaction and again
// at settlement. Owner deletion invalidates reservations before any publication.
func (r *EvidenceRepository) BindDerivedArtifact(ctx context.Context, tx domain.Tx, id string, a domain.Artifact) error {
	rows, err := r.query(ctx, tx, `MATCH (d:PreparedDerivation {id:$id}),(s:Artifact {id:d.sourceArtifactID}),(e:Artifact {id:$artifact}) WHERE d.state IN ['prepared','finalized'] AND d.projectID=$project AND d.excerptDigest=$digest AND s.generation=d.sourceGeneration AND s.digest=d.sourceDigest AND s.state='ready' AND coalesce(s.available,false) AND NOT EXISTS {MATCH (:Tombstone {id:s.id})} AND NOT EXISTS {MATCH (:SourceTombstone {projectID:s.projectID,digest:s.digest})} AND (coalesce(d.excerptArtifactID,'')='' OR (d.excerptArtifactID=$artifact AND d.excerptGeneration=$generation)) SET d.excerptArtifactID=$artifact,d.excerptGeneration=$generation MERGE (d)-[:PINS_EXCERPT]->(e) RETURN d.id`, map[string]any{"id": id, "artifact": a.ID, "project": a.Scope.ProjectID, "digest": a.Digest, "generation": a.Generation})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrDeleted
	}
	return nil
}

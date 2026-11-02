package repository

import (
	"context"
	"encoding/json"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"time"
)

type CheckpointRepository struct{ store *graph.Store }

func NewCheckpointRepository(store *graph.Store) *CheckpointRepository {
	return &CheckpointRepository{store}
}
func (r *CheckpointRepository) Get(ctx context.Context, tx domain.Tx, id string) (domain.Checkpoint, string, error) {
	native, err := r.store.Unwrap(tx)
	if err != nil {
		return domain.Checkpoint{}, "", err
	}
	res, err := native.Run(ctx, `MATCH (c:Checkpoint {id:$id}) WHERE NOT EXISTS { MATCH (:Tombstone {id:c.id}) } RETURN c.data,c.clientID`, map[string]any{"id": id})
	if err != nil {
		return domain.Checkpoint{}, "", domain.ErrUnavailable
	}
	rows, err := res.Collect(ctx)
	if err != nil {
		return domain.Checkpoint{}, "", domain.ErrUnavailable
	}
	if len(rows) == 0 {
		return domain.Checkpoint{}, "", nil
	}
	var c domain.Checkpoint
	if decodeCapture(rows[0], 0, &c) != nil {
		return c, "", domain.ErrUnavailable
	}
	client, ok := rows[0].Values[1].(string)
	if !ok {
		return c, "", domain.ErrUnavailable
	}
	return c, client, nil
}
func (r *CheckpointRepository) TaskOwner(ctx context.Context, tx domain.Tx, scope domain.Scope) (string, error) {
	native, err := r.store.Unwrap(tx)
	if err != nil {
		return "", err
	}
	res, err := native.Run(ctx, `MATCH (t:TaskCheckpointOwner {projectID:$project,taskID:$task}) RETURN t.clientID`, map[string]any{"project": scope.ProjectID, "task": scope.TaskID})
	if err != nil {
		return "", domain.ErrUnavailable
	}
	rows, err := res.Collect(ctx)
	if err != nil {
		return "", domain.ErrUnavailable
	}
	if len(rows) == 0 {
		return "", nil
	}
	owner, ok := rows[0].Values[0].(string)
	if !ok {
		return "", domain.ErrUnavailable
	}
	return owner, nil
}
func (r *CheckpointRepository) Save(ctx context.Context, tx domain.Tx, c domain.Checkpoint, client string) error {
	native, err := r.store.Unwrap(tx)
	if err != nil {
		return err
	}
	data, _ := json.Marshal(c)
	res, err := native.Run(ctx, `MERGE (t:TaskCheckpointOwner {projectID:$project,taskID:$task}) ON CREATE SET t.clientID=$client WITH t WHERE t.clientID=$client AND NOT EXISTS { MATCH (:Tombstone {id:$id}) } AND NOT EXISTS {MATCH (:Tombstone {id:$revision})} AND NOT EXISTS {MATCH (:SessionTombstone {projectID:$project,id:$session})} AND NOT EXISTS {MATCH (:TaskTombstone {projectID:$project,id:$task})} SET t.headID=$id,t.headRevision=$revision MERGE (c:Checkpoint {id:$id}) SET c.data=$data,c.clientID=$client,c.projectID=$project,c.taskID=$task,c.sessionID=$session,c.ingestedAt=coalesce(c.ingestedAt,$now) MERGE (activity:TaskActivity {identityDigest:$taskIdentity}) ON CREATE SET activity.projectID=$project,activity.taskID=$task SET activity.lastActivityAt=$now RETURN c.id`, map[string]any{"session": c.Scope.SessionID, "taskIdentity": domain.LifecycleIdentityDigest(c.Scope.ProjectID, c.Scope.TaskID), "now": time.Now().UTC().UnixNano(), "id": c.ID, "revision": c.Revision, "data": string(data), "client": client, "project": c.Scope.ProjectID, "task": c.Scope.TaskID})
	if err != nil {
		return domain.ErrUnavailable
	}
	_, err = res.Single(ctx)
	if err != nil {
		return domain.ErrDeleted
	}
	return nil
}

func (r *CheckpointRepository) Latest(ctx context.Context, tx domain.Tx, scope domain.Scope) (domain.Checkpoint, string, error) {
	native, e := r.store.Unwrap(tx)
	if e != nil {
		return domain.Checkpoint{}, "", e
	}
	res, e := native.Run(ctx, `MATCH (t:TaskCheckpointOwner {projectID:$project,taskID:$task}) MATCH (c:Checkpoint {id:t.headID}) WHERE NOT EXISTS { MATCH (:Tombstone {id:c.id}) } RETURN c.data,c.clientID`, map[string]any{"project": scope.ProjectID, "task": scope.TaskID})
	if e != nil {
		return domain.Checkpoint{}, "", domain.ErrUnavailable
	}
	rows, e := res.Collect(ctx)
	if e != nil {
		return domain.Checkpoint{}, "", domain.ErrUnavailable
	}
	if len(rows) == 0 {
		return domain.Checkpoint{}, "", nil
	}
	var c domain.Checkpoint
	if decodeCapture(rows[0], 0, &c) != nil {
		return c, "", domain.ErrUnavailable
	}
	client, ok := rows[0].Values[1].(string)
	if !ok {
		return c, "", domain.ErrUnavailable
	}
	return c, client, nil
}

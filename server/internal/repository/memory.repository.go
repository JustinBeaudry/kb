package repository

import (
	"context"
	"encoding/json"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"time"
)

type MemoryRepository struct{ store *graph.Store }

func NewMemoryRepository(s *graph.Store) *MemoryRepository { return &MemoryRepository{s} }
func (r *MemoryRepository) Head(ctx context.Context, tx domain.Tx, id string) (domain.ClaimRevision, string, error) {
	native, e := r.store.Unwrap(tx)
	if e != nil {
		return domain.ClaimRevision{}, "", e
	}
	deleted, e := native.Run(ctx, `MATCH (:Tombstone {id:$id}) RETURN 1`, map[string]any{"id": id})
	if e != nil {
		return domain.ClaimRevision{}, "", domain.ErrUnavailable
	}
	tombstones, e := deleted.Collect(ctx)
	if e != nil {
		return domain.ClaimRevision{}, "", domain.ErrUnavailable
	}
	if len(tombstones) > 0 {
		return domain.ClaimRevision{}, "", domain.ErrForbidden
	}
	res, e := native.Run(ctx, `MATCH (h:Claim {id:$id})-[:HEAD]->(c:ClaimRevision) RETURN c.data,c.clientID,EXISTS {MATCH (:Tombstone {id:c.id})}`, map[string]any{"id": id})
	if e != nil {
		return domain.ClaimRevision{}, "", domain.ErrUnavailable
	}
	rows, e := res.Collect(ctx)
	if e != nil {
		return domain.ClaimRevision{}, "", domain.ErrUnavailable
	}
	if len(rows) == 0 {
		return domain.ClaimRevision{}, "", nil
	}
	if rows[0].Values[2] == true {
		return domain.ClaimRevision{}, "", domain.ErrDeleted
	}
	var c domain.ClaimRevision
	if decodeCapture(rows[0], 0, &c) != nil {
		return c, "", domain.ErrUnavailable
	}
	client, ok := rows[0].Values[1].(string)
	if !ok {
		return c, "", domain.ErrUnavailable
	}
	return c, client, nil
}
func (r *MemoryRepository) PutCandidate(ctx context.Context, tx domain.Tx, c domain.ClaimRevision) error {
	if c.Status != domain.ClaimCandidate || c.Scope.TaskID == "" {
		return domain.ErrInvalidInput
	}
	native, e := r.store.Unwrap(tx)
	if e != nil {
		return e
	}
	data, e := json.Marshal(c)
	if e != nil {
		return domain.ErrInvalidInput
	}
	// The owning client is the task's atomic owner, not an evidence uploader.
	res, e := native.Run(ctx, `MERGE (t:TaskCheckpointOwner {projectID:$project,taskID:$task}) ON CREATE SET t.clientID=$client WITH t WHERE t.clientID=$client AND NOT EXISTS {MATCH (:TaskTombstone {projectID:$project,id:$task})} AND NOT EXISTS {MATCH (:SessionTombstone {projectID:$project,id:$session})} AND NOT EXISTS {MATCH (:Tombstone {id:$id})} AND NOT EXISTS {MATCH (:Tombstone {id:$claim})} MERGE (h:Claim {id:$claim}) OPTIONAL MATCH (h)-[old:HEAD]->() DELETE old CREATE (c:ClaimRevision {id:$id,claimID:$claim,data:$data,status:'candidate',valid:false,projectID:$project,taskID:$task,clientID:$client,text:$text,kind:$kind,sessionID:$session,ingestedAt:$now}) CREATE (h)-[:HEAD]->(c) MERGE (activity:TaskActivity {identityDigest:$taskIdentity}) ON CREATE SET activity.projectID=$project,activity.taskID=$task SET activity.lastActivityAt=$now WITH c MATCH (o:OwnerState {id:'owner'}) SET o.revisionEpoch=o.revisionEpoch+1 RETURN c.id`, map[string]any{"now": time.Now().UTC().UnixNano(), "session": c.Scope.SessionID, "taskIdentity": domain.LifecycleIdentityDigest(c.Scope.ProjectID, c.Scope.TaskID), "project": c.Scope.ProjectID, "task": c.Scope.TaskID, "client": candidateClient(c), "claim": c.ClaimID, "id": c.Revision, "data": string(data), "text": c.Text, "kind": c.Kind})
	if e != nil {
		return domain.ErrUnavailable
	}
	if _, e = res.Single(ctx); e != nil {
		return domain.ErrForbidden
	}
	for _, dependency := range c.DependsOn {
		res, e = native.Run(ctx, `MATCH (c:ClaimRevision {id:$id}),(d:ClaimRevision {id:$dependency}) WHERE d.valid=true AND d.status='verified' AND NOT EXISTS {MATCH (:Tombstone {id:d.id})} AND NOT EXISTS {MATCH (:Tombstone {id:d.claimID})} MERGE (c)-[:DEPENDS_ON]->(d) RETURN d.id`, map[string]any{"id": c.Revision, "dependency": dependency})
		if e != nil {
			return domain.ErrUnavailable
		}
		if _, e = res.Single(ctx); e != nil {
			return domain.ErrDeleted
		}
	}
	res, e = native.Run(ctx, `MATCH (c:ClaimRevision {id:$id}) SET c.dependencyProjectionVersion=1`, map[string]any{"id": c.Revision})
	if e != nil {
		return domain.ErrUnavailable
	}
	if _, e = res.Consume(ctx); e != nil {
		return domain.ErrUnavailable
	}
	for _, entity := range c.EntityIDs {
		res, e = native.Run(ctx, `MATCH (c:ClaimRevision {id:$id}) MERGE (e:Entity {projectID:$project,id:$entity}) MERGE (c)-[:ABOUT]->(e)`, map[string]any{"id": c.Revision, "project": c.Scope.ProjectID, "entity": entity})
		if e != nil {
			return domain.ErrUnavailable
		}
		if _, e = res.Consume(ctx); e != nil {
			return domain.ErrUnavailable
		}
	}
	for _, ref := range c.Evidence {
		res, e = native.Run(ctx, `MATCH (c:ClaimRevision {id:$id}),(a:Artifact {id:$artifact}) CREATE (c)-[:EVIDENCE {digest:$digest,locator:$locator}]->(a)`, map[string]any{"id": c.Revision, "artifact": ref.ArtifactID, "digest": ref.Digest, "locator": ref.Locator})
		if e != nil {
			return domain.ErrUnavailable
		}
		if _, e = res.Consume(ctx); e != nil {
			return domain.ErrUnavailable
		}
	}
	return nil
}
func candidateClient(c domain.ClaimRevision) string { return c.SubmittedBy }

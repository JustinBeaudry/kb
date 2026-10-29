package repository

import (
	"context"
	"encoding/json"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
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
	res, e := native.Run(ctx, `MATCH (h:Claim {id:$id})-[:HEAD]->(c:ClaimRevision) RETURN c.data,c.clientID`, map[string]any{"id": id})
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
	res, e := native.Run(ctx, `MERGE (t:TaskCheckpointOwner {projectID:$project,taskID:$task}) ON CREATE SET t.clientID=$client WITH t WHERE t.clientID=$client MERGE (h:Claim {id:$claim}) OPTIONAL MATCH (h)-[old:HEAD]->() DELETE old CREATE (c:ClaimRevision {id:$id,claimID:$claim,data:$data,status:'candidate',valid:false,projectID:$project,taskID:$task,clientID:$client,text:$text,kind:$kind}) CREATE (h)-[:HEAD]->(c) WITH c MATCH (o:OwnerState {id:'owner'}) SET o.revisionEpoch=o.revisionEpoch+1 RETURN c.id`, map[string]any{"project": c.Scope.ProjectID, "task": c.Scope.TaskID, "client": candidateClient(c), "claim": c.ClaimID, "id": c.Revision, "data": string(data), "text": c.Text, "kind": c.Kind})
	if e != nil {
		return domain.ErrUnavailable
	}
	if _, e = res.Single(ctx); e != nil {
		return domain.ErrForbidden
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

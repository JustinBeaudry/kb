package repository

import (
	"context"
	"encoding/json"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
)

type PublicationRepository struct{ store *graph.Store }

func NewPublicationRepository(s *graph.Store) *PublicationRepository {
	return &PublicationRepository{s}
}
func (r *PublicationRepository) query(ctx context.Context, tx domain.Tx, q string, p map[string]any) ([]*neo4j.Record, error) {
	native, err := r.store.Unwrap(tx)
	if err != nil {
		return nil, err
	}
	result, err := native.Run(ctx, q, p)
	if err != nil {
		return nil, domain.ErrUnavailable
	}
	rows, err := result.Collect(ctx)
	if err != nil {
		return nil, domain.ErrUnavailable
	}
	return rows, nil
}
func (r *PublicationRepository) PutTarget(ctx context.Context, tx domain.Tx, c domain.ClaimRevision, expected string) error {
	if c.Scope.TaskID != "" || c.Scope.SessionID != "" || c.Status != domain.ClaimVerified || len(c.Evidence) == 0 {
		return domain.ErrInvalidInput
	}
	data, err := json.Marshal(c)
	if err != nil {
		return domain.ErrInvalidInput
	}
	rows, err := r.query(ctx, tx, `MERGE (h:Claim {id:$claim}) WITH h OPTIONAL MATCH (h)-[old:HEAD]->(previous:ClaimRevision) WITH h,old,previous WHERE coalesce(previous.id,'')=$expected DELETE old CREATE (c:ClaimRevision {id:$id,claimID:$claim,data:$data,status:'verified',valid:true,projectID:$project,taskID:'',clientID:'',text:$text,kind:$kind}) CREATE (h)-[:HEAD]->(c) RETURN c.id`, map[string]any{"id": c.Revision, "claim": c.ClaimID, "data": string(data), "project": c.Scope.ProjectID, "text": c.Text, "kind": c.Kind, "expected": expected})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrConflict
	}
	for _, dependency := range c.DependsOn {
		rows, err = r.query(ctx, tx, `MATCH (c:ClaimRevision {id:$id}),(d:ClaimRevision {id:$dependency}) WHERE d.valid=true AND d.status='verified' AND NOT EXISTS {MATCH (:Tombstone {id:d.id})} AND NOT EXISTS {MATCH (:Tombstone {id:d.claimID})} MERGE (c)-[:DEPENDS_ON]->(d) RETURN d.id`, map[string]any{"id": c.Revision, "dependency": dependency})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return domain.ErrDeleted
		}
	}
	if _, err = r.query(ctx, tx, `MATCH (c:ClaimRevision {id:$id}) SET c.dependencyProjectionVersion=1`, map[string]any{"id": c.Revision}); err != nil {
		return err
	}
	for _, entity := range c.EntityIDs {
		if _, err = r.query(ctx, tx, `MATCH (c:ClaimRevision {id:$id}) MERGE (e:Entity {projectID:$project,id:$entity}) MERGE (c)-[:ABOUT]->(e)`, map[string]any{"id": c.Revision, "project": c.Scope.ProjectID, "entity": entity}); err != nil {
			return err
		}
	}
	for _, ref := range c.Evidence {
		rows, err = r.query(ctx, tx, `MATCH (c:ClaimRevision {id:$id}),(a:Artifact {id:$artifact}) CREATE (c)-[:EVIDENCE {digest:$digest,locator:$locator}]->(a) RETURN a.id`, map[string]any{"id": c.Revision, "artifact": ref.ArtifactID, "digest": ref.Digest, "locator": ref.Locator})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return domain.ErrIncomplete
		}
	}
	return nil
}
func (r *PublicationRepository) PutPublication(ctx context.Context, tx domain.Tx, p domain.Publication) error {
	if p.SharingScope != "project" || len(p.SupportRevisions) < 1 || len(p.SupportRevisions) > 1000 {
		return domain.ErrInvalidInput
	}
	data, err := json.Marshal(p)
	if err != nil {
		return domain.ErrInvalidInput
	}
	rows, err := r.query(ctx, tx, `MATCH (c:ClaimRevision {id:$target}) CREATE (p:Publication {id:$id,claimRevision:$target,revision:$revision,policyRevision:$policy,sharingScope:'project',valid:true,supportRevisions:$support,data:$data}) CREATE (p)-[:PUBLISHES]->(c) RETURN p.id`, map[string]any{"id": p.ID, "target": p.ClaimRevision, "revision": p.Revision, "policy": p.PolicyRevision, "support": p.SupportRevisions, "data": string(data)})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrIncomplete
	}
	return nil
}
func (r *PublicationRepository) PutExcerptLineage(ctx context.Context, tx domain.Tx, l domain.ExcerptLineage) error {
	data, err := json.Marshal(l)
	if err != nil {
		return domain.ErrInvalidInput
	}
	rows, err := r.query(ctx, tx, `MATCH (s:Artifact {id:$source}),(e:Artifact {id:$excerpt}),(c:ClaimRevision {id:$support}) CREATE (l:ExcerptLineage {id:$id,data:$data,decisionID:$decision,inputDigest:$digest}) CREATE (l)-[:PINS_SOURCE]->(s) CREATE (l)-[:PINS_EXCERPT]->(e) CREATE (l)-[:PINS_SUPPORT]->(c) RETURN l.id`, map[string]any{"id": l.ID, "data": string(data), "decision": l.DecisionID, "digest": l.InputDigest, "source": l.SourceArtifactID, "excerpt": l.ExcerptArtifactID, "support": l.SupportRevision})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrIncomplete
	}
	return nil
}
func (r *PublicationRepository) InvalidateSupport(ctx context.Context, tx domain.Tx, id string) error {
	rows, err := r.query(ctx, tx, `MATCH (c:ClaimRevision {id:$id}) SET c.valid=false,c.status='invalidated' RETURN c.id`, map[string]any{"id": id})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrIncomplete
	}
	// Complete stored closures permit a database-side update without hydrating an
	// unbounded dependent fanout into Go. OwnerState serializes this with publication.
	_, err = r.query(ctx, tx, `MATCH (p:Publication) WHERE p.claimRevision=$id OR $id IN p.supportRevisions SET p.valid=false WITH p MATCH (target:ClaimRevision {id:p.claimRevision}) SET target.valid=false,target.status='invalidated'`, map[string]any{"id": id})
	return err
}

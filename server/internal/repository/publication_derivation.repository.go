package repository

import (
	"context"
	"encoding/json"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// ReserveDerivation commits immutable original provenance before an excerpt
// object upload. BindDerivedArtifact attaches its generation in the same F4
// staging transaction; lifecycle deletion can therefore find pending excerpts.
func (r *PublicationRepository) ReserveDerivation(ctx context.Context, tx domain.Tx, project string, l domain.ExcerptLineage) error {
	if l.ExcerptArtifactID != "" || l.ExcerptGeneration != "" || l.ID == "" || l.SupportRevision == "" {
		return domain.ErrInvalidInput
	}
	data, err := json.Marshal(l)
	if err != nil {
		return domain.ErrInvalidInput
	}
	rows, err := r.query(ctx, tx, `MATCH (d:PreparedDerivation {id:$id}) RETURN d.data,d.state,d.projectID`, map[string]any{"id": l.ID})
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		state, _ := rows[0].Values[1].(string)
		stored, _ := rows[0].Values[0].(string)
		if state == "deleted" {
			return domain.ErrDeleted
		}
		storedProject, _ := rows[0].Values[2].(string)
		if storedProject != project || stored != string(data) || (state != "prepared" && state != "finalized") {
			return domain.ErrConflict
		}
		return nil
	}
	rows, err = r.query(ctx, tx, `MATCH (s:Artifact {id:$source}),(c:ClaimRevision {id:$support}) WHERE s.projectID=$project AND c.projectID=$project AND s.generation=$generation AND s.digest=$digest AND s.state='ready' AND s.available=true AND s.deletionOperation IS NULL AND c.deletionOperation IS NULL AND c.status<>'invalidated' AND NOT EXISTS {MATCH (:Tombstone {id:s.id})} AND NOT EXISTS {MATCH (:SourceTombstone {projectID:$project,digest:s.digest})} CREATE (d:PreparedDerivation {id:$id,projectID:$project,sourceArtifactID:$source,sourceGeneration:$generation,sourceDigest:$digest,supportRevision:$support,excerptDigest:$excerptDigest,decisionID:$decision,inputDigest:$input,state:'prepared',data:$data}) CREATE (d)-[:PINS_SOURCE]->(s) CREATE (d)-[:PINS_SUPPORT]->(c) RETURN d.id`, map[string]any{"id": l.ID, "project": project, "source": l.SourceArtifactID, "generation": l.SourceGeneration, "digest": l.SourceDigest, "support": l.SupportRevision, "excerptDigest": l.ExcerptDigest, "decision": l.DecisionID, "input": l.InputDigest, "data": string(data)})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrDeleted
	}
	return nil
}
func (r *PublicationRepository) FinalizeDerivation(ctx context.Context, tx domain.Tx, l domain.ExcerptLineage) error {
	data, err := json.Marshal(l)
	if err != nil {
		return domain.ErrInvalidInput
	}
	reservation := l
	reservation.ExcerptArtifactID = ""
	reservation.ExcerptGeneration = ""
	reservationData, err := json.Marshal(reservation)
	if err != nil {
		return domain.ErrInvalidInput
	}
	rows, err := r.query(ctx, tx, `MATCH (d:PreparedDerivation {id:$id})-[:PINS_SOURCE]->(s:Artifact),(d)-[:PINS_EXCERPT]->(e:Artifact) WHERE d.state IN ['prepared','finalized'] AND d.data=$reservationData AND d.decisionID=$decision AND d.inputDigest=$input AND d.sourceArtifactID=$source AND d.sourceGeneration=$sourceGeneration AND d.sourceDigest=$sourceDigest AND d.supportRevision=$support AND d.excerptDigest=$excerptDigest AND d.excerptArtifactID=$excerpt AND d.excerptGeneration=$excerptGeneration AND s.state='ready' AND s.available=true AND e.state='ready' AND e.available=true AND s.deletionOperation IS NULL AND e.deletionOperation IS NULL SET d.state='finalized',d.finalLineage=$data RETURN d.id`, map[string]any{"id": l.ID, "decision": l.DecisionID, "input": l.InputDigest, "source": l.SourceArtifactID, "sourceGeneration": l.SourceGeneration, "sourceDigest": l.SourceDigest, "support": l.SupportRevision, "excerptDigest": l.ExcerptDigest, "excerpt": l.ExcerptArtifactID, "excerptGeneration": l.ExcerptGeneration, "data": string(data), "reservationData": string(reservationData)})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrDeleted
	}
	return nil
}

// PinPublicationSupport retains every immutable F5 revision and exact artifact
// generation/digest in the complete support closure, including non-excerpted
// evidence. Explicit deletion invalidates the publication and overrides pins.
func (r *PublicationRepository) PinPublicationSupport(ctx context.Context, tx domain.Tx, id string, revisions []string, artifacts []domain.Artifact) error {
	if len(revisions) < 1 || len(revisions) > domain.MaxDeletionEntries || len(artifacts) < 1 || len(artifacts) > domain.MaxDeletionEntries {
		return domain.ErrBudget
	}
	rows, err := r.query(ctx, tx, `MATCH (p:Publication {id:$id,valid:true}) UNWIND $revisions AS revision MATCH (c:ClaimRevision {id:revision,valid:true}) WHERE c.deletionOperation IS NULL MERGE (p)-[:REQUIRES_REVISION]->(c) RETURN count(DISTINCT c)`, map[string]any{"id": id, "revisions": revisions})
	if err != nil {
		return err
	}
	if len(rows) != 1 || rows[0].Values[0] != int64(len(revisions)) {
		return domain.ErrIncomplete
	}
	inputs := make([]map[string]any, 0, len(artifacts))
	seen := map[string]bool{}
	for _, a := range artifacts {
		if seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		inputs = append(inputs, map[string]any{"id": a.ID, "generation": a.Generation, "digest": a.Digest})
	}
	rows, err = r.query(ctx, tx, `MATCH (p:Publication {id:$id,valid:true}) UNWIND $artifacts AS expected MATCH (a:Artifact {id:expected.id,generation:expected.generation,digest:expected.digest,state:'ready',available:true}) WHERE a.deletionOperation IS NULL MERGE (p)-[:REQUIRES_ARTIFACT {generation:expected.generation,digest:expected.digest}]->(a) RETURN count(DISTINCT a)`, map[string]any{"id": id, "artifacts": inputs})
	if err != nil {
		return err
	}
	if len(rows) != 1 || rows[0].Values[0] != int64(len(inputs)) {
		return domain.ErrIncomplete
	}
	return nil
}

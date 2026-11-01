package repository

import (
	"context"
	"encoding/json"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
)

type VerificationRepository struct{ store *graph.Store }

func NewVerificationRepository(s *graph.Store) *VerificationRepository {
	return &VerificationRepository{s}
}
func (r *VerificationRepository) query(ctx context.Context, tx domain.Tx, q string, p map[string]any) ([]*neo4j.Record, error) {
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
func (r *VerificationRepository) LoadRevision(ctx context.Context, tx domain.Tx, id string) (domain.RevisionState, error) {
	rows, err := r.query(ctx, tx, `MATCH (c:ClaimRevision {id:$id}) WHERE NOT EXISTS {MATCH (:Tombstone {id:c.id})} AND NOT EXISTS {MATCH (:Tombstone {id:c.claimID})} RETURN c.data,c.status,coalesce(c.valid,false) LIMIT 2`, map[string]any{"id": id})
	if err != nil {
		return domain.RevisionState{}, err
	}
	if len(rows) != 1 {
		return domain.RevisionState{}, domain.ErrIncomplete
	}
	var state domain.RevisionState
	if decodeCapture(rows[0], 0, &state.Claim) != nil {
		return state, domain.ErrUnavailable
	}
	status, a := rows[0].Values[1].(string)
	valid, b := rows[0].Values[2].(bool)
	if !a || !b || state.Claim.Revision != id {
		return state, domain.ErrUnavailable
	}
	state.Status = domain.ClaimStatus(status)
	state.Valid = valid
	return state, nil
}
func (r *VerificationRepository) Head(ctx context.Context, tx domain.Tx, id string) (string, error) {
	rows, err := r.query(ctx, tx, `OPTIONAL MATCH (t:Tombstone {id:$id}) WITH count(t) AS deleted OPTIONAL MATCH (:Claim {id:$id})-[:HEAD]->(c:ClaimRevision) RETURN deleted,c.id LIMIT 2`, map[string]any{"id": id})
	if err != nil {
		return "", err
	}
	if len(rows) != 1 {
		return "", domain.ErrUnavailable
	}
	deleted, ok := rows[0].Values[0].(int64)
	if !ok {
		return "", domain.ErrUnavailable
	}
	if deleted > 0 {
		return "", domain.ErrDeleted
	}
	if rows[0].Values[1] == nil {
		return "", nil
	}
	head, ok := rows[0].Values[1].(string)
	if !ok {
		return "", domain.ErrUnavailable
	}
	return head, nil
}
func (r *VerificationRepository) LoadDecisionByKey(ctx context.Context, tx domain.Tx, owner, key string) (domain.OwnerDecision, error) {
	rows, err := r.query(ctx, tx, `MATCH (d:OwnerDecision {ownerID:$owner,key:$key}) RETURN d.data LIMIT 2`, map[string]any{"owner": owner, "key": key})
	if err != nil {
		return domain.OwnerDecision{}, err
	}
	if len(rows) == 0 {
		return domain.OwnerDecision{}, domain.ErrIncomplete
	}
	var d domain.OwnerDecision
	if len(rows) != 1 || decodeCapture(rows[0], 0, &d) != nil {
		return d, domain.ErrUnavailable
	}
	return d, nil
}
func (r *VerificationRepository) PutVerification(ctx context.Context, tx domain.Tx, v domain.Verification, status domain.ClaimStatus, valid bool) error {
	data, err := json.Marshal(v)
	if err != nil {
		return domain.ErrInvalidInput
	}
	rows, err := r.query(ctx, tx, `MATCH (c:ClaimRevision {id:$revision}) CREATE (v:Verification {id:$id,claimRevision:$revision,data:$data,method:$method,outcome:$outcome,policyRevision:$policy,evidenceDigest:$digest}) CREATE (c)-[:VERIFIED_BY]->(v) SET c.status=$status,c.valid=$valid RETURN v.id`, map[string]any{"id": v.ID, "revision": v.ClaimRevision, "data": string(data), "method": v.Method, "outcome": string(v.Outcome), "policy": v.PolicyRevision, "digest": v.EvidenceDigest, "status": string(status), "valid": valid})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrIncomplete
	}
	return nil
}
func (r *VerificationRepository) PutDecision(ctx context.Context, tx domain.Tx, d domain.OwnerDecision) error {
	data, err := json.Marshal(d)
	if err != nil {
		return domain.ErrInvalidInput
	}
	_, err = r.query(ctx, tx, `CREATE (:OwnerDecision {id:$id,ownerID:$owner,key:$key,data:$data,inputDigest:$digest})`, map[string]any{"id": d.ID, "owner": d.OwnerID, "key": d.Key, "data": string(data), "digest": d.InputDigest})
	return err
}
func (r *VerificationRepository) AdvanceRevisionEpoch(ctx context.Context, tx domain.Tx) error {
	rows, err := r.query(ctx, tx, `MATCH (o:OwnerState {id:'owner'}) SET o.revisionEpoch=o.revisionEpoch+1 RETURN o.id`, nil)
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrUnavailable
	}
	return nil
}

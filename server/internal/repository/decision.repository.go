package repository

import (
	"context"
	"encoding/json"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
)

// DecisionRepository persists owner decisions, judgments (never Verification
// nodes), reply receipts, the Buzz cursor and recovery challenges.
type DecisionRepository struct{ store *graph.Store }

func NewDecisionRepository(s *graph.Store) *DecisionRepository { return &DecisionRepository{s} }

func (r *DecisionRepository) query(ctx context.Context, tx domain.Tx, q string, p map[string]any) ([]*neo4j.Record, error) {
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

func encode(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", domain.ErrInvalidInput
	}
	return string(data), nil
}

// one decodes exactly one JSON row; zero rows report ErrIncomplete.
func one(rows []*neo4j.Record, dest any) error {
	if len(rows) == 0 {
		return domain.ErrIncomplete
	}
	if len(rows) != 1 {
		return domain.ErrUnavailable
	}
	return decodeCapture(rows[0], 0, dest)
}

// expectOne requires a write to have matched exactly one node.
func expectOne(rows []*neo4j.Record, err error) error {
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrIncomplete
	}
	return nil
}

func decisionParams(d domain.Decision) (map[string]any, error) {
	data, err := encode(d)
	return map[string]any{"id": d.ID, "state": string(d.State), "thread": d.ThreadID, "owner": d.OwnerID, "data": data}, err
}

func (r *DecisionRepository) PutDecision(ctx context.Context, tx domain.Tx, d domain.Decision) error {
	p, err := decisionParams(d)
	if err != nil {
		return err
	}
	return expectOne(r.query(ctx, tx, `CREATE (d:Decision {id:$id,state:$state,threadID:$thread,ownerID:$owner,data:$data}) RETURN d.id`, p))
}
func (r *DecisionRepository) Decision(ctx context.Context, tx domain.Tx, id string) (domain.Decision, error) {
	rows, err := r.query(ctx, tx, `MATCH (d:Decision {id:$id}) RETURN d.data LIMIT 2`, map[string]any{"id": id})
	if err != nil {
		return domain.Decision{}, err
	}
	var d domain.Decision
	return d, one(rows, &d)
}
func (r *DecisionRepository) UpdateDecision(ctx context.Context, tx domain.Tx, d domain.Decision) error {
	p, err := decisionParams(d)
	if err != nil {
		return err
	}
	return expectOne(r.query(ctx, tx, `MATCH (d:Decision {id:$id}) SET d.state=$state,d.threadID=$thread,d.data=$data RETURN d.id`, p))
}
func (r *DecisionRepository) ListDecisions(ctx context.Context, tx domain.Tx, undeliveredOnly bool, limit int) ([]domain.Decision, error) {
	rows, err := r.query(ctx, tx, `MATCH (d:Decision {state:'pending'}) WHERE NOT $undelivered OR d.threadID = '' RETURN d.data ORDER BY d.id LIMIT $limit`, map[string]any{"undelivered": undeliveredOnly, "limit": limit})
	if err != nil {
		return nil, err
	}
	out := make([]domain.Decision, 0, len(rows))
	for _, row := range rows {
		var d domain.Decision
		if err = decodeCapture(row, 0, &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}
func (r *DecisionRepository) ReplyReceipt(ctx context.Context, tx domain.Tx, eventID string) (domain.DecisionReplyReceipt, error) {
	rows, err := r.query(ctx, tx, `MATCH (r:DecisionReply {eventID:$id}) RETURN r.data LIMIT 2`, map[string]any{"id": eventID})
	if err != nil {
		return domain.DecisionReplyReceipt{}, err
	}
	var v domain.DecisionReplyReceipt
	return v, one(rows, &v)
}
func (r *DecisionRepository) PutReplyReceipt(ctx context.Context, tx domain.Tx, v domain.DecisionReplyReceipt) error {
	data, err := encode(v)
	if err != nil {
		return err
	}
	return expectOne(r.query(ctx, tx, `MATCH (d:Decision {id:$decision}) CREATE (r:DecisionReply {eventID:$id,decisionID:$decision,data:$data})-[:ANSWERS]->(d) RETURN r.eventID`, map[string]any{"id": v.EventID, "decision": v.DecisionID, "data": data}))
}
func (r *DecisionRepository) PutJudgment(ctx context.Context, tx domain.Tx, j domain.OwnerJudgment) error {
	data, err := encode(j)
	if err != nil {
		return err
	}
	return expectOne(r.query(ctx, tx, `MATCH (d:Decision {id:$decision}) CREATE (j:OwnerJudgment {id:$id,decisionID:$decision,candidateRevision:$revision,action:$action,data:$data})-[:JUDGES]->(d) RETURN j.id`, map[string]any{"id": j.ID, "decision": j.DecisionID, "revision": j.CandidateRevision, "action": j.Action, "data": data}))
}

// ListJudgments returns every owner judgment with the decision it answered,
// in no particular order. Judgments are written only by the owner's own
// signatures, so the set stays small enough to read whole; the service
// filters, orders and pages it for review.
func (r *DecisionRepository) ListJudgments(ctx context.Context, tx domain.Tx) ([]domain.JudgmentRecord, error) {
	rows, err := r.query(ctx, tx, `MATCH (j:OwnerJudgment)-[:JUDGES]->(d:Decision) RETURN j.data, d.data`, nil)
	if err != nil {
		return nil, err
	}
	out := make([]domain.JudgmentRecord, 0, len(rows))
	for _, row := range rows {
		var j domain.OwnerJudgment
		var d domain.Decision
		if err = decodeCapture(row, 0, &j); err != nil {
			return nil, err
		}
		if err = decodeCapture(row, 1, &d); err != nil {
			return nil, err
		}
		out = append(out, domain.JudgmentRecord{ID: j.ID, RecordedAt: j.RecordedAt, SignerID: j.SignerID, Action: j.Action, ProjectID: d.ProjectID, DecisionID: j.DecisionID, RequestedAction: d.RequestedAction, CandidateRevision: j.CandidateRevision, EvidenceDigest: j.EvidenceDigest, OwnerID: j.OwnerID, EventID: j.EventID, Channel: j.Channel})
	}
	return out, nil
}
func (r *DecisionRepository) Cursor(ctx context.Context, tx domain.Tx) (string, error) {
	rows, err := r.query(ctx, tx, `OPTIONAL MATCH (c:DecisionCursor {id:'buzz'}) RETURN coalesce(c.value,'')`, nil)
	if err != nil {
		return "", err
	}
	if len(rows) != 1 {
		return "", domain.ErrUnavailable
	}
	v, ok := rows[0].Values[0].(string)
	if !ok {
		return "", domain.ErrUnavailable
	}
	return v, nil
}
func (r *DecisionRepository) PutCursor(ctx context.Context, tx domain.Tx, cursor string) error {
	return expectOne(r.query(ctx, tx, `MERGE (c:DecisionCursor {id:'buzz'}) SET c.value=$value RETURN c.id`, map[string]any{"value": cursor}))
}
func (r *DecisionRepository) PutChallenge(ctx context.Context, tx domain.Tx, c domain.OwnerChallenge) error {
	data, err := encode(c)
	if err != nil {
		return err
	}
	return expectOne(r.query(ctx, tx, `CREATE (c:OwnerChallenge {id:$id,data:$data}) RETURN c.id`, map[string]any{"id": c.ID, "data": data}))
}
func (r *DecisionRepository) Challenge(ctx context.Context, tx domain.Tx, id string) (domain.OwnerChallenge, error) {
	rows, err := r.query(ctx, tx, `MATCH (c:OwnerChallenge {id:$id}) RETURN c.data LIMIT 2`, map[string]any{"id": id})
	if err != nil {
		return domain.OwnerChallenge{}, err
	}
	var c domain.OwnerChallenge
	return c, one(rows, &c)
}
func (r *DecisionRepository) UpdateChallenge(ctx context.Context, tx domain.Tx, c domain.OwnerChallenge) error {
	data, err := encode(c)
	if err != nil {
		return err
	}
	return expectOne(r.query(ctx, tx, `MATCH (c:OwnerChallenge {id:$id}) SET c.data=$data RETURN c.id`, map[string]any{"id": c.ID, "data": data}))
}
func (r *DecisionRepository) PutGrant(ctx context.Context, tx domain.Tx, g domain.ActionGrant) error {
	data, err := encode(g)
	if err != nil {
		return err
	}
	return expectOne(r.query(ctx, tx, `MATCH (c:OwnerChallenge {id:$challenge}) CREATE (g:ActionGrant {id:$id,tokenDigest:$token,data:$data})-[:GRANTED_BY]->(c) RETURN g.id`, map[string]any{"id": g.ID, "challenge": g.ChallengeID, "token": g.TokenDigest, "data": data}))
}
func (r *DecisionRepository) GrantByToken(ctx context.Context, tx domain.Tx, digest string) (domain.ActionGrant, error) {
	rows, err := r.query(ctx, tx, `MATCH (g:ActionGrant {tokenDigest:$token}) RETURN g.data LIMIT 2`, map[string]any{"token": digest})
	if err != nil {
		return domain.ActionGrant{}, err
	}
	var g domain.ActionGrant
	return g, one(rows, &g)
}
func (r *DecisionRepository) UpdateGrant(ctx context.Context, tx domain.Tx, g domain.ActionGrant) error {
	data, err := encode(g)
	if err != nil {
		return err
	}
	return expectOne(r.query(ctx, tx, `MATCH (g:ActionGrant {tokenDigest:$token}) SET g.data=$data RETURN g.id`, map[string]any{"token": g.TokenDigest, "data": data}))
}

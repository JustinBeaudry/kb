package repository

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
)

// FindingsRepository reads dream inputs and persists findings, per-project
// input cursors and the notice outbox. It opens no transactions.
type FindingsRepository struct{ store *graph.Store }

func NewFindingsRepository(store *graph.Store) *FindingsRepository {
	return &FindingsRepository{store: store}
}

func (r *FindingsRepository) query(ctx context.Context, tx domain.Tx, statement string, params map[string]any) ([]*neo4j.Record, error) {
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

// Policy returns an empty policy (no revision) when none exists, which the
// dreaming service treats as "nothing authorized".
func (r *FindingsRepository) Policy(ctx context.Context, tx domain.Tx, project string) (domain.Policy, error) {
	rows, err := r.query(ctx, tx, `MATCH (p:CapturePolicy {projectID:$project}) RETURN p.data`, map[string]any{"project": project})
	if err != nil || len(rows) == 0 {
		return domain.Policy{}, err
	}
	if len(rows) != 1 {
		return domain.Policy{}, domain.ErrUnavailable
	}
	var p domain.Policy
	if decodeCapture(rows[0], 0, &p) != nil {
		return domain.Policy{}, domain.ErrUnavailable
	}
	return p, nil
}

// A revision is current while it is its claim's head, live, and not deleted.
const dreamRevisionFields = `c.id,c.claimID,coalesce(c.text,''),coalesce(c.clientID,''),c.projectID,coalesce(c.taskID,''),coalesce(c.sessionID,''),c.status,
EXISTS {MATCH (:Claim {id:c.claimID})-[:HEAD]->(c)} AND c.status IN ['candidate','verified'] AND NOT EXISTS {MATCH (:Tombstone {id:c.id})} AND NOT EXISTS {MATCH (:Tombstone {id:c.claimID})} AND NOT EXISTS {MATCH (:TaskTombstone {projectID:c.projectID,id:c.taskID})}`

func decodeDreamInput(row *neo4j.Record) (domain.DreamInput, error) {
	v := row.Values
	if len(v) != 9 {
		return domain.DreamInput{}, domain.ErrUnavailable
	}
	var s [8]string
	for i := range s {
		text, ok := v[i].(string)
		if !ok {
			return domain.DreamInput{}, domain.ErrUnavailable
		}
		s[i] = text
	}
	current, ok := v[8].(bool)
	if !ok {
		return domain.DreamInput{}, domain.ErrUnavailable
	}
	return domain.DreamInput{Revision: s[0], ClaimID: s[1], Text: s[2], SubmittedBy: s[3], Scope: domain.Scope{ProjectID: s[4], TaskID: s[5], SessionID: s[6]}, Status: domain.ClaimStatus(s[7]), Current: current}, nil
}

func (r *FindingsRepository) Inputs(ctx context.Context, tx domain.Tx, project string, limit int) ([]domain.DreamInput, error) {
	rows, err := r.query(ctx, tx, `MATCH (:Claim)-[:HEAD]->(c:ClaimRevision {projectID:$project}) WHERE c.status IN ['candidate','verified'] AND NOT EXISTS {MATCH (:Tombstone {id:c.id})} AND NOT EXISTS {MATCH (:Tombstone {id:c.claimID})} AND NOT EXISTS {MATCH (:TaskTombstone {projectID:c.projectID,id:c.taskID})} RETURN `+dreamRevisionFields+` ORDER BY coalesce(c.ingestedAt,0) DESC, c.id LIMIT $limit`, map[string]any{"project": project, "limit": limit})
	if err != nil {
		return nil, err
	}
	return decodeDreamInputs(rows)
}

func (r *FindingsRepository) Revisions(ctx context.Context, tx domain.Tx, ids []string) ([]domain.DreamInput, error) {
	rows, err := r.query(ctx, tx, `MATCH (c:ClaimRevision) WHERE c.id IN $ids RETURN `+dreamRevisionFields, map[string]any{"ids": ids})
	if err != nil {
		return nil, err
	}
	return decodeDreamInputs(rows)
}

func decodeDreamInputs(rows []*neo4j.Record) ([]domain.DreamInput, error) {
	out := make([]domain.DreamInput, 0, len(rows))
	for _, row := range rows {
		in, err := decodeDreamInput(row)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, nil
}

func (r *FindingsRepository) Cursor(ctx context.Context, tx domain.Tx, project string) (string, error) {
	rows, err := r.query(ctx, tx, `MATCH (d:DreamCursor {projectID:$project}) RETURN d.digest`, map[string]any{"project": project})
	if err != nil || len(rows) == 0 {
		return "", err
	}
	digest, ok := rows[0].Values[0].(string)
	if !ok {
		return "", domain.ErrUnavailable
	}
	return digest, nil
}

func (r *FindingsRepository) PutCursor(ctx context.Context, tx domain.Tx, project, digest string) error {
	_, err := r.query(ctx, tx, `MERGE (d:DreamCursor {projectID:$project}) SET d.digest=$digest,d.updatedAt=$now`, map[string]any{"project": project, "digest": digest, "now": time.Now().UTC().UnixNano()})
	return err
}

// ChangedProjects finds projects with claim revisions ingested since the
// given time; the zero time selects every project with claims (hourly scan).
// Results are ordered by project ID and start strictly after the keyset
// cursor `after` ("" for the first page).
func (r *FindingsRepository) ChangedProjects(ctx context.Context, tx domain.Tx, since time.Time, after string, limit int) ([]string, error) {
	rows, err := r.query(ctx, tx, `MATCH (c:ClaimRevision) WHERE coalesce(c.ingestedAt,0)>=$since AND c.projectID IS NOT NULL AND c.projectID>$after AND coalesce(c.clientID,'')<>$dream RETURN DISTINCT c.projectID ORDER BY c.projectID LIMIT $limit`, map[string]any{"since": unixOrZero(since), "after": after, "limit": limit, "dream": domain.DreamClientID})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		id, ok := row.Values[0].(string)
		if !ok {
			return nil, domain.ErrUnavailable
		}
		out = append(out, id)
	}
	return out, nil
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func (r *FindingsRepository) FindingByDigest(ctx context.Context, tx domain.Tx, digest string) (domain.Finding, bool, error) {
	rows, err := r.query(ctx, tx, `MATCH (f:Finding {digest:$digest}) RETURN f.data,f.status`, map[string]any{"digest": digest})
	if err != nil || len(rows) == 0 {
		return domain.Finding{}, false, err
	}
	f, err := decodeFinding(rows[0])
	return f, err == nil, err
}

func decodeFinding(row *neo4j.Record) (domain.Finding, error) {
	var f domain.Finding
	if decodeCapture(row, 0, &f) != nil {
		return f, domain.ErrUnavailable
	}
	status, ok := row.Values[1].(string)
	if !ok {
		return f, domain.ErrUnavailable
	}
	f.Status = domain.FindingStatus(status)
	return f, nil
}

func (r *FindingsRepository) PutFinding(ctx context.Context, tx domain.Tx, f domain.Finding) error {
	data, err := json.Marshal(f)
	if err != nil {
		return domain.ErrInvalidInput
	}
	rows, err := r.query(ctx, tx, `MATCH (c:ClaimRevision {id:$candidate}) CREATE (f:Finding {id:$id,digest:$digest,projectID:$project,status:$status,sensitive:$sensitive,urgency:$urgency,data:$data,createdAt:$created}) CREATE (f)-[:PROPOSES]->(c) WITH f UNWIND $sources AS source MATCH (s:ClaimRevision {id:source}) CREATE (f)-[:DERIVED_FROM]->(s) RETURN count(s)`, map[string]any{"candidate": f.CandidateRevision, "id": f.ID, "digest": f.Digest, "project": f.Scope.ProjectID, "status": string(f.Status), "sensitive": f.Sensitive, "urgency": string(f.Urgency), "data": string(data), "created": f.CreatedAt.UnixNano(), "sources": f.SourceRevisions})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrConflict
	}
	if n, ok := rows[0].Values[0].(int64); !ok || int(n) != len(f.SourceRevisions) {
		return domain.ErrConflict
	}
	return nil
}

// AddToNotice appends to a pending outbox notice, creating it on first use.
// A notice that already left is never reopened.
func (r *FindingsRepository) AddToNotice(ctx context.Context, tx domain.Tx, n domain.Notice) error {
	rows, err := r.query(ctx, tx, `MERGE (n:DreamNotice {id:$id}) ON CREATE SET n.projectID=$project,n.urgency=$urgency,n.state='pending',n.findingIDs=[],n.dueAt=$due WITH n WHERE n.state='pending' SET n.findingIDs=n.findingIDs+[x IN $findings WHERE NOT x IN n.findingIDs] RETURN n.id`, map[string]any{"id": n.ID, "project": n.ProjectID, "urgency": string(n.Urgency), "due": n.DueAt.UnixNano(), "findings": n.FindingIDs})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrConflict
	}
	return nil
}

func (r *FindingsRepository) Notice(ctx context.Context, tx domain.Tx, id string) (domain.Notice, error) {
	rows, err := r.query(ctx, tx, `MATCH (n:DreamNotice {id:$id}) RETURN n.projectID,n.urgency,n.state,n.findingIDs,n.dueAt`, map[string]any{"id": id})
	if err != nil {
		return domain.Notice{}, err
	}
	if len(rows) != 1 {
		return domain.Notice{}, domain.ErrConflict
	}
	v := rows[0].Values
	project, a := v[0].(string)
	urgency, b := v[1].(string)
	state, c := v[2].(string)
	findings, d := stringList(v[3])
	due, e := v[4].(int64)
	if !a || !b || !c || !d || !e {
		return domain.Notice{}, domain.ErrUnavailable
	}
	return domain.Notice{ID: id, ProjectID: project, Urgency: domain.FindingUrgency(urgency), State: domain.NoticeState(state), FindingIDs: findings, DueAt: time.Unix(0, due).UTC()}, nil
}

func (r *FindingsRepository) Findings(ctx context.Context, tx domain.Tx, ids []string) ([]domain.Finding, error) {
	rows, err := r.query(ctx, tx, `MATCH (f:Finding) WHERE f.id IN $ids RETURN f.data,f.status ORDER BY f.createdAt,f.id`, map[string]any{"ids": ids})
	if err != nil {
		return nil, err
	}
	out := make([]domain.Finding, 0, len(rows))
	for _, row := range rows {
		f, err := decodeFinding(row)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

func (r *FindingsRepository) MarkNotice(ctx context.Context, tx domain.Tx, id string, state domain.NoticeState, delivered, suppressed []string) error {
	rows, err := r.query(ctx, tx, `MATCH (n:DreamNotice {id:$id}) WHERE n.state='pending' SET n.state=$state,n.completedAt=$now RETURN n.id`, map[string]any{"id": id, "state": string(state), "now": time.Now().UTC().UnixNano()})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrStale
	}
	if _, err = r.query(ctx, tx, `MATCH (f:Finding) WHERE f.id IN $delivered AND f.status='pending' SET f.status='notified'`, map[string]any{"delivered": delivered}); err != nil {
		return err
	}
	_, err = r.query(ctx, tx, `MATCH (f:Finding) WHERE f.id IN $suppressed AND f.status='pending' SET f.status='suppressed'`, map[string]any{"suppressed": suppressed})
	return err
}

// DreamStatus counts queued dream jobs for the given projects with their
// visible pending reasons.
func (r *FindingsRepository) DreamStatus(ctx context.Context, tx domain.Tx, projects []string) (int, []string, error) {
	rows, err := r.query(ctx, tx, `MATCH (j:Job {status:'queued'}) WHERE j.kind IN $kinds AND j.projectID IN $projects RETURN count(j),collect(DISTINCT j.reasonCode)`, map[string]any{"kinds": []string{domain.DreamJobKind, domain.NotifyJobKind}, "projects": projects})
	if err != nil {
		return 0, nil, err
	}
	if len(rows) != 1 {
		return 0, nil, domain.ErrUnavailable
	}
	n, ok := rows[0].Values[0].(int64)
	reasons, ok2 := stringList(rows[0].Values[1])
	if !ok || !ok2 {
		return 0, nil, domain.ErrUnavailable
	}
	reasons = slices.DeleteFunc(reasons, func(s string) bool { return s == "" })
	slices.Sort(reasons)
	return int(n), reasons, nil
}

package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
)

type JobsRepository struct{ store *graph.Store }

func NewJobsRepository(store *graph.Store) *JobsRepository { return &JobsRepository{store: store} }
func (r *JobsRepository) query(ctx context.Context, tx domain.Tx, statement string, params map[string]any) ([]*neo4j.Record, error) {
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
func (r *JobsRepository) Enqueue(ctx context.Context, tx domain.Tx, job domain.Job) error {
	data, _ := json.Marshal([]string{job.Kind, job.InputDigest, job.PolicyRevision})
	digest := sha256.Sum256(data)
	key := hex.EncodeToString(digest[:])
	rows, err := r.query(ctx, tx, `MATCH (j:Job {id:$id}) WHERE j.key<>$key RETURN j.id`, map[string]any{"id": job.ID, "key": key})
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		return domain.ErrConflict
	}
	revisions := job.InputRevisionIDs
	if revisions == nil {
		revisions = []string{}
	}
	_, err = r.query(ctx, tx, `MERGE (j:Job {key:$key}) ON CREATE SET j.id=$id,j.kind=$kind,j.inputDigest=$digest,j.policyRevision=$policy,j.projectID=$project,j.taskID=$task,j.sessionID=$session,j.inputRevisionIDs=$revisions,j.status='queued',j.attempts=0,j.fence=0,j.dueAt=$due,j.reasonCode=''`, map[string]any{"key": key, "id": job.ID, "kind": job.Kind, "digest": job.InputDigest, "policy": job.PolicyRevision, "project": job.Scope.ProjectID, "task": job.Scope.TaskID, "session": job.Scope.SessionID, "revisions": revisions, "due": job.DueAt.UnixNano()})
	return err
}

// Next only selects persisted eligibility; exhaustion and lease policy belong
// to the service that holds the OwnerState lock for this read/decision/write.
func (r *JobsRepository) Next(ctx context.Context, tx domain.Tx, kinds []string, now time.Time) (domain.Job, domain.Lease, error) {
	rows, err := r.query(ctx, tx, `MATCH (j:Job) WHERE j.kind IN $kinds AND ((j.status='queued' AND j.dueAt<=$now) OR (j.status='running' AND j.expiresAt<=$now)) RETURN properties(j) ORDER BY j.dueAt,j.id LIMIT 1`, map[string]any{"kinds": kinds, "now": now.UnixNano()})
	if err != nil {
		return domain.Job{}, domain.Lease{}, err
	}
	if len(rows) == 0 {
		return domain.Job{}, domain.Lease{}, domain.ErrIncomplete
	}
	return decodeJob(rows[0])
}
func (r *JobsRepository) Claim(ctx context.Context, tx domain.Tx, next domain.Lease, now, maxUntil time.Time) (domain.Job, domain.Lease, error) {
	p, err := leaseParams(next, now)
	if err != nil {
		return domain.Job{}, domain.Lease{}, err
	}
	p["previous"] = int64(next.Fence - 1)
	p["expires"] = next.ExpiresAt.UnixNano()
	p["maximum"] = maxUntil.UnixNano()
	rows, err := r.query(ctx, tx, `MATCH (j:Job {id:$id}) WHERE j.fence=$previous AND ((j.status='queued' AND j.dueAt<=$now) OR (j.status='running' AND j.expiresAt<=$now)) AND $expires>$now AND $maximum>=$expires SET j.status='running',j.workerID=$worker,j.fence=$fence,j.attempts=j.attempts+1,j.expiresAt=$expires,j.maxLeaseUntil=$maximum RETURN properties(j)`, p)
	if err != nil {
		return domain.Job{}, domain.Lease{}, err
	}
	if len(rows) != 1 {
		return domain.Job{}, domain.Lease{}, domain.ErrStale
	}
	return decodeJob(rows[0])
}
func (r *JobsRepository) Exhaust(ctx context.Context, tx domain.Tx, previous domain.Lease, now time.Time, reason string) error {
	if previous.Fence > math.MaxInt64 {
		return domain.ErrInvalidInput
	}
	rows, err := r.query(ctx, tx, `MATCH (j:Job {id:$id}) WHERE j.fence=$fence AND ((j.status='queued' AND j.dueAt<=$now) OR (j.status='running' AND j.expiresAt<=$now)) SET j.status='failed',j.reasonCode=$reason REMOVE j.workerID,j.expiresAt,j.maxLeaseUntil RETURN j.id`, map[string]any{"id": previous.JobID, "fence": int64(previous.Fence), "now": now.UnixNano(), "reason": reason})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrStale
	}
	return nil
}
func decodeJob(row *neo4j.Record) (domain.Job, domain.Lease, error) {
	if len(row.Values) == 0 {
		return domain.Job{}, domain.Lease{}, domain.ErrUnavailable
	}
	p, ok := row.Values[0].(map[string]any)
	if !ok {
		return domain.Job{}, domain.Lease{}, domain.ErrUnavailable
	}
	id, a := p["id"].(string)
	kind, b := p["kind"].(string)
	digest, c := p["inputDigest"].(string)
	policy, d := p["policyRevision"].(string)
	status, e := p["status"].(string)
	attempts, f := p["attempts"].(int64)
	due, g := p["dueAt"].(int64)
	fence, h := p["fence"].(int64)
	revisions, i := stringList(p["inputRevisionIDs"])
	if !a || !b || !c || !d || !e || !f || !g || !h || !i || attempts < 0 || fence < 0 {
		return domain.Job{}, domain.Lease{}, domain.ErrUnavailable
	}
	project, _ := p["projectID"].(string)
	task, _ := p["taskID"].(string)
	session, _ := p["sessionID"].(string)
	reason, _ := p["reasonCode"].(string)
	job := domain.Job{ID: id, Kind: kind, InputDigest: digest, PolicyRevision: policy, ReasonCode: reason, Scope: domain.Scope{ProjectID: project, TaskID: task, SessionID: session}, InputRevisionIDs: revisions, Status: domain.JobStatus(status), Attempts: int(attempts), DueAt: time.Unix(0, due).UTC()}
	lease := domain.Lease{JobID: id, Fence: uint64(fence)}
	if worker, ok := p["workerID"].(string); ok {
		lease.WorkerID = worker
	}
	if expires, ok := p["expiresAt"].(int64); ok {
		lease.ExpiresAt = time.Unix(0, expires).UTC()
	}
	return job, lease, nil
}
func leaseParams(lease domain.Lease, now time.Time) (map[string]any, error) {
	if lease.Fence == 0 || lease.Fence > math.MaxInt64 {
		return nil, domain.ErrInvalidInput
	}
	return map[string]any{"id": lease.JobID, "worker": lease.WorkerID, "fence": int64(lease.Fence), "now": now.UnixNano()}, nil
}

const liveLease = `MATCH (j:Job {id:$id}) WHERE j.status='running' AND j.workerID=$worker AND j.fence=$fence AND j.expiresAt>$now`

func (r *JobsRepository) CheckLease(ctx context.Context, tx domain.Tx, lease domain.Lease, digest string, now time.Time) error {
	p, err := leaseParams(lease, now)
	if err != nil {
		return err
	}
	p["digest"] = digest
	rows, err := r.query(ctx, tx, liveLease+` AND j.inputDigest=$digest RETURN j.id`, p)
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrStale
	}
	return nil
}
func (r *JobsRepository) Finish(ctx context.Context, tx domain.Tx, lease domain.Lease, digest string, now time.Time) error {
	p, err := leaseParams(lease, now)
	if err != nil {
		return err
	}
	p["digest"] = digest
	// Repeating an acknowledged completion is safe; no new business callback runs.
	rows, err := r.query(ctx, tx, `MATCH (j:Job {id:$id}) WHERE j.status='succeeded' AND j.workerID=$worker AND j.fence=$fence AND j.inputDigest=$digest RETURN j.id`, p)
	if err != nil {
		return err
	}
	if len(rows) == 1 {
		return nil
	}
	rows, err = r.query(ctx, tx, liveLease+` AND j.inputDigest=$digest SET j.status='succeeded',j.reasonCode='' MERGE (e:JobEffect {id:j.key}) ON CREATE SET e.jobID=j.id,e.kind=j.kind,e.inputDigest=j.inputDigest,e.policyRevision=j.policyRevision,e.fence=j.fence,e.createdAt=$now RETURN j.id`, p)
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrStale
	}
	return nil
}
func (r *JobsRepository) Leased(ctx context.Context, tx domain.Tx, lease domain.Lease, now time.Time) (domain.Job, time.Time, error) {
	p, err := leaseParams(lease, now)
	if err != nil {
		return domain.Job{}, time.Time{}, err
	}
	rows, err := r.query(ctx, tx, liveLease+` RETURN properties(j),j.maxLeaseUntil`, p)
	if err != nil {
		return domain.Job{}, time.Time{}, err
	}
	if len(rows) != 1 {
		return domain.Job{}, time.Time{}, domain.ErrStale
	}
	job, _, err := decodeJob(rows[0])
	if err != nil {
		return domain.Job{}, time.Time{}, err
	}
	maximum, ok := rows[0].Values[1].(int64)
	if !ok {
		return domain.Job{}, time.Time{}, domain.ErrUnavailable
	}
	return job, time.Unix(0, maximum).UTC(), nil
}
func (r *JobsRepository) Heartbeat(ctx context.Context, tx domain.Tx, lease domain.Lease, expires, now time.Time) (domain.Lease, error) {
	p, err := leaseParams(lease, now)
	if err != nil {
		return domain.Lease{}, err
	}
	p["expires"] = expires.UnixNano()
	rows, err := r.query(ctx, tx, liveLease+` AND j.maxLeaseUntil>$now AND $expires<=j.maxLeaseUntil AND $expires>$now SET j.expiresAt=$expires RETURN properties(j)`, p)
	if err != nil {
		return domain.Lease{}, err
	}
	if len(rows) != 1 {
		return domain.Lease{}, domain.ErrStale
	}
	_, renewed, err := decodeJob(rows[0])
	return renewed, err
}
func (r *JobsRepository) Fail(ctx context.Context, tx domain.Tx, lease domain.Lease, retryAt time.Time, reason string, status domain.JobStatus, now time.Time) error {
	if status != domain.JobQueued && status != domain.JobFailed {
		return domain.ErrInvalidInput
	}
	p, err := leaseParams(lease, now)
	if err != nil {
		return err
	}
	p["due"] = retryAt.UnixNano()
	p["reason"] = reason
	p["status"] = string(status)
	rows, err := r.query(ctx, tx, liveLease+` SET j.status=$status,j.reasonCode=$reason,j.dueAt=$due REMOVE j.workerID,j.expiresAt,j.maxLeaseUntil RETURN j.id`, p)
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrStale
	}
	return nil
}
func (r *JobsRepository) Cancel(ctx context.Context, tx domain.Tx, id string) error {
	rows, err := r.query(ctx, tx, `MATCH (j:Job {id:$id}) WHERE j.status IN ['queued','running','cancelled'] SET j.status='cancelled' REMOVE j.workerID,j.expiresAt,j.maxLeaseUntil RETURN j.id`, map[string]any{"id": id})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrConflict
	}
	return nil
}
func (r *JobsRepository) List(ctx context.Context, tx domain.Tx, status domain.JobStatus, limit int) ([]domain.Job, error) {
	if limit < 1 || limit > 100 {
		return nil, domain.ErrInvalidInput
	}
	rows, err := r.query(ctx, tx, `MATCH (j:Job {status:$status}) RETURN properties(j) ORDER BY j.dueAt,j.id LIMIT $limit`, map[string]any{"status": string(status), "limit": limit})
	if err != nil {
		return nil, err
	}
	jobs := make([]domain.Job, 0, len(rows))
	for _, row := range rows {
		job, _, err := decodeJob(row)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}
func (r *JobsRepository) Effects(ctx context.Context, tx domain.Tx, jobID string) ([]domain.JobEffect, error) {
	rows, err := r.query(ctx, tx, `MATCH (e:JobEffect {jobID:$id}) RETURN e.id,e.jobID,e.kind,e.inputDigest,e.policyRevision,e.fence,e.createdAt`, map[string]any{"id": jobID})
	if err != nil {
		return nil, err
	}
	effects := make([]domain.JobEffect, 0, len(rows))
	for _, row := range rows {
		v := row.Values
		id, a := v[0].(string)
		job, b := v[1].(string)
		kind, c := v[2].(string)
		digest, d := v[3].(string)
		policy, e := v[4].(string)
		fence, f := v[5].(int64)
		created, g := v[6].(int64)
		if !a || !b || !c || !d || !e || !f || !g || fence < 0 {
			return nil, domain.ErrUnavailable
		}
		effects = append(effects, domain.JobEffect{ID: id, JobID: job, Kind: kind, InputDigest: digest, PolicyRevision: policy, Fence: uint64(fence), CreatedAt: time.Unix(0, created).UTC()})
	}
	return effects, nil
}

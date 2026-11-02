package repository

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
)

type LifecycleRepository struct{ store *graph.Store }

func NewLifecycleRepository(s *graph.Store) *LifecycleRepository { return &LifecycleRepository{s} }
func (r *LifecycleRepository) query(ctx context.Context, tx domain.Tx, q string, p map[string]any) ([]*neo4j.Record, error) {
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
func (r *LifecycleRepository) Select(ctx context.Context, tx domain.Tx, v domain.DeleteSelection) (domain.DeletionManifest, error) {
	m := domain.DeletionManifest{ProjectID: v.ProjectID, Sessions: slices.Clone(v.SessionIDs), Tasks: slices.Clone(v.TaskIDs)}
	rows, err := r.query(ctx, tx, `MATCH (n) WHERE (n:Artifact OR n:Event OR n:ClaimRevision OR n:Checkpoint) AND n.projectID=$project AND (n.id IN $ids OR n.claimID IN $ids OR n.sessionID IN $sessions OR n.taskID IN $tasks) RETURN n.id,coalesce(n.claimID,'') ORDER BY n.id LIMIT 1001`, map[string]any{"project": v.ProjectID, "ids": v.IDs, "sessions": v.SessionIDs, "tasks": v.TaskIDs})
	if err != nil {
		return m, err
	}
	if len(rows) > domain.MaxDeletionEntries {
		return m, domain.ErrBudget
	}
	ids := map[string]bool{}
	for _, row := range rows {
		id, ok := row.Values[0].(string)
		if !ok {
			return m, domain.ErrUnavailable
		}
		ids[id] = true
		m.IDs = append(m.IDs, id)
		claim, _ := row.Values[1].(string)
		if claim != "" && !ids[claim] {
			ids[claim] = true
			m.IDs = append(m.IDs, claim)
		}
	}
	if len(m.IDs) == 0 && len(m.Tasks)+len(m.Sessions) == 0 {
		return m, domain.ErrIncomplete
	}
	// Explicit selection alone owns upstream evidence expansion. Computed
	// dependent revisions contribute payload/excerpts, never independent sources.
	explicit := slices.Clone(m.IDs)
	artifactIDs := []string{}
	artifacts := map[string]bool{}
	digests := map[string]bool{}
	add := func(rows []*neo4j.Record) (bool, error) {
		changed := false
		if len(rows) > domain.MaxDeletionEntries {
			return false, domain.ErrBudget
		}
		for _, row := range rows {
			id, a := row.Values[0].(string)
			generation, b := row.Values[1].(string)
			digest, c := row.Values[2].(string)
			if !a || !b || !c {
				return false, domain.ErrUnavailable
			}
			if !artifacts[id] {
				artifacts[id] = true
				artifactIDs = append(artifactIDs, id)
				m.Artifacts = append(m.Artifacts, domain.DeletionArtifact{ID: id, Generation: generation, Digest: digest})
				changed = true
			}
			if !ids[id] {
				ids[id] = true
				m.IDs = append(m.IDs, id)
				changed = true
			}
			if !digests[digest] {
				digests[digest] = true
				m.SourceDigests = append(m.SourceDigests, digest)
				changed = true
			}
		}
		if len(m.IDs)+len(m.Artifacts)+len(m.SourceDigests)+len(m.Tasks)+len(m.Sessions) > domain.MaxDeletionEntries {
			return false, domain.ErrBudget
		}
		return changed, nil
	}
	rows, err = r.query(ctx, tx, `MATCH (a:Artifact {projectID:$project}) WHERE a.id IN $ids OR EXISTS {MATCH (c:ClaimRevision)-[:EVIDENCE]->(a) WHERE c.id IN $ids OR c.claimID IN $ids} OR EXISTS {MATCH (e:Event) WHERE e.id IN $ids AND e.payloadArtifactID=a.id} RETURN a.id,a.generation,a.digest ORDER BY a.id LIMIT 1001`, map[string]any{"project": v.ProjectID, "ids": explicit})
	if err != nil {
		return m, err
	}
	if _, err = add(rows); err != nil {
		return m, err
	}
	for range domain.MaxDeletionEntries + 1 {
		changed := false
		for _, q := range []string{
			`MATCH (a:Artifact {projectID:$project}) WHERE a.id IN $artifacts OR a.digest IN $digests RETURN a.id,a.generation,a.digest ORDER BY a.id LIMIT 1001`,
			`MATCH (source:Artifact {projectID:$project})<-[:PINS_SOURCE]-(lineage)-[:PINS_EXCERPT]->(a:Artifact {projectID:$project}) WHERE source.id IN $artifacts AND (lineage:ExcerptLineage OR lineage:PreparedDerivation) RETURN DISTINCT a.id,a.generation,a.digest ORDER BY a.id LIMIT 1001`,
		} {
			rows, err = r.query(ctx, tx, q, map[string]any{"project": v.ProjectID, "artifacts": artifactIDs, "digests": m.SourceDigests})
			if err != nil {
				return m, err
			}
			added, err := add(rows)
			if err != nil {
				return m, err
			}
			changed = changed || added
		}
		seeds := append(slices.Clone(explicit), artifactIDs...)
		if err = r.markAffected(ctx, tx, v.ProjectID, seeds); err != nil {
			return m, err
		}
		rows, err = r.query(ctx, tx, `MATCH (c:ClaimRevision {projectID:$project}) WHERE c._lifecycleAffected=true RETURN c.id ORDER BY c.id LIMIT 1001`, map[string]any{"project": v.ProjectID})
		if err != nil {
			return m, err
		}
		if len(rows) > domain.MaxDeletionEntries {
			return m, domain.ErrBudget
		}
		for _, row := range rows {
			id, ok := row.Values[0].(string)
			if !ok {
				return m, domain.ErrUnavailable
			}
			if !ids[id] {
				ids[id] = true
				m.IDs = append(m.IDs, id)
				changed = true
			}
		}
		rows, err = r.query(ctx, tx, `MATCH (lineage)-[:PINS_SUPPORT]->(support:ClaimRevision {projectID:$project}) MATCH (lineage)-[:PINS_EXCERPT]->(a:Artifact {projectID:$project}) WHERE support._lifecycleAffected=true AND (lineage:ExcerptLineage OR lineage:PreparedDerivation) RETURN DISTINCT a.id,a.generation,a.digest ORDER BY a.id LIMIT 1001`, map[string]any{"project": v.ProjectID})
		if err != nil {
			return m, err
		}
		added, err := add(rows)
		if err != nil {
			return m, err
		}
		changed = changed || added
		if len(m.IDs)+len(m.Artifacts)+len(m.SourceDigests)+len(m.Tasks)+len(m.Sessions) > domain.MaxDeletionEntries {
			return m, domain.ErrBudget
		}
		if !changed {
			if _, err = r.query(ctx, tx, `MATCH (c:ClaimRevision {projectID:$project}) WHERE c._lifecycleAffected=true REMOVE c._lifecycleAffected`, map[string]any{"project": v.ProjectID}); err != nil {
				return m, err
			}
			return m, nil
		}
	}
	return m, domain.ErrBudget
}

func (r *LifecycleRepository) Operation(ctx context.Context, tx domain.Tx, id string) (domain.DeletionOperation, error) {
	rows, err := r.query(ctx, tx, `MATCH (o:DeletionOperation {id:$id}) RETURN o.data`, map[string]any{"id": id})
	if err != nil {
		return domain.DeletionOperation{}, err
	}
	if len(rows) != 1 {
		return domain.DeletionOperation{}, domain.ErrIncomplete
	}
	var op domain.DeletionOperation
	if decodeCapture(rows[0], 0, &op) != nil {
		return op, domain.ErrUnavailable
	}
	return op, nil
}
func (r *LifecycleRepository) Begin(ctx context.Context, tx domain.Tx, op domain.DeletionOperation) error {
	data, _ := json.Marshal(op)
	m := op.Record.Manifest
	p := map[string]any{"id": op.Receipt.ID, "data": string(data), "digest": op.Record.ManifestDigest, "project": m.ProjectID, "ids": m.IDs, "digests": m.SourceDigests, "sessions": m.Sessions, "tasks": m.Tasks}
	for name, values := range map[string][]string{"sessionEntries": m.Sessions, "taskEntries": m.Tasks, "sourceEntries": m.SourceDigests} {
		entries := make([]map[string]any, 0, len(values))
		for _, value := range values {
			entries = append(entries, map[string]any{"value": value, "identityDigest": domain.LifecycleIdentityDigest(m.ProjectID, value)})
		}
		p[name] = entries
	}
	if _, err := r.query(ctx, tx, `CREATE (:DeletionOperation {id:$id,data:$data,state:'pending_journal',manifestDigest:$digest})`, p); err != nil {
		return err
	}
	if err := r.markAffected(ctx, tx, m.ProjectID, m.IDs); err != nil {
		return err
	}
	for _, q := range []string{
		`UNWIND $ids AS id MERGE (:Tombstone {id:id})`,
		`UNWIND $sourceEntries AS item MERGE (t:SourceTombstone {identityDigest:item.identityDigest}) ON CREATE SET t.projectID=$project,t.digest=item.value`,
		`UNWIND $sessionEntries AS item MERGE (t:SessionTombstone {identityDigest:item.identityDigest}) ON CREATE SET t.projectID=$project,t.id=item.value`,
		`UNWIND $taskEntries AS item MERGE (t:TaskTombstone {identityDigest:item.identityDigest}) ON CREATE SET t.projectID=$project,t.id=item.value`,
		`MATCH (n) WHERE n.projectID=$project AND n.id IN $ids SET n.deletionOperation=$id`,
		`MATCH (l:ExcerptLineage)-[:PINS_SOURCE|PINS_EXCERPT]->(a:Artifact) WHERE a.projectID=$project AND a.id IN $ids SET l.deletionOperation=$id`,
		`MATCH (a:Artifact {projectID:$project}) WHERE a.id IN $ids SET a.state='deleted',a.available=false,a.deletionOperation=$id`,
		`MATCH (c:ClaimRevision {projectID:$project}) WHERE c._lifecycleAffected=true SET c.valid=false,c.status='invalidated',c.deletionOperation=$id`,
		`MATCH (p:Publication) MATCH (c:ClaimRevision {id:p.claimRevision,projectID:$project}) WHERE c._lifecycleAffected=true SET p.valid=false,p.deletionOperation=$id`,
		`MATCH (d:PreparedDerivation) WHERE d.projectID=$project AND (d.sourceArtifactID IN $ids OR d.excerptArtifactID IN $ids OR d.supportRevision IN $ids OR EXISTS {MATCH (c:ClaimRevision {id:d.supportRevision}) WHERE c._lifecycleAffected=true}) SET d.state='deleted'`,
		`MATCH (j:Job) WHERE j.projectID=$project AND (any(id IN coalesce(j.inputRevisionIDs,[]) WHERE id IN $ids OR EXISTS {MATCH (c:ClaimRevision {id:id}) WHERE c._lifecycleAffected=true}) OR j.taskID IN $tasks) AND j.status IN ['queued','running'] SET j.status='cancelled',j.reasonCode='source_deleted'`,
		`MATCH (c:ClaimRevision {projectID:$project}) WHERE c._lifecycleAffected=true REMOVE c._lifecycleAffected`,
		`MATCH (o:OwnerState {id:'owner'}) SET o.deletionEpoch=o.deletionEpoch+1,o.revisionEpoch=o.revisionEpoch+1`,
	} {
		if _, err := r.query(ctx, tx, q, p); err != nil {
			return err
		}
	}
	return nil
}
func (r *LifecycleRepository) Accept(ctx context.Context, tx domain.Tx, id string, b domain.JournalBarrier) (domain.DeletionReceipt, error) {
	op, err := r.Operation(ctx, tx, id)
	if err != nil {
		return domain.DeletionReceipt{}, err
	}
	if op.Receipt.State != "pending_journal" {
		return op.Receipt, nil
	}
	op.Receipt.State = "purge_pending"
	op.Receipt.Barrier = b
	data, _ := json.Marshal(op)
	_, err = r.query(ctx, tx, `MATCH (o:DeletionOperation {id:$id,state:'pending_journal'}) SET o.state='purge_pending',o.data=$data`, map[string]any{"id": id, "data": string(data)})
	return op.Receipt, err
}
func (r *LifecycleRepository) Barrier(ctx context.Context, tx domain.Tx) (domain.JournalBarrier, error) {
	rows, err := r.query(ctx, tx, `MATCH (o:OwnerState {id:'owner'}) RETURN coalesce(o.journalBarrier,'')`, nil)
	if err != nil {
		return domain.JournalBarrier{}, err
	}
	var b domain.JournalBarrier
	if len(rows) != 1 {
		return b, domain.ErrUnavailable
	}
	data, _ := rows[0].Values[0].(string)
	if data != "" && json.Unmarshal([]byte(data), &b) != nil {
		return b, domain.ErrUnavailable
	}
	return b, nil
}
func (r *LifecycleRepository) SetBarrier(ctx context.Context, tx domain.Tx, b domain.JournalBarrier) error {
	data, _ := json.Marshal(b)
	_, err := r.query(ctx, tx, `MATCH (o:OwnerState {id:'owner'}) SET o.journalBarrier=$data`, map[string]any{"data": string(data)})
	return err
}
func (r *LifecycleRepository) PendingOperations(ctx context.Context, tx domain.Tx, limit int) ([]domain.DeletionOperation, error) {
	rows, err := r.query(ctx, tx, `MATCH (o:DeletionOperation {state:'pending_journal'}) RETURN o.data ORDER BY o.id LIMIT $limit`, map[string]any{"limit": limit})
	if err != nil {
		return nil, err
	}
	ops := make([]domain.DeletionOperation, 0, len(rows))
	for _, row := range rows {
		var op domain.DeletionOperation
		if decodeCapture(row, 0, &op) != nil {
			return nil, domain.ErrUnavailable
		}
		ops = append(ops, op)
	}
	return ops, nil
}
func (r *LifecycleRepository) PurgeObjects(ctx context.Context, tx domain.Tx, id string) ([]domain.PurgeObject, error) {
	op, err := r.Operation(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	objects := make([]domain.PurgeObject, 0, len(op.Record.Manifest.Artifacts))
	for _, a := range op.Record.Manifest.Artifacts {
		rows, err := r.query(ctx, tx, `MATCH (a:Artifact {id:$id,state:'deleted'}) WHERE NOT coalesce(a.purged,false) RETURN a.objectKey,a.generation,a.digest,coalesce(a.uploadLeaseUntil,0)`, map[string]any{"id": a.ID, "operation": id})
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			continue
		}
		key, _ := rows[0].Values[0].(string)
		generation, _ := rows[0].Values[1].(string)
		digest, _ := rows[0].Values[2].(string)
		if generation != a.Generation || digest != a.Digest {
			return nil, domain.ErrStale
		}
		until, _ := rows[0].Values[3].(int64)
		objects = append(objects, domain.PurgeObject{ID: a.ID, Key: key, Generation: generation, Digest: digest, UploadLeaseUntil: time.Unix(0, until).UTC()})
	}
	return objects, nil
}
func (r *LifecycleRepository) FinishPurge(ctx context.Context, tx domain.Tx, id string, objects []domain.PurgeObject) error {
	for _, a := range objects {
		rows, err := r.query(ctx, tx, `MATCH (a:Artifact {id:$id,state:'deleted'}) WHERE a.objectKey=$key AND a.generation=$generation AND a.digest=$digest REMOVE a.data,a.objectKey,a.sourcePath SET a.purged=true RETURN a.id`, map[string]any{"id": a.ID, "operation": id, "key": a.Key, "generation": a.Generation, "digest": a.Digest})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return domain.ErrStale
		}
	}
	op, err := r.Operation(ctx, tx, id)
	if err != nil {
		return err
	}
	if err = r.markAffected(ctx, tx, op.Record.Manifest.ProjectID, op.Record.Manifest.IDs); err != nil {
		return err
	}
	params := map[string]any{"id": id, "ids": op.Record.Manifest.IDs, "project": op.Record.Manifest.ProjectID}
	for _, q := range []string{
		`MATCH (n) WHERE (n.deletionOperation=$id OR n.id IN $ids) AND (n:ClaimRevision OR n:Event OR n:Checkpoint OR n:PreparedDerivation OR n:ExcerptLineage) REMOVE n.data,n.text`,
		`MATCH (c:ClaimRevision {projectID:$project}) WHERE c._lifecycleAffected=true REMOVE c.data,c.text,c._lifecycleAffected`,
		`MATCH (d)-[:PINS_SOURCE|PINS_EXCERPT]->(a:Artifact) WHERE a.id IN $ids AND (d:PreparedDerivation OR d:ExcerptLineage) REMOVE d.data`,
	} {
		if _, err = r.query(ctx, tx, q, params); err != nil {
			return err
		}
	}
	op.Receipt.State = "purge_complete"
	data, _ := json.Marshal(op)
	_, err = r.query(ctx, tx, `MATCH (o:DeletionOperation {id:$id}) SET o.state='purge_complete',o.data=$data`, map[string]any{"id": id, "data": string(data)})
	return err
}
func (r *LifecycleRepository) Expiry(ctx context.Context, tx domain.Tx, now time.Time, limit int) ([]domain.DeleteSelection, error) {
	return r.expiry(ctx, tx, now, limit, domain.DeleteSelection{})
}
func (r *LifecycleRepository) expiry(ctx context.Context, tx domain.Tx, now time.Time, limit int, selection domain.DeleteSelection) ([]domain.DeleteSelection, error) {
	// A same-byte copy of a pinned support must not acquire an expiry digest
	// tombstone. Retain that bounded source identity conservatively while pinned.
	rows, err := r.query(ctx, tx, `MATCH (a:Artifact) MATCH (policy:CapturePolicy {projectID:a.projectID}) WHERE a.state IN ['ready','staged'] AND a.ingestedAt IS NOT NULL AND a.ingestedAt <= $now-coalesce(policy.historyRetention,7776000000000000) AND ($project='' OR (a.projectID=$project AND a.id IN $ids)) AND NOT EXISTS {MATCH (pinned:Artifact {projectID:a.projectID,digest:a.digest})<-[:PINS_SOURCE|PINS_EXCERPT]-(pin) WHERE pin:ExcerptLineage OR (pin:PreparedDerivation AND pin.state IN ['prepared','finalized'])} AND NOT EXISTS {MATCH (:Publication {valid:true})-[:REQUIRES_ARTIFACT]->(pinned:Artifact {projectID:a.projectID,digest:a.digest})} RETURN a.projectID,a.id,a.ingestedAt ORDER BY a.ingestedAt,a.id LIMIT $limit`, map[string]any{"now": now.UnixNano(), "limit": limit, "project": selection.ProjectID, "ids": selection.IDs, "tasks": selection.TaskIDs})
	if err != nil {
		return nil, err
	}
	out := []domain.DeleteSelection{}
	for _, row := range rows {
		project, _ := row.Values[0].(string)
		id, _ := row.Values[1].(string)
		at, ok := row.Values[2].(int64)
		if !ok {
			return nil, domain.ErrUnavailable
		}
		p, err := r.Policy(ctx, tx, project)
		if err != nil {
			return nil, err
		}
		duration := p.RawHistoryRetention
		if duration == 0 {
			duration = 90 * 24 * time.Hour
		}
		if at <= now.Add(-duration).UnixNano() {
			out = append(out, domain.DeleteSelection{ProjectID: project, IDs: []string{id}})
		}
	}
	rows, err = r.query(ctx, tx, `MATCH (t:TaskActivity) MATCH (policy:CapturePolicy {projectID:t.projectID}) WHERE t.lastActivityAt IS NOT NULL AND t.lastActivityAt <= $now-coalesce(policy.taskRetention,2592000000000000) AND ($project='' OR (t.projectID=$project AND t.taskID IN $tasks)) AND NOT EXISTS {MATCH (:TaskTombstone {projectID:t.projectID,id:t.taskID})} AND NOT EXISTS {MATCH (:Publication {valid:true})-[:REQUIRES_REVISION]->(c:ClaimRevision {projectID:t.projectID,taskID:t.taskID})} AND NOT EXISTS {MATCH (pin)-[:PINS_SUPPORT]->(c:ClaimRevision {projectID:t.projectID,taskID:t.taskID}) WHERE pin:ExcerptLineage OR (pin:PreparedDerivation AND pin.state IN ['prepared','finalized'])} AND NOT EXISTS {MATCH (pin)-[:PINS_SOURCE|PINS_EXCERPT]->(a:Artifact {projectID:t.projectID,taskID:t.taskID}) WHERE pin:ExcerptLineage OR (pin:PreparedDerivation AND pin.state IN ['prepared','finalized'])} AND NOT EXISTS {MATCH (a:Artifact {projectID:t.projectID,taskID:t.taskID}) MATCH (:Publication {valid:true})-[:REQUIRES_ARTIFACT]->(pinned:Artifact {projectID:t.projectID,digest:a.digest})} AND NOT EXISTS {MATCH (a:Artifact {projectID:t.projectID,taskID:t.taskID}) MATCH (pinned:Artifact {projectID:t.projectID,digest:a.digest})<-[:PINS_SOURCE|PINS_EXCERPT]-(pin) WHERE pin:ExcerptLineage OR (pin:PreparedDerivation AND pin.state IN ['prepared','finalized'])} RETURN t.projectID,t.taskID,t.lastActivityAt ORDER BY t.lastActivityAt,t.taskID LIMIT $limit`, map[string]any{"now": now.UnixNano(), "limit": limit, "project": selection.ProjectID, "ids": selection.IDs, "tasks": selection.TaskIDs})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		project, _ := row.Values[0].(string)
		id, _ := row.Values[1].(string)
		at, ok := row.Values[2].(int64)
		if !ok {
			return nil, domain.ErrUnavailable
		}
		p, err := r.Policy(ctx, tx, project)
		if err != nil {
			return nil, err
		}
		duration := p.InactiveTaskRetention
		if duration == 0 {
			duration = 30 * 24 * time.Hour
		}
		if at <= now.Add(-duration).UnixNano() {
			out = append(out, domain.DeleteSelection{ProjectID: project, TaskIDs: []string{id}})
		}
	}
	return out, nil
}

func (r *LifecycleRepository) Policy(ctx context.Context, tx domain.Tx, project string) (domain.Policy, error) {
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
func (r *LifecycleRepository) UpdatePolicy(ctx context.Context, tx domain.Tx, project, expected string, p domain.Policy) error {
	previous, err := r.Policy(ctx, tx, project)
	if err != nil {
		if err != domain.ErrForbidden {
			return err
		}
		if expected != "" {
			return domain.ErrStale
		}
		data, _ := json.Marshal(p)
		rows, err := r.query(ctx, tx, `CREATE (p:CapturePolicy {projectID:$project,revision:$revision,data:$data,historyRetention:$historyRetention,taskRetention:$taskRetention}) CREATE (:PolicyRevision {projectID:$project,revision:$revision,data:$data,previous:'',excludedFields:$fields,identityDigest:$identity}) WITH p MATCH (o:OwnerState {id:'owner'}) SET o.revisionEpoch=o.revisionEpoch+1 RETURN p.projectID`, map[string]any{"project": project, "revision": p.Revision, "data": string(data), "fields": p.ExcludedFields, "identity": domain.LifecycleIdentityDigest(project, p.Revision), "historyRetention": int64(p.RawHistoryRetention), "taskRetention": int64(p.InactiveTaskRetention)})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return domain.ErrUnavailable
		}
		return nil
	}
	if previous.Revision != expected {
		return domain.ErrStale
	}
	oldData, _ := json.Marshal(previous)
	if _, err = r.query(ctx, tx, `MERGE (p:PolicyRevision {identityDigest:$identity}) ON CREATE SET p.projectID=$project,p.revision=$revision,p.data=$data,p.excludedFields=$fields`, map[string]any{"project": project, "revision": previous.Revision, "data": string(oldData), "fields": previous.ExcludedFields, "identity": domain.LifecycleIdentityDigest(project, previous.Revision)}); err != nil {
		return err
	}
	data, _ := json.Marshal(p)
	rows, err := r.query(ctx, tx, `MATCH (p:CapturePolicy {projectID:$project}) WHERE NOT EXISTS {MATCH (:PolicyRevision {projectID:$project,revision:$revision})} CREATE (:PolicyRevision {projectID:$project,revision:$revision,data:$data,previous:$expected,excludedFields:$fields,identityDigest:$identity}) SET p.data=$data,p.revision=$revision,p.historyRetention=$historyRetention,p.taskRetention=$taskRetention WITH p MATCH (o:OwnerState {id:'owner'}) SET o.revisionEpoch=o.revisionEpoch+1 RETURN p.projectID`, map[string]any{"project": project, "revision": p.Revision, "expected": expected, "data": string(data), "fields": p.ExcludedFields, "identity": domain.LifecycleIdentityDigest(project, p.Revision), "historyRetention": int64(p.RawHistoryRetention), "taskRetention": int64(p.InactiveTaskRetention)})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrConflict
	}
	return nil
}
func (r *LifecycleRepository) ReconcilePolicy(ctx context.Context, tx domain.Tx, project, revision string, limit int) (bool, error) {
	p, err := r.Policy(ctx, tx, project)
	if err != nil {
		return false, err
	}
	if p.Revision != revision {
		return false, domain.ErrStale
	}
	if p.Paused {
		return false, nil
	}
	rows, err := r.query(ctx, tx, `MATCH (a:Artifact {projectID:$project,state:'ready'}) WHERE a.policyRevision<>$revision AND a.bytes<=$bytes AND NOT EXISTS {MATCH (:Tombstone {id:a.id})} AND NOT EXISTS {MATCH (:SourceTombstone {projectID:a.projectID,digest:a.digest})} AND NOT EXISTS {MATCH (:PolicyEligibility {artifactID:a.id,generation:a.generation,digest:a.digest,policyRevision:$revision,eligible:true})} RETURN a.data ORDER BY a.id LIMIT $limit`, map[string]any{"project": project, "revision": revision, "bytes": p.SpoolLimitBytes, "limit": limit + 1})
	if err != nil {
		return false, err
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	for _, row := range rows {
		var a domain.Artifact
		if decodeCapture(row, 0, &a) != nil {
			return false, domain.ErrUnavailable
		}
		safe := true
		for _, excluded := range p.ExcludedPaths {
			prefix := strings.TrimSuffix(excluded, "/")
			if a.SourcePath == "" || a.SourcePath == prefix || strings.HasPrefix(a.SourcePath, prefix+"/") {
				safe = false
			}
		}
		oldRows, err := r.query(ctx, tx, `MATCH (p:PolicyRevision {projectID:$project,revision:$revision}) RETURN p.data`, map[string]any{"project": project, "revision": a.PolicyRevision})
		if err != nil {
			return false, err
		}
		old := domain.Policy{}
		if len(oldRows) == 1 && decodeCapture(oldRows[0], 0, &old) != nil {
			return false, domain.ErrUnavailable
		}
		for _, field := range p.ExcludedFields {
			if !slices.Contains(old.ExcludedFields, field) {
				safe = false
			}
		}
		if !safe {
			continue
		}
		if _, err = r.query(ctx, tx, `MATCH (a:Artifact {id:$id,state:'ready'}) WHERE a.generation=$generation AND a.digest=$digest AND NOT EXISTS {MATCH (:Tombstone {id:a.id})} MERGE (e:PolicyEligibility {identityDigest:$identity}) ON CREATE SET e.artifactID=$id,e.generation=$generation,e.digest=$digest,e.policyRevision=$revision SET e.eligible=true`, map[string]any{"id": a.ID, "generation": a.Generation, "digest": a.Digest, "revision": revision, "identity": domain.LifecycleIdentityDigest(a.ID, a.Generation, a.Digest, revision)}); err != nil {
			return false, err
		}
	}
	_, err = r.query(ctx, tx, `MATCH (o:OwnerState {id:'owner'}) SET o.revisionEpoch=o.revisionEpoch+1`, nil)
	return more, err
}

// PolicyExclusions selects exact sources requiring lifecycle journal removal.
// Fields newly forbidden by current policy cannot be certified absent in old
// opaque bytes. Restrictive changes therefore retire those bytes permanently.
func (r *LifecycleRepository) PolicyExclusions(ctx context.Context, tx domain.Tx, project, revision string, limit int) ([]domain.DeleteSelection, error) {
	p, err := r.Policy(ctx, tx, project)
	if err != nil {
		return nil, err
	}
	if p.Revision != revision {
		return nil, domain.ErrStale
	}
	rows, err := r.query(ctx, tx, `MATCH (a:Artifact {projectID:$project}) OPTIONAL MATCH (old:PolicyRevision {projectID:$project,revision:a.policyRevision}) WITH a,old WHERE a.state IN ['ready','staged'] AND a.policyRevision<>$revision AND (any(path IN $paths WHERE coalesce(a.sourcePath,'')='' OR a.sourcePath=CASE WHEN right(path,1)='/' THEN left(path,size(path)-1) ELSE path END OR a.sourcePath STARTS WITH CASE WHEN right(path,1)='/' THEN path ELSE path+'/' END) OR any(field IN $fields WHERE NOT field IN coalesce(old.excludedFields,[]))) RETURN a.id ORDER BY a.id LIMIT $limit`, map[string]any{"project": project, "revision": revision, "paths": p.ExcludedPaths, "fields": p.ExcludedFields, "limit": limit})
	if err != nil {
		return nil, err
	}
	out := make([]domain.DeleteSelection, 0, len(rows))
	for _, row := range rows {
		id, _ := row.Values[0].(string)
		out = append(out, domain.DeleteSelection{ProjectID: project, IDs: []string{id}})
	}
	return out, nil
}

// ReindexLegacy derives only identity/scope projections from immutable JSON.
// Ingestion age remains the conservative migration baseline, never ObservedAt.
func (r *LifecycleRepository) ReindexLegacy(ctx context.Context, tx domain.Tx, limit int) (bool, error) {
	scopeRows, err := r.query(ctx, tx, `MATCH (n) WHERE (n:TaskActivity OR n:SourceTombstone OR n:SessionTombstone OR n:TaskTombstone OR n:PolicyRevision OR n:PolicyEligibility) AND n.identityDigest IS NULL RETURN elementId(n),labels(n),properties(n) LIMIT $limit`, map[string]any{"limit": limit})
	if err != nil {
		return false, err
	}
	if len(scopeRows) > 0 {
		updates := make([]map[string]any, 0, len(scopeRows))
		for _, row := range scopeRows {
			element, ok := row.Values[0].(string)
			if !ok {
				return false, domain.ErrUnavailable
			}
			labels, ok := stringList(row.Values[1])
			if !ok {
				return false, domain.ErrUnavailable
			}
			properties, ok := row.Values[2].(map[string]any)
			if !ok {
				return false, domain.ErrUnavailable
			}
			fields := []string{"projectID", "id"}
			switch {
			case slices.Contains(labels, "TaskActivity"):
				fields = []string{"projectID", "taskID"}
			case slices.Contains(labels, "SourceTombstone"):
				fields = []string{"projectID", "digest"}
			case slices.Contains(labels, "PolicyRevision"):
				fields = []string{"projectID", "revision"}
			case slices.Contains(labels, "PolicyEligibility"):
				fields = []string{"artifactID", "generation", "digest", "policyRevision"}
			}
			values := make([]string, 0, len(fields))
			for _, field := range fields {
				value, ok := properties[field].(string)
				if !ok {
					return false, domain.ErrUnavailable
				}
				values = append(values, value)
			}
			updates = append(updates, map[string]any{"element": element, "identity": domain.LifecycleIdentityDigest(values...)})
		}
		if _, err = r.query(ctx, tx, `UNWIND $updates AS item MATCH (n) WHERE elementId(n)=item.element SET n.identityDigest=item.identity`, map[string]any{"updates": updates}); err != nil {
			return false, err
		}
		return true, nil
	}
	policyRows, err := r.query(ctx, tx, `MATCH (p:CapturePolicy) WHERE p.historyRetention IS NULL RETURN p.projectID,p.data LIMIT $limit`, map[string]any{"limit": limit})
	if err != nil {
		return false, err
	}
	if len(policyRows) > 0 {
		for _, row := range policyRows {
			project, _ := row.Values[0].(string)
			var p domain.Policy
			if decodeCapture(row, 1, &p) != nil {
				return false, domain.ErrUnavailable
			}
			history := p.RawHistoryRetention
			if history == 0 {
				history = 90 * 24 * time.Hour
			}
			task := p.InactiveTaskRetention
			if task == 0 {
				task = 30 * 24 * time.Hour
			}
			if _, err = r.query(ctx, tx, `MATCH (p:CapturePolicy {projectID:$project}) SET p.historyRetention=$history,p.taskRetention=$task,p.revision=$revision`, map[string]any{"project": project, "history": int64(history), "task": int64(task), "revision": p.Revision}); err != nil {
				return false, err
			}
		}
		return true, nil
	}
	rows, err := r.query(ctx, tx, `MATCH (a:Artifact) WHERE coalesce(a.lifecycleMetadataVersion,0)<>1 AND a.data IS NOT NULL RETURN a.data ORDER BY a.id LIMIT $limit`, map[string]any{"limit": limit})
	if err != nil {
		return false, err
	}
	updates := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		var a domain.Artifact
		if decodeCapture(row, 0, &a) != nil {
			return false, domain.ErrUnavailable
		}
		updates = append(updates, map[string]any{"id": a.ID, "digest": a.Digest, "generation": a.Generation, "session": a.Scope.SessionID, "task": a.Scope.TaskID, "project": a.Scope.ProjectID, "policy": a.PolicyRevision, "sourcePath": a.SourcePath, "bytes": a.Bytes})
	}
	if len(updates) > 0 {
		_, err = r.query(ctx, tx, `UNWIND $updates AS item MATCH (a:Artifact {id:item.id}) SET a.digest=item.digest,a.generation=item.generation,a.sessionID=item.session,a.taskID=item.task,a.projectID=item.project,a.policyRevision=item.policy,a.sourcePath=item.sourcePath,a.bytes=item.bytes,a.lifecycleMetadataVersion=1`, map[string]any{"updates": updates})
		if err != nil {
			return false, err
		}
		return true, nil
	}
	dependencyRows, err := r.query(ctx, tx, `MATCH (c:ClaimRevision) WHERE coalesce(c.dependencyProjectionVersion,0)<>1 AND c.data IS NOT NULL RETURN c.data ORDER BY c.id LIMIT $limit`, map[string]any{"limit": limit})
	if err != nil {
		return false, err
	}
	if len(dependencyRows) > 0 {
		for _, row := range dependencyRows {
			var c domain.ClaimRevision
			if decodeCapture(row, 0, &c) != nil {
				return false, domain.ErrUnavailable
			}
			if len(c.DependsOn) > domain.MaxSupportReferences {
				return false, domain.ErrBudget
			}
			for _, id := range c.DependsOn {
				if _, err = r.query(ctx, tx, `MATCH (c:ClaimRevision {id:$id}),(d:ClaimRevision {id:$dependency}) MERGE (c)-[:DEPENDS_ON]->(d)`, map[string]any{"id": c.Revision, "dependency": id}); err != nil {
					return false, err
				}
			}
			if _, err = r.query(ctx, tx, `MATCH (c:ClaimRevision {id:$id}) SET c.dependencyProjectionVersion=1`, map[string]any{"id": c.Revision}); err != nil {
				return false, err
			}
		}
		return true, nil
	}
	rows, err = r.query(ctx, tx, `MATCH (n) WHERE (n:Checkpoint OR n:Event OR n:ClaimRevision) AND n.lifecycleIndexed IS NULL AND n.data IS NOT NULL RETURN labels(n),n.data ORDER BY n.id LIMIT $limit`, map[string]any{"limit": limit})
	if err != nil {
		return false, err
	}
	updates = make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		labels, ok := stringList(row.Values[0])
		if !ok {
			return false, domain.ErrUnavailable
		}
		var scope domain.Scope
		var id, payload string
		switch {
		case slices.Contains(labels, "Checkpoint"):
			var c domain.Checkpoint
			if decodeCapture(row, 1, &c) != nil {
				return false, domain.ErrUnavailable
			}
			scope = c.Scope
			id = c.ID
		case slices.Contains(labels, "Event"):
			var e domain.Event
			if decodeCapture(row, 1, &e) != nil {
				return false, domain.ErrUnavailable
			}
			scope = e.Scope
			id = e.ID
			payload = e.PayloadArtifactID
		default:
			var c domain.ClaimRevision
			if decodeCapture(row, 1, &c) != nil {
				return false, domain.ErrUnavailable
			}
			scope = c.Scope
			id = c.Revision
		}
		updates = append(updates, map[string]any{"id": id, "project": scope.ProjectID, "task": scope.TaskID, "session": scope.SessionID, "payload": payload})
	}
	if len(updates) > 0 {
		_, err = r.query(ctx, tx, `UNWIND $updates AS item MATCH (n {id:item.id}) WHERE n:Checkpoint OR n:Event OR n:ClaimRevision SET n.lifecycleIndexed=true,n.projectID=item.project,n.taskID=item.task,n.sessionID=item.session,n.payloadArtifactID=item.payload`, map[string]any{"updates": updates})
	}
	return len(updates) > 0, err
}

// Initializing a new namespace is never a restore repair. Only an empty source
// graph with no prior deletion/barrier history qualifies as a fresh installation.
func (r *LifecycleRepository) CanInitializeJournal(ctx context.Context, tx domain.Tx) error {
	rows, err := r.query(ctx, tx, `MATCH (o:OwnerState {id:'owner'}) RETURN coalesce(o.journalBarrier,'')='',NOT EXISTS {MATCH (n) WHERE n:Artifact OR n:Event OR n:Checkpoint OR n:ClaimRevision OR n:Publication OR n:DeletionOperation OR n:Tombstone OR n:SourceTombstone OR n:SessionTombstone OR n:TaskTombstone OR n:PreparedDerivation}`, nil)
	if err != nil {
		return err
	}
	if len(rows) != 1 || rows[0].Values[0] != true || rows[0].Values[1] != true {
		return domain.ErrConflict
	}
	return nil
}

// ExpiryEligible repeats current age/activity, policy and complete pin decisions
// in the same OwnerState lock that will persist Begin. A preview never authorizes
// deletion after an intervening task write, publication pin or policy increase.
func (r *LifecycleRepository) ExpiryEligible(ctx context.Context, tx domain.Tx, selection domain.DeleteSelection, now time.Time) (bool, error) {
	current, err := r.expiry(ctx, tx, now, domain.MaxDeletionEntries, selection)
	if err != nil {
		return false, err
	}
	ids := map[string]bool{}
	tasks := map[string]bool{}
	for _, v := range current {
		for _, id := range v.IDs {
			ids[id] = true
		}
		for _, id := range v.TaskIDs {
			tasks[id] = true
		}
	}
	for _, id := range selection.IDs {
		if !ids[id] {
			return false, nil
		}
	}
	for _, id := range selection.TaskIDs {
		if !tasks[id] {
			return false, nil
		}
	}
	if len(selection.SessionIDs) > 0 {
		return false, nil
	}
	manifest, err := r.Select(ctx, tx, selection)
	if err != nil {
		return false, err
	}
	rows, err := r.query(ctx, tx, `MATCH (a:Artifact) WHERE a.id IN $ids AND (EXISTS {MATCH (:Publication {valid:true})-[:REQUIRES_ARTIFACT]->(a)} OR EXISTS {MATCH (pin)-[:PINS_SOURCE|PINS_EXCERPT]->(a) WHERE pin:ExcerptLineage OR (pin:PreparedDerivation AND pin.state IN ['prepared','finalized'])}) RETURN a.id LIMIT 1`, map[string]any{"ids": manifest.IDs})
	if err != nil {
		return false, err
	}
	if len(rows) > 0 {
		return false, nil
	}
	return true, nil
}

// markAffected projects the complete server-known dependency/publication closure
// in the selected project without collecting an unbounded fanout in Go. Edges
// survive payload scrubbing so replay and overlapping purge remain authoritative.
func (r *LifecycleRepository) markAffected(ctx context.Context, tx domain.Tx, project string, ids []string) error {
	p := map[string]any{"project": project, "ids": ids}
	if _, err := r.query(ctx, tx, `MATCH (c:ClaimRevision {projectID:$project}) WHERE c.id IN $ids OR c.claimID IN $ids OR EXISTS {MATCH (c)-[:EVIDENCE]->(a:Artifact) WHERE a.id IN $ids} SET c._lifecycleAffected=true`, p); err != nil {
		return err
	}
	previous := int64(-1)
	for range domain.MaxSupportReferences + 1 {
		if _, err := r.query(ctx, tx, `MATCH (dependent:ClaimRevision {projectID:$project})-[:DEPENDS_ON]->(source:ClaimRevision {projectID:$project}) WHERE source._lifecycleAffected=true SET dependent._lifecycleAffected=true`, p); err != nil {
			return err
		}
		if _, err := r.query(ctx, tx, `MATCH (publication:Publication) MATCH (target:ClaimRevision {id:publication.claimRevision,projectID:$project}) WHERE target._lifecycleAffected=true OR any(id IN publication.supportRevisions WHERE EXISTS {MATCH (source:ClaimRevision {id:id,projectID:$project}) WHERE source._lifecycleAffected=true}) SET target._lifecycleAffected=true`, p); err != nil {
			return err
		}
		rows, err := r.query(ctx, tx, `MATCH (c:ClaimRevision {projectID:$project}) WHERE c._lifecycleAffected=true RETURN count(c)`, p)
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return domain.ErrUnavailable
		}
		count, ok := rows[0].Values[0].(int64)
		if !ok {
			return domain.ErrUnavailable
		}
		if count == previous {
			return nil
		}
		previous = count
	}
	return domain.ErrBudget
}

package repository

import (
	"context"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
)

type BackupRepository struct{ store *graph.Store }

func NewBackupRepository(s *graph.Store) *BackupRepository { return &BackupRepository{store: s} }
func (r *BackupRepository) query(ctx context.Context, tx domain.Tx, q string, p map[string]any) ([]*neo4j.Record, error) {
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
func (r *BackupRepository) InventoryPage(ctx context.Context, tx domain.Tx, after string, limit int) ([]domain.BackupObject, error) {
	if limit < 1 || limit > 100 {
		return nil, domain.ErrInvalidInput
	}
	rows, err := r.query(ctx, tx, `MATCH (a:Artifact) WHERE a.id>$after RETURN a.id,coalesce(a.objectKey,''),coalesce(a.generation,''),coalesce(a.digest,''),coalesce(a.bytes,0),coalesce(a.state,''),coalesce(a.available,false),a.state IN ['deleted','abandoned','abandoning'] OR EXISTS {MATCH (:Tombstone {id:a.id})} OR EXISTS {MATCH (:SourceTombstone {projectID:a.projectID,digest:a.digest})} OR EXISTS {MATCH (:SessionTombstone {projectID:a.projectID,id:a.sessionID})} OR EXISTS {MATCH (:TaskTombstone {projectID:a.projectID,id:a.taskID})} ORDER BY a.id LIMIT $limit`, map[string]any{"after": after, "limit": limit})
	if err != nil {
		return nil, err
	}
	out := make([]domain.BackupObject, 0, len(rows))
	for _, row := range rows {
		if len(row.Values) != 8 {
			return nil, domain.ErrUnavailable
		}
		id, a := row.Values[0].(string)
		key, b := row.Values[1].(string)
		generation, c := row.Values[2].(string)
		digest, d := row.Values[3].(string)
		size, e := row.Values[4].(int64)
		state, f := row.Values[5].(string)
		available, g := row.Values[6].(bool)
		deleted, h := row.Values[7].(bool)
		if !a || !b || !c || !d || !e || !f || !g || !h {
			return nil, domain.ErrUnavailable
		}
		out = append(out, domain.BackupObject{ArtifactID: id, ObjectKey: key, Generation: generation, Digest: digest, Bytes: size, State: state, Available: available, Deleted: deleted})
	}
	return out, nil
}
func (r *BackupRepository) Barrier(ctx context.Context, tx domain.Tx) (domain.JournalBarrier, error) {
	rows, err := r.query(ctx, tx, `MATCH (o:OwnerState {id:'owner'}) RETURN coalesce(o.journalBarrier,'')`, nil)
	if err != nil {
		return domain.JournalBarrier{}, err
	}
	if len(rows) != 1 {
		return domain.JournalBarrier{}, domain.ErrUnavailable
	}
	var b domain.JournalBarrier
	if err = decodeCapture(rows[0], 0, &b); err != nil {
		return b, err
	}
	return b, nil
}

// The caller's OwnerState lock serializes the marker, revocations and epoch
// changes in one transaction. Retrying a completed restore cannot revoke newly
// enrolled clients, while every new restore ID rotates authentication again.
func (r *BackupRepository) InvalidateRestoredClients(ctx context.Context, tx domain.Tx, id string) error {
	if domain.ValidateIDs(id) != nil {
		return domain.ErrInvalidInput
	}
	rows, err := r.query(ctx, tx, `MATCH (o:OwnerState {id:'owner'}) RETURN coalesce(o.lastCredentialRestoreID,'')`, nil)
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrUnavailable
	}
	previous, ok := rows[0].Values[0].(string)
	if !ok {
		return domain.ErrUnavailable
	}
	if previous == id {
		return nil
	}
	if _, err = r.query(ctx, tx, `MATCH (c:Client) SET c.revokedAt=coalesce(c.revokedAt,$now) REMOVE c.tokenDigest`, map[string]any{"now": time.Now().UTC().UnixNano()}); err != nil {
		return err
	}
	rows, err = r.query(ctx, tx, `MATCH (o:OwnerState {id:'owner'}) SET o.authEpoch=coalesce(o.authEpoch,0)+1,o.revisionEpoch=coalesce(o.revisionEpoch,0)+1,o.deletionEpoch=coalesce(o.deletionEpoch,0)+1,o.lastCredentialRestoreID=$id RETURN o.id`, map[string]any{"id": id})
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return domain.ErrUnavailable
	}
	return nil
}

func (r *BackupRepository) BootstrapPolicyReady(ctx context.Context, tx domain.Tx) error {
	rows, err := r.query(ctx, tx, `MATCH (o:OwnerState {id:'owner'}) RETURN EXISTS {MATCH (:CapturePolicy)}`, nil)
	if err != nil {
		return err
	}
	if len(rows) != 1 || rows[0].Values[0] != true {
		return domain.ErrIncomplete
	}
	return nil
}

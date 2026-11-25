package repository

import (
	"context"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
)

// TransferRepository lists export identities. Visibility is decided by the
// retrieval eligibility boundary; this query only excludes records that are
// already tombstoned so deleted identities never consume export pages.
type TransferRepository struct{ store *graph.Store }

func NewTransferRepository(s *graph.Store) *TransferRepository { return &TransferRepository{s} }

func (r *TransferRepository) ExportPage(ctx context.Context, tx domain.Tx, scope domain.Scope, after string, limit int) ([]domain.ExportHead, error) {
	if limit < 1 || limit > 1000 || domain.ValidateIDs(scope.ProjectID) != nil {
		return nil, domain.ErrInvalidInput
	}
	native, err := r.store.Unwrap(tx)
	if err != nil {
		return nil, err
	}
	res, err := native.Run(ctx, `MATCH (h:Claim)-[:HEAD]->(c:ClaimRevision) WHERE h.id > $after AND c.projectID=$project AND ($task='' OR c.taskID=$task) AND ($session='' OR c.sessionID=$session) AND c.data IS NOT NULL AND NOT EXISTS {MATCH (:Tombstone {id:c.id})} AND NOT EXISTS {MATCH (:Tombstone {id:h.id})} AND NOT EXISTS {MATCH (:SessionTombstone {projectID:c.projectID,id:c.sessionID})} AND NOT EXISTS {MATCH (:TaskTombstone {projectID:c.projectID,id:c.taskID})} RETURN h.id,c.id ORDER BY h.id LIMIT $limit`, map[string]any{"after": after, "project": scope.ProjectID, "task": scope.TaskID, "session": scope.SessionID, "limit": limit})
	if err != nil {
		return nil, domain.ErrUnavailable
	}
	rows, err := res.Collect(ctx)
	if err != nil {
		return nil, domain.ErrUnavailable
	}
	out := make([]domain.ExportHead, 0, len(rows))
	for _, row := range rows {
		claim, ok := row.Values[0].(string)
		revision, ok2 := row.Values[1].(string)
		if !ok || !ok2 {
			return nil, domain.ErrUnavailable
		}
		out = append(out, domain.ExportHead{ClaimID: claim, Revision: revision})
	}
	return out, nil
}

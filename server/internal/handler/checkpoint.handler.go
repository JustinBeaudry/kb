package handler

import (
	"context"
	"github.com/JustinBeaudry/kb/server/internal/domain"
)

func (h *IdentityHandler) PutCheckpoint(ctx context.Context, request PutCheckpointRequestObject) (PutCheckpointResponseObject, error) {
	if request.Body == nil {
		return nil, domain.ErrInvalidInput
	}
	b := request.Body
	c, err := h.checkpoints.Put(ctx, principal(ctx), stringValue(b.ExpectedRevision), domain.Checkpoint{ID: request.Id, Scope: scopeDomain(b.Scope), Objective: b.Objective, Completed: b.Completed, Pending: b.Pending, Questions: b.Questions, EvidenceRevisionIDs: b.EvidenceRevisionIds})
	if err != nil {
		return nil, err
	}
	return PutCheckpoint200JSONResponse{SchemaVersion: "3", Checkpoint: checkpointAPI(c)}, nil
}

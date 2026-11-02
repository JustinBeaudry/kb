package handler

import (
	"bytes"
	"context"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/labstack/echo/v4"
	"io"
	"log/slog"
)

func NewMemoryServer(identity IdentityService, capture CaptureService, checkpoints CheckpointService, logger *slog.Logger) (*echo.Echo, error) {
	return newMemoryServer(identity, capture, checkpoints, logger)
}

type CaptureService interface {
	Stage(context.Context, domain.Principal, service.ArtifactInput, io.Reader) (domain.Artifact, error)
	Complete(context.Context, domain.Principal, string, string) (domain.Receipt, error)
	Append(context.Context, domain.Principal, []domain.Event) ([]domain.Receipt, error)
}
type CheckpointService interface {
	Put(context.Context, domain.Principal, string, domain.Checkpoint) (domain.Checkpoint, error)
}

func scopeDomain(s Scope) domain.Scope {
	return domain.Scope{ProjectID: s.ProjectId, TaskID: stringValue(s.TaskId), SessionID: stringValue(s.SessionId)}
}
func stringValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
func scopeAPI(s domain.Scope) Scope {
	return Scope{ProjectId: s.ProjectID, TaskId: optionalString(s.TaskID), SessionId: optionalString(s.SessionID)}
}
func receiptAPI(r domain.Receipt) Receipt {
	return Receipt{Id: r.ID, Duplicate: r.Duplicate, State: r.State}
}
func (h *IdentityHandler) StageArtifact(ctx context.Context, request StageArtifactRequestObject) (StageArtifactResponseObject, error) {
	if request.Body == nil {
		return nil, domain.ErrInvalidInput
	}
	b := request.Body
	a, err := h.capture.Stage(ctx, principal(ctx), service.ArtifactInput{Scope: scopeDomain(b.Scope), Digest: b.Digest, Bytes: b.Bytes, PolicyRevision: b.PolicyRevision, SourceLocator: b.SourceLocator, SourcePath: stringValue(b.SourcePath)}, bytes.NewReader(b.ContentBase64))
	if err != nil {
		return nil, err
	}
	return StageArtifact201JSONResponse{SchemaVersion: "3", Artifact: Artifact{Id: a.ID, Digest: a.Digest, Scope: scopeAPI(a.Scope), Bytes: a.Bytes, State: ArtifactState(a.State), PolicyRevision: a.PolicyRevision, ExpiresAt: a.ExpiresAt, ObjectAvailable: a.ObjectAvailable, SourceLocator: a.SourceLocator, SourcePath: optionalString(a.SourcePath)}}, nil
}
func (h *IdentityHandler) CompleteArtifact(ctx context.Context, request CompleteArtifactRequestObject) (CompleteArtifactResponseObject, error) {
	if request.Body == nil {
		return nil, domain.ErrInvalidInput
	}
	r, err := h.capture.Complete(ctx, principal(ctx), request.Id, request.Body.Digest)
	if err != nil {
		return nil, err
	}
	return CompleteArtifact200JSONResponse{SchemaVersion: "3", Receipt: receiptAPI(r)}, nil
}
func (h *IdentityHandler) AppendEvents(ctx context.Context, request AppendEventsRequestObject) (AppendEventsResponseObject, error) {
	if request.Body == nil {
		return nil, domain.ErrInvalidInput
	}
	events := make([]domain.Event, len(request.Body.Events))
	for i, e := range request.Body.Events {
		if e.Sequence < 0 {
			return nil, domain.ErrInvalidInput
		}
		events[i] = domain.Event{ID: e.Id, IdempotencyKey: e.IdempotencyKey, Digest: e.Digest, Scope: scopeDomain(e.Scope), Kind: domain.EventKind(e.Kind), ObservedAt: e.ObservedAt, PayloadArtifactID: e.PayloadArtifactId, Sequence: uint64(e.Sequence), Completeness: domain.Completeness(e.Completeness)}
	}
	receipts, err := h.capture.Append(ctx, principal(ctx), events)
	if err != nil {
		return nil, err
	}
	result := make([]Receipt, len(receipts))
	for i, r := range receipts {
		result[i] = receiptAPI(r)
	}
	return AppendEvents202JSONResponse{SchemaVersion: "3", Receipts: result}, nil
}

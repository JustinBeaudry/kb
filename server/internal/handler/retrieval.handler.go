package handler

import (
	"context"
	"encoding/json"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/labstack/echo/v4"
	"log/slog"
)

type RetrievalService interface {
	Retrieve(context.Context, domain.Principal, domain.RetrievalRequest) (domain.RetrievalResult, error)
	Fetch(context.Context, domain.Principal, string, string, int) (domain.RetrievalResult, error)
}

func NewRetrievalServer(i IdentityService, c CaptureService, t CheckpointService, m MemoryService, r RetrievalService, l *slog.Logger) (*echo.Echo, error) {
	return configuredMemoryServer(i, c, t, m, r, l)
}
func retrievalAPI(r domain.RetrievalResult) RetrievalResult {
	out := RetrievalResult{Action: optionalString(r.Action), SchemaVersion: "3", Chunks: make([]EvidenceChunk, 0, len(r.Chunks)), Truncated: r.Truncated, Continuation: optionalString(r.Continuation), Visited: r.Visited, NoResults: r.NoResults}
	if r.TruncationReason != "" {
		v := RetrievalResultTruncationReason(r.TruncationReason)
		out.TruncationReason = &v
	}
	for _, c := range r.Chunks {
		item := EvidenceChunk{RecordId: c.RecordID, Revision: c.Revision, Text: c.Text, ClaimStatus: ClaimStatus(c.ClaimStatus), OriginTrust: OriginTrust(c.OriginTrust), Evidence: refsAPI(c.Evidence), Warnings: stringsAPI(c.Warnings)}
		if c.Applicability != nil {
			a := applicabilityAPI(*c.Applicability)
			item.Applicability = &a
		}
		if c.VerificationOutcome != nil {
			v := VerificationOutcome(*c.VerificationOutcome)
			item.VerificationOutcome = &v
		}
		out.Chunks = append(out.Chunks, item)
	}
	if r.TaskCheckpoint != nil {
		out.TaskCheckpoint = &TaskState{Label: TaskStateLabelTaskState, Checkpoint: checkpointAPI(r.TaskCheckpoint.Checkpoint)}
	}
	if r.TaskStateOmitted {
		v := true
		out.TaskStateOmitted = &v
	}
	return out
}
func wireBudget(result domain.RetrievalResult, budget int) (RetrievalResult, error) {
	out := retrievalAPI(result)
	data, e := json.Marshal(out)
	if e != nil || len(data)+1 > budget {
		return RetrievalResult{}, domain.ErrBudget
	}
	return out, nil
}
func valueOr[T ~int](p *T, fallback int) int {
	if p == nil {
		return fallback
	}
	return int(*p)
}
func (h *IdentityHandler) OrientMemory(ctx context.Context, request OrientMemoryRequestObject) (OrientMemoryResponseObject, error) {
	if request.Body == nil {
		return nil, domain.ErrInvalidInput
	}
	b := request.Body
	r := domain.RetrievalRequest{Scope: scopeDomain(b.Scope), Query: stringValue(b.Query), BudgetBytes: valueOr(b.BudgetBytes, 4096), MaxDepth: valueOr(b.MaxDepth, 2), MaxVisited: valueOr(b.MaxVisited, 50), Continuation: stringValue(b.Continuation), Mode: domain.RetrievalOrientation}
	if b.Mode != nil {
		r.Mode = domain.RetrievalMode(*b.Mode)
	}
	if b.EntityIds != nil {
		r.EntityIDs = *b.EntityIds
	}
	if b.Applicability != nil {
		r.Applicability = applicabilityDomain(*b.Applicability)
	}
	result, e := h.retrieval.Retrieve(ctx, principal(ctx), r)
	if e != nil {
		return nil, e
	}
	out, e := wireBudget(result, r.BudgetBytes)
	return OrientMemory200JSONResponse(out), e
}
func (h *IdentityHandler) SearchMemory(ctx context.Context, request SearchMemoryRequestObject) (SearchMemoryResponseObject, error) {
	if request.Body == nil {
		return nil, domain.ErrInvalidInput
	}
	b := request.Body
	r := domain.RetrievalRequest{Scope: scopeDomain(b.Scope), Query: stringValue(b.Query), BudgetBytes: valueOr(b.BudgetBytes, 16384), MaxDepth: valueOr(b.MaxDepth, 2), MaxVisited: valueOr(b.MaxVisited, 50), Continuation: stringValue(b.Continuation), Mode: domain.RetrievalEvidence}
	if b.Mode != nil {
		r.Mode = domain.RetrievalMode(*b.Mode)
	}
	if b.EntityIds != nil {
		r.EntityIDs = *b.EntityIds
	}
	if b.Applicability != nil {
		r.Applicability = applicabilityDomain(*b.Applicability)
	}
	result, e := h.retrieval.Retrieve(ctx, principal(ctx), r)
	if e != nil {
		return nil, e
	}
	out, e := wireBudget(result, r.BudgetBytes)
	return SearchMemory200JSONResponse(out), e
}
func (h *IdentityHandler) FetchEvidence(ctx context.Context, request FetchEvidenceRequestObject) (FetchEvidenceResponseObject, error) {
	budget := valueOr(request.Params.BudgetBytes, 16384)
	result, e := h.retrieval.Fetch(ctx, principal(ctx), request.Id, stringValue(request.Params.Locator), budget)
	if e != nil {
		return nil, e
	}
	out, e := wireBudget(result, budget)
	return FetchEvidence200JSONResponse(out), e
}
func (h *IdentityHandler) GetCheckpoint(ctx context.Context, request GetCheckpointRequestObject) (GetCheckpointResponseObject, error) {
	reader, ok := h.checkpoints.(interface {
		Get(context.Context, domain.Principal, string) (domain.Checkpoint, error)
	})
	if !ok {
		return nil, domain.ErrUnavailable
	}
	c, e := reader.Get(ctx, principal(ctx), request.Id)
	if e != nil {
		return nil, e
	}
	return GetCheckpoint200JSONResponse{SchemaVersion: "3", Checkpoint: checkpointAPI(c)}, nil
}

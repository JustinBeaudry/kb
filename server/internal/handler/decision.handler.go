package handler

import (
	"context"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// DecisionService resolves owner decisions. Authority comes only from the
// enrolled owner's signature over the exact reply, never the caller's bearer.
type DecisionService interface {
	Respond(context.Context, domain.DecisionReply) (domain.Decision, error)
}

// ServerOption enables optional routes without changing NewServer callers.
type ServerOption func(*IdentityHandler)

// WithDecisions registers POST /v1/decisions/{id}:respond.
func WithDecisions(d DecisionService) ServerOption {
	return func(h *IdentityHandler) { h.decisions = d }
}

func (h *IdentityHandler) RespondDecision(ctx context.Context, request RespondDecisionRequestObject) (RespondDecisionResponseObject, error) {
	if request.Body == nil {
		return nil, domain.ErrInvalidInput
	}
	b := request.Body
	d, err := h.decisions.Respond(ctx, domain.DecisionReply{DecisionID: request.Id, CandidateRevision: b.CandidateRevision, EvidenceDigest: b.EvidenceDigest, Action: b.Action, EventID: b.EventId, SignerID: b.SignerId, Signature: b.Signature})
	if err != nil {
		return nil, err
	}
	return RespondDecision200JSONResponse{SchemaVersion: "3", Receipt: Receipt{Id: d.ID, State: string(d.State)}}, nil
}

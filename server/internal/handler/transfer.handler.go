package handler

import (
	"context"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/service"
)

// TransferService exports authorized, resumable chunks. The handler never
// shapes visibility or status; it maps the service manifest to the wire DTO.
type TransferService interface {
	Export(context.Context, domain.Principal, domain.Scope, string) (service.ExportManifest, error)
}

func claimAPI(c domain.ClaimRevision, trust domain.OriginTrust) ClaimRevision {
	var entityIDs *[]string
	if c.EntityIDs != nil {
		ids := stringsAPI(c.EntityIDs)
		entityIDs = &ids
	}
	return ClaimRevision{EntityIds: entityIDs, ClaimId: c.ClaimID, Revision: c.Revision, Text: c.Text, Kind: c.Kind, Scope: scopeAPI(c.Scope), Evidence: refsAPI(c.Evidence), DependsOn: stringsAPI(c.DependsOn), Applicability: applicabilityAPI(c.Applicability), ClaimStatus: ClaimStatus(c.Status), OriginTrust: OriginTrust(trust)}
}

func (h *IdentityHandler) ExportMemory(ctx context.Context, request ExportMemoryRequestObject) (ExportMemoryResponseObject, error) {
	if request.Body == nil {
		return nil, domain.ErrInvalidInput
	}
	m, err := h.transfer.Export(ctx, principal(ctx), scopeDomain(request.Body.Scope), stringValue(request.Body.Continuation))
	if err != nil {
		return nil, err
	}
	claims := make([]ClaimRevision, 0, len(m.Claims))
	for _, c := range m.Claims {
		claims = append(claims, claimAPI(c.Claim, c.OriginTrust))
	}
	return ExportMemory200JSONResponse{SchemaVersion: "3", Scope: scopeAPI(m.Scope), Claims: claims, Omitted: m.Omitted, Completeness: Completeness(m.Completeness), Continuation: optionalString(m.Continuation)}, nil
}

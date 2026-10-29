package handler

import (
	"context"
	"github.com/JustinBeaudry/kb/server/internal/domain"
)

type MemoryService interface {
	Submit(context.Context, domain.Principal, domain.CandidateInput) (domain.ClaimRevision, error)
}

func applicabilityDomain(a Applicability) domain.Applicability {
	d := domain.Applicability{RepositoryRevision: stringValue(a.RepositoryRevision), DirtyDigest: stringValue(a.DirtyDigest), Environment: stringValue(a.Environment), ValidFrom: a.ValidFrom, ValidUntil: a.ValidUntil}
	if a.Versions != nil {
		d.Versions = *a.Versions
	}
	return d
}
func applicabilityAPI(a domain.Applicability) Applicability {
	d := Applicability{RepositoryRevision: optionalString(a.RepositoryRevision), DirtyDigest: optionalString(a.DirtyDigest), Environment: optionalString(a.Environment), ValidFrom: a.ValidFrom, ValidUntil: a.ValidUntil}
	if a.Versions != nil {
		d.Versions = &a.Versions
	}
	return d
}
func refsAPI(refs []domain.EvidenceRef) []EvidenceRef {
	out := make([]EvidenceRef, 0, len(refs))
	for _, e := range refs {
		out = append(out, EvidenceRef{ArtifactId: e.ArtifactID, Digest: e.Digest, Locator: e.Locator, SourceRevision: e.SourceRevision, Origin: Origin{ClientId: e.Origin.ClientID, Trust: OriginTrust(e.Origin.Trust), AttestationId: optionalString(e.Origin.AttestationID)}, Applicability: applicabilityAPI(e.Applicability)})
	}
	return out
}
func (h *IdentityHandler) SubmitCandidate(ctx context.Context, request SubmitCandidateRequestObject) (SubmitCandidateResponseObject, error) {
	if request.Body == nil {
		return nil, domain.ErrInvalidInput
	}
	b := request.Body
	refs := make([]domain.EvidenceRef, 0, len(b.Evidence))
	for _, e := range b.Evidence {
		refs = append(refs, domain.EvidenceRef{ArtifactID: e.ArtifactId, Digest: e.Digest, Locator: e.Locator, SourceRevision: e.SourceRevision, Applicability: applicabilityDomain(e.Applicability)})
	}
	var entities []string
	if b.EntityIds != nil {
		entities = *b.EntityIds
	}
	c, e := h.memory.Submit(ctx, principal(ctx), domain.CandidateInput{EntityIDs: entities, ClaimID: b.ClaimId, ExpectedRevision: stringValue(b.ExpectedRevision), Text: b.Text, Kind: b.Kind, Scope: scopeDomain(b.Scope), Evidence: refs, DependsOn: b.DependsOn, Applicability: applicabilityDomain(b.Applicability)})
	if e != nil {
		return nil, e
	}
	var entityIDs *[]string
	if c.EntityIDs != nil {
		entityIDs = &c.EntityIDs
	}
	trust := OriginTrustSubmitted
	if len(c.Evidence) > 0 {
		trust = OriginTrust(c.Evidence[0].Origin.Trust)
	}
	return SubmitCandidate201JSONResponse{SchemaVersion: "3", Claim: ClaimRevision{EntityIds: entityIDs, ClaimId: c.ClaimID, Revision: c.Revision, Text: c.Text, Kind: c.Kind, Scope: scopeAPI(c.Scope), Evidence: refsAPI(c.Evidence), DependsOn: stringsAPI(c.DependsOn), Applicability: applicabilityAPI(c.Applicability), ClaimStatus: ClaimStatus(c.Status), OriginTrust: trust}}, nil
}
func stringsAPI(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
func checkpointAPI(c domain.Checkpoint) Checkpoint {
	out := Checkpoint{Id: c.ID, Revision: c.Revision, Scope: scopeAPI(c.Scope), Objective: c.Objective, Completed: stringsAPI(c.Completed), Pending: stringsAPI(c.Pending), Questions: stringsAPI(c.Questions), EvidenceRevisionIds: stringsAPI(c.EvidenceRevisionIDs), UpdatedAt: c.UpdatedAt}
	if c.EvidenceReferencesOmitted {
		v := true
		out.EvidenceReferencesOmitted = &v
	}
	return out
}

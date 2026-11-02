package service

import (
	"context"
	"crypto/rand"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"slices"
)

type MemoryRepository interface {
	PutCandidate(context.Context, Tx, domain.ClaimRevision) error
	Head(context.Context, Tx, string) (domain.ClaimRevision, string, error)
}
type EvidenceReferencer interface {
	Reference(context.Context, domain.Principal, domain.EvidenceRef, domain.Scope) (domain.EvidenceRef, error)
}
type MemoryService struct {
	tx         Transactor
	repo       MemoryRepository
	auth       RetrievalRepository
	evidence   RetrievalEvidence
	references EvidenceReferencer
}

func NewMemoryService(tx Transactor, r MemoryRepository, a RetrievalRepository, e RetrievalEvidence, ref EvidenceReferencer) *MemoryService {
	return &MemoryService{tx, r, a, e, ref}
}
func (s *MemoryService) Submit(ctx context.Context, p domain.Principal, input domain.CandidateInput) (domain.ClaimRevision, error) {
	if domain.ValidateIDs(input.ClaimID, input.Text, input.Kind, input.Scope.ProjectID, input.Scope.TaskID) != nil || len(input.Text) > 16384 || len(input.Evidence) > 50 || len(input.DependsOn) > 50 || len(input.EntityIDs) > 50 {
		return domain.ClaimRevision{}, domain.ErrInvalidInput
	}
	if e := Authorize(p, "candidate:write", []string{input.Scope.ProjectID}); e != nil {
		return domain.ClaimRevision{}, e
	}
	for _, id := range input.EntityIDs {
		if domain.ValidateIDs(id) != nil {
			return domain.ClaimRevision{}, domain.ErrInvalidInput
		}
	}
	// Check task ownership before any source health/integrity observation. The
	// final transaction below rechecks it after external evidence I/O.
	if len(input.Evidence) > 0 {
		err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
			snapshot, e := s.auth.Snapshot(ctx, tx, p, input.Scope)
			if e != nil {
				return e
			}
			if e = Authorize(snapshot.Principal, "candidate:write", []string{input.Scope.ProjectID}); e != nil {
				return e
			}
			if snapshot.TaskOwner != "" && snapshot.TaskOwner != p.ClientID {
				return domain.ErrForbidden
			}
			return nil
		})
		if err != nil {
			return domain.ClaimRevision{}, err
		}
	}
	evidence := slices.Clone(input.Evidence)
	for i, ref := range evidence {
		if domain.ValidateIDs(ref.ArtifactID, ref.Locator, ref.SourceRevision) != nil || !validDigest(ref.Digest) {
			return domain.ClaimRevision{}, domain.ErrInvalidInput
		}
		var e error
		evidence[i], e = s.references.Reference(ctx, p, ref, input.Scope)
		if e != nil {
			return domain.ClaimRevision{}, e
		}
	}
	c := domain.ClaimRevision{EntityIDs: slices.Clone(input.EntityIDs), SubmittedBy: p.ClientID, ClaimID: input.ClaimID, Revision: newRevision(), Text: input.Text, Kind: input.Kind, Scope: input.Scope, Evidence: evidence, DependsOn: slices.Clone(input.DependsOn), Applicability: input.Applicability, Status: domain.ClaimCandidate}
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		snapshot, e := s.auth.Snapshot(ctx, tx, p, input.Scope)
		if e != nil {
			return e
		}
		if e = Authorize(snapshot.Principal, "candidate:write", []string{input.Scope.ProjectID}); e != nil {
			return e
		}
		if snapshot.TaskOwner != "" && snapshot.TaskOwner != p.ClientID {
			return domain.ErrForbidden
		}
		previous, client, e := s.repo.Head(ctx, tx, c.ClaimID)
		if e != nil {
			return e
		}
		if previous.Revision != "" && (client != p.ClientID || previous.Scope != c.Scope) {
			return domain.ErrForbidden
		}
		if previous.Revision != input.ExpectedRevision {
			return domain.ErrConflict
		}
		// Authoritative health and policy are rechecked after external integrity I/O.
		for i, ref := range c.Evidence {
			a, client, e := s.evidence.Artifact(ctx, tx, ref.ArtifactID)
			if e != nil {
				return e
			}
			if a.Scope != c.Scope {
				return domain.ErrForbidden
			}
			if a.State != domain.ArtifactReady || !a.ObjectAvailable {
				return domain.ErrIncomplete
			}
			if a.Digest != ref.Digest || a.SourceLocator != ref.Locator {
				return domain.ErrConflict
			}
			policy, e := s.evidence.Policy(ctx, tx, a.Scope.ProjectID)
			if e != nil {
				return e
			}
			if policy.Paused || policy.Revision == "" || (policy.Revision != a.PolicyRevision && policy.Revision != a.CurrentPolicyRevision) {
				return domain.ErrStale
			}
			if e = validateSource(policy, a.SourcePath); e != nil {
				return e
			}
			c.Evidence[i].Origin = domain.Origin{ClientID: client, Trust: domain.TrustSubmitted}
		}
		if len(c.DependsOn) > 0 {
			eligible, e := s.auth.Eligible(ctx, tx, p, c.Scope, c.DependsOn)
			if e != nil {
				return e
			}
			if len(eligible.Claims) != len(c.DependsOn) {
				return domain.ErrIncomplete
			}
			for _, dep := range eligible.Claims {
				if dep.Status != domain.ClaimVerified {
					return domain.ErrIncomplete
				}
			}
		}
		return s.repo.PutCandidate(ctx, tx, c)
	})
	if err != nil {
		return domain.ClaimRevision{}, err
	}
	return c, nil
}

// newRevision issues every claim and checkpoint revision. rand.Text is 26
// base32 characters, inside the domain.MaxRevisionBytes bound that status
// advertises to remote writers.
func newRevision() string { return rand.Text() }

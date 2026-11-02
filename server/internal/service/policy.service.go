package service

import (
	"context"
	"github.com/JustinBeaudry/kb/server/internal/domain"
)

type PolicyReader interface {
	Policy(context.Context, Tx, string) (domain.Policy, error)
}
type PolicyAuthorization interface {
	Snapshot(context.Context, Tx, domain.Principal, domain.Scope) (domain.RetrievalSnapshot, error)
}
type PolicyService struct {
	tx   Transactor
	repo PolicyReader
	auth PolicyAuthorization
}

func NewPolicyService(tx Transactor, repo PolicyReader, auth PolicyAuthorization) *PolicyService {
	return &PolicyService{tx, repo, auth}
}

// Get refreshes grants inside the same OwnerState-locked transaction as policy.
// A principal authenticated before a grant change cannot disclose stale policy.
func (s *PolicyService) Get(ctx context.Context, p domain.Principal, projectID string) (domain.Policy, error) {
	if domain.ValidateIDs(projectID) != nil {
		return domain.Policy{}, domain.ErrInvalidInput
	}
	if err := Authorize(p, "policy:read", []string{projectID}); err != nil {
		return domain.Policy{}, err
	}
	var policy domain.Policy
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		current, err := s.auth.Snapshot(ctx, tx, p, domain.Scope{ProjectID: projectID})
		if err != nil {
			return err
		}
		if err = Authorize(current.Principal, "policy:read", []string{projectID}); err != nil {
			return err
		}
		policy, err = s.repo.Policy(ctx, tx, projectID)
		if err != nil {
			return err
		}
		if policy.Revision == "" {
			return domain.ErrForbidden
		}
		return nil
	})
	if err != nil {
		return domain.Policy{}, err
	}
	return policy, nil
}

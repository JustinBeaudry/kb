package service

import (
	"context"
	"slices"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// StatusGrants reads the authoritative current grant snapshot for a client.
type StatusGrants interface {
	Snapshot(context.Context, Tx, domain.Principal, domain.Scope) (RetrievalSnapshot, error)
}

// StatusService reports the authenticated client's own connection view from the
// authoritative current grant snapshot, never from request metadata alone. It
// lists no other client and offers no administration.
type StatusService struct {
	tx     Transactor
	grants StatusGrants
	gaps   []string
	dreams DreamingStatusSource
	budget DreamingBudgetStatus
}

// DreamingStatusSource counts queued dream work and its pending reasons for
// the given projects.
type DreamingStatusSource interface {
	DreamStatus(context.Context, Tx, []string) (int, []string, error)
}
type DreamingBudgetStatus interface {
	Status(context.Context) (domain.DreamingStatus, error)
}

// WithDreaming adds owner-only dreaming state: pending reasons, today's cap
// and committed spend, and unreconciled reservations.
func (s *StatusService) WithDreaming(dreams DreamingStatusSource, budget DreamingBudgetStatus) *StatusService {
	s.dreams, s.budget = dreams, budget
	return s
}

// NewStatusService records composition-level capability gaps. Backup/restore
// recovery is always reported unavailable until O2 qualifies it.
func NewStatusService(tx Transactor, grants StatusGrants, gaps ...string) *StatusService {
	return &StatusService{tx: tx, grants: grants, gaps: append([]string{"recovery_unavailable"}, gaps...)}
}
func (s *StatusService) Status(ctx context.Context, p domain.Principal) (domain.Status, error) {
	if err := authorize(ctx, p, "status:read", nil); err != nil {
		return domain.Status{}, err
	}
	var current domain.Principal
	var pending int
	var reasons []string
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		snap, err := s.grants.Snapshot(ctx, tx, p, domain.Scope{})
		if err != nil {
			return err
		}
		if err = authorize(ctx, snap.Principal, "status:read", nil); err != nil {
			return err
		}
		current = snap.Principal
		if s.dreams == nil || current.Role != domain.RoleOwner {
			return nil
		}
		pending, reasons, err = s.dreams.DreamStatus(ctx, tx, current.ProjectIDs)
		return err
	})
	if err != nil {
		return domain.Status{}, err
	}
	status := domain.Status{MaxRevisionBytes: domain.MaxRevisionBytes, ConnectionState: "available", AuthorizedProjectIDs: slices.Clone(current.ProjectIDs), AuthorizedCapabilities: slices.Clone(current.Capabilities), CapabilityGaps: slices.Clone(s.gaps)}
	if s.dreams != nil && s.budget != nil && current.Role == domain.RoleOwner {
		dreaming, err := s.budget.Status(ctx)
		if err != nil {
			return domain.Status{}, err
		}
		dreaming.PendingJobs, dreaming.ReasonCodes = pending, slices.Clone(reasons)
		dreaming.State = dreamingState(dreaming)
		status.Dreaming = &dreaming
	}
	return status, nil
}

func dreamingState(d domain.DreamingStatus) string {
	switch {
	case d.DailyCapMicroUSD <= 0:
		return "unconfigured"
	case slices.Contains(d.ReasonCodes, "model_budget_exhausted"):
		return "budget_exhausted"
	case len(d.ReasonCodes) > 0:
		return "pending"
	}
	return "available"
}

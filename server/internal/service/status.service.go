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
}

// NewStatusService records composition-level capability gaps. Backup/restore
// recovery is always reported unavailable until O2 qualifies it.
func NewStatusService(tx Transactor, grants StatusGrants, gaps ...string) *StatusService {
	return &StatusService{tx: tx, grants: grants, gaps: append([]string{"recovery_unavailable"}, gaps...)}
}
func (s *StatusService) Status(ctx context.Context, p domain.Principal) (domain.Status, error) {
	if err := Authorize(p, "status:read", nil); err != nil {
		return domain.Status{}, err
	}
	var current domain.Principal
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		snap, err := s.grants.Snapshot(ctx, tx, p, domain.Scope{})
		if err != nil {
			return err
		}
		if err = Authorize(snap.Principal, "status:read", nil); err != nil {
			return err
		}
		current = snap.Principal
		return nil
	})
	if err != nil {
		return domain.Status{}, err
	}
	return domain.Status{MaxRevisionBytes: domain.MaxRevisionBytes, ConnectionState: "available", AuthorizedProjectIDs: slices.Clone(current.ProjectIDs), AuthorizedCapabilities: slices.Clone(current.Capabilities), CapabilityGaps: slices.Clone(s.gaps)}, nil
}

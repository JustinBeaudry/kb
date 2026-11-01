package service

import (
	"context"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"time"
)

type CheckpointRepository interface {
	Get(context.Context, Tx, string) (domain.Checkpoint, string, error)
	TaskOwner(context.Context, Tx, domain.Scope) (string, error)
	Save(context.Context, Tx, domain.Checkpoint, string) error
	Latest(context.Context, Tx, domain.Scope) (domain.Checkpoint, string, error)
}
type CheckpointService struct {
	tx   Transactor
	repo CheckpointRepository
	auth CheckpointAuthorization
}

type CheckpointAuthorization interface {
	Snapshot(context.Context, Tx, domain.Principal, domain.Scope) (domain.RetrievalSnapshot, error)
}

func NewCheckpointService(tx Transactor, r CheckpointRepository, auth CheckpointAuthorization) *CheckpointService {
	return &CheckpointService{tx: tx, repo: r, auth: auth}
}
func (s *CheckpointService) Put(ctx context.Context, p domain.Principal, expected string, c domain.Checkpoint) (domain.Checkpoint, error) {
	if domain.ValidateIDs(c.ID, c.Scope.ProjectID, c.Scope.TaskID, c.Objective) != nil || len(c.EvidenceRevisionIDs) > 50 {
		return domain.Checkpoint{}, domain.ErrInvalidInput
	}
	if err := Authorize(p, "checkpoint:write", []string{c.Scope.ProjectID}); err != nil {
		return domain.Checkpoint{}, err
	}
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		if s.auth == nil {
			return domain.ErrUnavailable
		}
		snap, e := s.auth.Snapshot(ctx, tx, p, c.Scope)
		if e != nil {
			return e
		}
		if e = Authorize(snap.Principal, "checkpoint:write", []string{c.Scope.ProjectID}); e != nil {
			return e
		}
		owner, err := s.repo.TaskOwner(ctx, tx, c.Scope)
		if err != nil {
			return err
		}
		if owner != "" && owner != p.ClientID {
			return domain.ErrForbidden
		}
		previous, client, err := s.repo.Get(ctx, tx, c.ID)
		if err != nil {
			return err
		}
		if previous.ID != "" && (client != p.ClientID || previous.Scope != c.Scope) {
			return domain.ErrForbidden
		}
		if previous.Revision != expected {
			return domain.ErrConflict
		}
		c.Revision = newRevision()
		c.UpdatedAt = time.Now().UTC()
		return s.repo.Save(ctx, tx, c, p.ClientID)
	})
	if err != nil {
		return domain.Checkpoint{}, err
	}
	return c, nil
}

func (s *CheckpointService) Get(ctx context.Context, p domain.Principal, id string) (domain.Checkpoint, error) {
	if domain.ValidateIDs(id) != nil {
		return domain.Checkpoint{}, domain.ErrInvalidInput
	}
	var c domain.Checkpoint
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		var client string
		var e error
		c, client, e = s.repo.Get(ctx, tx, id)
		if e != nil {
			return e
		}
		if c.ID == "" || client != p.ClientID {
			return domain.ErrForbidden
		}
		if e = s.authorizeRead(ctx, tx, p, c.Scope); e != nil {
			return e
		}
		if len(c.EvidenceRevisionIDs) > 0 {
			c.EvidenceRevisionIDs = nil
			c.EvidenceReferencesOmitted = true
		}
		return nil
	})
	if err != nil {
		return domain.Checkpoint{}, err
	}
	return c, nil
}
func (s *CheckpointService) Latest(ctx context.Context, p domain.Principal, scope domain.Scope) (domain.Checkpoint, error) {
	if domain.ValidateIDs(scope.ProjectID, scope.TaskID) != nil {
		return domain.Checkpoint{}, domain.ErrInvalidInput
	}
	var c domain.Checkpoint
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		if e := s.authorizeRead(ctx, tx, p, scope); e != nil {
			return e
		}
		var client string
		var e error
		c, client, e = s.repo.Latest(ctx, tx, scope)
		if e != nil {
			return e
		}
		if c.ID == "" || client != p.ClientID {
			return domain.ErrForbidden
		}
		if len(c.EvidenceRevisionIDs) > 0 {
			c.EvidenceRevisionIDs = nil
			c.EvidenceReferencesOmitted = true
		}
		return nil
	})
	if err != nil {
		return domain.Checkpoint{}, err
	}
	return c, nil
}
func (s *CheckpointService) authorizeRead(ctx context.Context, tx Tx, p domain.Principal, scope domain.Scope) error {
	if e := Authorize(p, "retrieval:read", []string{scope.ProjectID}); e != nil {
		return e
	}
	if s.auth == nil {
		return domain.ErrUnavailable
	}
	snap, e := s.auth.Snapshot(ctx, tx, p, scope)
	if e != nil {
		return e
	}
	if e = Authorize(snap.Principal, "retrieval:read", []string{scope.ProjectID}); e != nil {
		return e
	}
	if snap.TaskOwner != p.ClientID {
		return domain.ErrForbidden
	}
	return nil
}

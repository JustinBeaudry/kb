package service

import (
	"context"
	"crypto/rand"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"time"
)

type CheckpointRepository interface {
	Get(context.Context, Tx, string) (domain.Checkpoint, string, error)
	TaskOwner(context.Context, Tx, domain.Scope) (string, error)
	Save(context.Context, Tx, domain.Checkpoint, string) error
}
type CheckpointService struct {
	tx   Transactor
	repo CheckpointRepository
}

func NewCheckpointService(tx Transactor, r CheckpointRepository) *CheckpointService {
	return &CheckpointService{tx, r}
}
func (s *CheckpointService) Put(ctx context.Context, p domain.Principal, expected string, c domain.Checkpoint) (domain.Checkpoint, error) {
	if domain.ValidateIDs(c.ID, c.Scope.ProjectID, c.Scope.TaskID, c.Objective) != nil || len(c.EvidenceRevisionIDs) > 50 {
		return domain.Checkpoint{}, domain.ErrInvalidInput
	}
	if err := Authorize(p, "checkpoint:write", []string{c.Scope.ProjectID}); err != nil {
		return domain.Checkpoint{}, err
	}
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
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
		c.Revision = rand.Text()
		c.UpdatedAt = time.Now().UTC()
		return s.repo.Save(ctx, tx, c, p.ClientID)
	})
	if err != nil {
		return domain.Checkpoint{}, err
	}
	return c, nil
}

package service

import (
	"context"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// ModelBudgetRepository persists reservations. The daily committed sum counts
// reserved and unreconciled holds at their worst case and settled holds at
// measured cost; it never counts unknown spend as zero.
type ModelBudgetRepository interface {
	Committed(context.Context, Tx, string) (int64, error)
	PutReservation(context.Context, Tx, domain.Reservation) error
	Reservation(context.Context, Tx, string) (domain.Reservation, error)
	UpdateReservation(context.Context, Tx, domain.Reservation) error
	Unreconciled(context.Context, Tx) (int, error)
}

// ModelBudgetService enforces the owner-configured daily model spend cap. The
// check-and-reserve runs inside one OwnerState-locked transaction, so
// concurrent workers cannot jointly exceed the cap.
type ModelBudgetService struct {
	tx       Transactor
	repo     ModelBudgetRepository
	dailyCap int64
	now      func() time.Time
}

// NewModelBudgetService takes the owner's daily cap in micro-USD. A cap of
// zero or less means no spend is permitted at all.
func NewModelBudgetService(tx Transactor, repo ModelBudgetRepository, dailyCapMicroUSD int64, now func() time.Time) *ModelBudgetService {
	if now == nil {
		now = time.Now
	}
	return &ModelBudgetService{tx: tx, repo: repo, dailyCap: dailyCapMicroUSD, now: now}
}

func (s *ModelBudgetService) Configured() bool { return s.dailyCap > 0 }

func (s *ModelBudgetService) Reserve(ctx context.Context, scope domain.Scope, route string, maxCostMicros int64) (domain.Reservation, error) {
	if domain.ValidateIDs(scope.ProjectID, route) != nil || maxCostMicros <= 0 {
		return domain.Reservation{}, domain.ErrInvalidInput
	}
	if !s.Configured() {
		return domain.Reservation{}, domain.ErrModelBudgetUnconfigured
	}
	now := s.now().UTC()
	r := domain.Reservation{ID: "reservation-" + newRevision(), Route: route, Day: domain.BudgetDay(now), Scope: scope, MaxCostMicroUSD: maxCostMicros, State: domain.ReservationReserved, Usage: domain.Usage{ReservedMicroUSD: maxCostMicros}, CreatedAt: now}
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		committed, err := s.repo.Committed(ctx, tx, r.Day)
		if err != nil {
			return err
		}
		if committed > s.dailyCap || maxCostMicros > s.dailyCap-committed {
			return domain.ErrBudget
		}
		return s.repo.PutReservation(ctx, tx, r)
	})
	if err != nil {
		return domain.Reservation{}, err
	}
	return r, nil
}

// Settle records what a call cost. Unknown cost leaves the full reservation
// held (state unreconciled) until Reconcile supplies a measured amount.
func (s *ModelBudgetService) Settle(ctx context.Context, reservationID string, usage domain.Usage) error {
	if domain.ValidateIDs(reservationID) != nil || !validUsage(usage) {
		return domain.ErrInvalidInput
	}
	return s.transition(ctx, reservationID, domain.ReservationReserved, usage)
}

// Reconcile resolves an unknown settlement with a measured cost, for example
// from the provider's usage report. It never accepts another unknown.
func (s *ModelBudgetService) Reconcile(ctx context.Context, reservationID string, usage domain.Usage) error {
	if domain.ValidateIDs(reservationID) != nil || !validUsage(usage) || !usage.CostKnown {
		return domain.ErrInvalidInput
	}
	return s.transition(ctx, reservationID, domain.ReservationUnreconciled, usage)
}

func (s *ModelBudgetService) transition(ctx context.Context, id string, from domain.ReservationState, usage domain.Usage) error {
	return s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		r, err := s.repo.Reservation(ctx, tx, id)
		if err != nil {
			return err
		}
		if r.State != from {
			return domain.ErrConflict
		}
		usage.ReservedMicroUSD = r.MaxCostMicroUSD
		r.Usage = usage
		r.State = domain.ReservationUnreconciled
		if usage.CostKnown {
			r.State = domain.ReservationSettled
		}
		return s.repo.UpdateReservation(ctx, tx, r)
	})
}

func validUsage(u domain.Usage) bool {
	if u.Tokens < 0 || u.CostMicroUSD < 0 {
		return false
	}
	// An unknown measurement must not carry a fabricated value.
	return (u.TokensKnown || u.Tokens == 0) && (u.CostKnown || u.CostMicroUSD == 0)
}

// Status reports today's cap, committed spend and unresolved holds.
func (s *ModelBudgetService) Status(ctx context.Context) (domain.DreamingStatus, error) {
	status := domain.DreamingStatus{DailyCapMicroUSD: max(s.dailyCap, 0)}
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		if status.CommittedMicroUSD, err = s.repo.Committed(ctx, tx, domain.BudgetDay(s.now())); err != nil {
			return err
		}
		status.UnreconciledReservations, err = s.repo.Unreconciled(ctx, tx)
		return err
	})
	return status, err
}

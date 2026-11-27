package service

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// AccessRepository persists access audit rollups in the graph.
type AccessRepository interface {
	AddAccess(context.Context, Tx, []domain.AccessRollup) error
	ListAccess(context.Context, Tx, domain.AccessQuery) ([]domain.AccessRollup, error)
	PruneAccess(ctx context.Context, tx Tx, beforeDay string, limit int) (int, error)
}

// AccessOwners resolves the enrolled owner for host-only reads.
type AccessOwners interface {
	Owner(context.Context, Tx) (OwnerIdentity, error)
}

const (
	// MaxPendingAccess bounds the rollups held in memory between flushes. Past
	// it, new keys are dropped and counted; existing keys keep counting.
	MaxPendingAccess = 10000
	// AccessRetentionDays is how long daily rollups are kept.
	AccessRetentionDays = 400
	accessFlushBatch    = 500
	accessPruneBatch    = 1000
	defaultAccessWindow = 30
)

// AccessService is the access audit trail: an in-memory ledger that request
// handling feeds without touching the graph, flushed to durable daily rollups
// in the background, and a host-owner read of those rollups.
type AccessService struct {
	tx     Transactor
	repo   AccessRepository
	owners AccessOwners
	now    func() time.Time
	logger *slog.Logger

	mu      sync.Mutex
	pending map[string]*domain.AccessRollup
	dropped int64
}

func NewAccessService(tx Transactor, repo AccessRepository, owners AccessOwners, now func() time.Time, logger *slog.Logger) *AccessService {
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &AccessService{tx: tx, repo: repo, owners: owners, now: now, logger: logger, pending: map[string]*domain.AccessRollup{}}
}

// Record adds one request's decisions to the ledger. It never blocks on I/O.
func (s *AccessService) Record(observations []domain.AccessObservation) {
	if len(observations) == 0 {
		return
	}
	at := s.now().UTC()
	day := domain.AccessDay(at)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range observations {
		r := domain.AccessRollup{ClientID: o.ClientID, ProjectID: o.ProjectID, Capability: o.Capability, Day: day, FirstAt: at, LastAt: at}
		if o.Allowed {
			r.Allowed = 1
		} else {
			r.Denied = 1
		}
		s.mergeLocked(r)
	}
}

func (s *AccessService) mergeLocked(r domain.AccessRollup) {
	key := r.Key()
	existing, ok := s.pending[key]
	if !ok {
		if len(s.pending) >= MaxPendingAccess {
			s.dropped += r.Allowed + r.Denied
			return
		}
		copied := r
		s.pending[key] = &copied
		return
	}
	existing.Allowed += r.Allowed
	existing.Denied += r.Denied
	if r.FirstAt.Before(existing.FirstAt) {
		existing.FirstAt = r.FirstAt
	}
	if r.LastAt.After(existing.LastAt) {
		existing.LastAt = r.LastAt
	}
}

// Flush writes pending rollups to the graph and prunes expired ones. Rollups
// that fail to write go back to the ledger for the next flush.
func (s *AccessService) Flush(ctx context.Context) error {
	s.mu.Lock()
	batch := s.pending
	s.pending = map[string]*domain.AccessRollup{}
	dropped := s.dropped
	s.dropped = 0
	s.mu.Unlock()
	if dropped > 0 {
		s.logger.WarnContext(ctx, "access_audit_dropped", "dropped_records", dropped)
	}
	keys := slices.Sorted(maps.Keys(batch))
	for start := 0; start < len(keys); start += accessFlushBatch {
		end := min(start+accessFlushBatch, len(keys))
		rows := make([]domain.AccessRollup, 0, end-start)
		for _, k := range keys[start:end] {
			rows = append(rows, *batch[k])
		}
		err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error { return s.repo.AddAccess(ctx, tx, rows) })
		if err != nil {
			s.requeue(keys[start:], batch)
			return err
		}
	}
	cutoff := domain.AccessDay(s.now().UTC().AddDate(0, 0, -AccessRetentionDays))
	return s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		_, err := s.repo.PruneAccess(ctx, tx, cutoff, accessPruneBatch)
		return err
	})
}

func (s *AccessService) requeue(keys []string, batch map[string]*domain.AccessRollup) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		s.mergeLocked(*batch[k])
	}
}

// Run flushes on every interval until ctx ends, then flushes once more so a
// clean shutdown loses nothing. flush wraps Flush with whatever admission the
// process needs; nil means Flush itself. A refused flush keeps its rollups.
func (s *AccessService) Run(ctx context.Context, interval time.Duration, flush func(context.Context) error) {
	if flush == nil {
		flush = s.Flush
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := flush(ctx); err != nil && ctx.Err() == nil && !errors.Is(err, domain.ErrMaintenance) {
				s.logger.WarnContext(ctx, "access_audit_flush_failed", "error_class", "unavailable")
			}
		case <-ctx.Done():
			final, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			if err := flush(final); err != nil {
				s.logger.WarnContext(final, "access_audit_flush_failed", "error_class", "unavailable")
			}
			cancel()
			return
		}
	}
}

// ListHost reads the audit trail for the enrolled owner on the service host.
// Next is an opaque cursor; pass it back as After for the following page.
func (s *AccessService) ListHost(ctx context.Context, q domain.AccessQuery) (domain.AccessPage, error) {
	if q.Limit == 0 {
		q.Limit = 100
	}
	if q.Since == "" {
		q.Since = domain.AccessDay(s.now().UTC().AddDate(0, 0, -defaultAccessWindow))
	}
	if q.Limit < 1 || q.Limit > 500 || !domain.ValidAccessDay(q.Since) || len(q.After) > 1024 {
		return domain.AccessPage{}, domain.ErrInvalidInput
	}
	if q.After != "" {
		after, err := base64.RawURLEncoding.DecodeString(q.After)
		if err != nil || len(after) == 0 {
			return domain.AccessPage{}, domain.ErrInvalidInput
		}
		q.After = string(after)
	}
	for _, id := range []string{q.ClientID, q.ProjectID} {
		if id != "" && !domain.ValidAccessID(id) {
			return domain.AccessPage{}, domain.ErrInvalidInput
		}
	}
	var page domain.AccessPage
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := hostOwner(ctx, tx, s.owners); err != nil {
			return err
		}
		fetch := q
		fetch.Limit = q.Limit + 1
		rows, err := s.repo.ListAccess(ctx, tx, fetch)
		if err != nil {
			return err
		}
		if len(rows) > q.Limit {
			rows = rows[:q.Limit]
			page.Next = base64.RawURLEncoding.EncodeToString([]byte(rows[q.Limit-1].Key()))
		}
		page.Rollups = rows
		return nil
	})
	if err != nil {
		return domain.AccessPage{}, err
	}
	if page.Rollups == nil {
		page.Rollups = []domain.AccessRollup{}
	}
	return page, nil
}

// authorize is Authorize plus an audit observation on the request's collector.
// Use it wherever a decision grants or refuses access to a project; filters
// that only decide what to include keep calling Authorize.
func authorize(ctx context.Context, p domain.Principal, capability string, projects []string) error {
	err := Authorize(p, capability, projects)
	domain.ObserveAccess(ctx, p, capability, projects, err == nil)
	return err
}

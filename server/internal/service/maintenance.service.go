package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

type MaintenanceStateStore interface {
	Load(context.Context) (domain.MaintenanceState, []domain.OperationPermit, error)
	Save(context.Context, domain.MaintenanceState, []domain.OperationPermit) error
}
type OperationAdmission interface {
	Admit(context.Context, string, string) (domain.OperationPermit, error)
	Finish(context.Context, domain.OperationPermit) error
}
type MaintenanceControl interface {
	Begin(context.Context, string) (domain.MaintenanceFence, error)
	WaitDrained(context.Context, domain.MaintenanceFence) error
	State(context.Context) (domain.MaintenanceState, error)
	SetPhase(context.Context, domain.MaintenanceFence, string) error
	Resume(context.Context, domain.MaintenanceFence) error
	AdmitRecovery(context.Context, domain.MaintenanceFence, string, string) (domain.OperationPermit, error)
}

// MaintenanceService is the single supervisor's durable admission authority.
// Cancellation or process exit never imply that a remote effect has settled.
type MaintenanceService struct {
	mu      sync.Mutex
	store   MaintenanceStateStore
	state   domain.MaintenanceState
	permits map[string]domain.OperationPermit
	changed chan struct{}
	failed  bool
}

func NewMaintenanceService(ctx context.Context, store MaintenanceStateStore) (*MaintenanceService, error) {
	if store == nil {
		return nil, domain.ErrInvalidInput
	}
	state, permits, err := store.Load(ctx)
	if err != nil {
		return nil, err
	}
	if !validPhase(state.Phase) || state.Fence.Generation == 0 || state.Fence.OperationID == "" {
		return nil, domain.ErrUnavailable
	}
	s := &MaintenanceService{store: store, state: state, permits: make(map[string]domain.OperationPermit), changed: make(chan struct{})}
	for _, p := range permits {
		if p.ID == "" || p.InstanceID == "" || p.Kind == "" || p.Generation == 0 {
			return nil, domain.ErrUnavailable
		}
		if _, ok := s.permits[p.ID]; ok {
			return nil, domain.ErrUnavailable
		}
		p.Unknown = true
		s.permits[p.ID] = p
	}
	s.state.RecoveryComplete = false
	if s.state.Phase == "open" {
		id, err := randomOperationID()
		if err != nil {
			return nil, err
		}
		s.state.Phase = "recovering"
		s.state.Fence = domain.MaintenanceFence{OperationID: "startup-" + id, Generation: state.Fence.Generation + 1}
		if s.state.Fence.Generation == 0 {
			return nil, domain.ErrUnavailable
		}
	}
	if err = s.save(ctx); err != nil {
		return nil, err
	}
	return s, nil
}
func randomOperationID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", domain.ErrUnavailable
	}
	return hex.EncodeToString(b[:]), nil
}
func validPhase(p string) bool {
	switch p {
	case "bootstrap", "open", "draining", "offline", "recovering", "failed":
		return true
	}
	return false
}
func (s *MaintenanceService) save(ctx context.Context) error {
	permits := make([]domain.OperationPermit, 0, len(s.permits))
	s.state.Active = 0
	s.state.Unknown = 0
	for _, p := range s.permits {
		permits = append(permits, p)
		if p.Unknown {
			s.state.Unknown++
		} else {
			s.state.Active++
		}
	}
	if err := s.store.Save(ctx, s.state, permits); err != nil {
		s.failed = true
		s.state.Phase = "failed"
		s.state.RecoveryComplete = false
		close(s.changed)
		s.changed = make(chan struct{})
		return domain.ErrUnavailable
	}
	close(s.changed)
	s.changed = make(chan struct{})
	return nil
}
func (s *MaintenanceService) State(ctx context.Context) (domain.MaintenanceState, error) {
	if ctx.Err() != nil {
		return domain.MaintenanceState{}, ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, nil
}
func (s *MaintenanceService) Admit(ctx context.Context, instance, kind string) (domain.OperationPermit, error) {
	if strings.HasPrefix(kind, "host-admin:") || strings.Contains(kind, "recovery") {
		return domain.OperationPermit{}, domain.ErrForbidden
	}
	return s.admit(ctx, instance, kind, nil, false)
}

// AdmitHost is deliberately absent from the service-role HTTP interface.
func (s *MaintenanceService) AdmitHost(ctx context.Context, instance, kind string) (domain.OperationPermit, error) {
	if !strings.HasPrefix(kind, "host-admin:") {
		return domain.OperationPermit{}, domain.ErrForbidden
	}
	return s.admit(ctx, instance, kind, nil, true)
}
func (s *MaintenanceService) AdmitRecovery(ctx context.Context, f domain.MaintenanceFence, instance, kind string) (domain.OperationPermit, error) {
	if !recoveryKind(kind) {
		return domain.OperationPermit{}, domain.ErrForbidden
	}
	return s.admit(ctx, instance, kind, &f, true)
}
func recoveryKind(kind string) bool {
	switch kind {
	case "startup-recovery", "backup-recovery", "restore-recovery", "host-admin:setup", "host-admin:journal-init", "host-admin:journal-check", "host-admin:policy", "host-admin:policy-update", "host-admin:enroll", "host-admin:revoke", "host-admin:list", "host-admin:delete", "host-admin:deletion-status":
		return true
	}
	return false
}
func validAdmissionName(s string) bool {
	return s != "" && len(s) <= 128 && !strings.ContainsAny(s, "\x00\r\n")
}
func (s *MaintenanceService) admit(ctx context.Context, instance, kind string, f *domain.MaintenanceFence, _ bool) (domain.OperationPermit, error) {
	if !validAdmissionName(instance) || !validAdmissionName(kind) {
		return domain.OperationPermit{}, domain.ErrInvalidInput
	}
	if ctx.Err() != nil {
		return domain.OperationPermit{}, ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed {
		return domain.OperationPermit{}, domain.ErrMaintenance
	}
	if f != nil {
		if *f != s.state.Fence {
			return domain.OperationPermit{}, domain.ErrStale
		}
		if (s.state.Phase != "bootstrap" && s.state.Phase != "recovering") || len(s.permits) != 0 {
			return domain.OperationPermit{}, domain.ErrMaintenance
		}
	} else if s.state.Phase != "open" {
		return domain.OperationPermit{}, domain.ErrMaintenance
	}
	if len(s.permits) >= 10000 {
		return domain.OperationPermit{}, domain.ErrMaintenance
	}
	id, err := randomOperationID()
	if err != nil {
		return domain.OperationPermit{}, err
	}
	p := domain.OperationPermit{ID: id, InstanceID: instance, Kind: kind, Generation: s.state.Fence.Generation}
	s.permits[id] = p
	s.state.RecoveryComplete = false
	if err = s.save(ctx); err != nil {
		return domain.OperationPermit{}, err
	}
	return p, nil
}
func samePermit(a, b domain.OperationPermit) bool {
	return a.ID == b.ID && a.InstanceID == b.InstanceID && a.Kind == b.Kind && a.Generation == b.Generation
}
func (s *MaintenanceService) Finish(ctx context.Context, p domain.OperationPermit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.permits[p.ID]
	if !ok {
		return nil
	} // A lost Finish response can be retried; missing IDs never grant authority.
	if !samePermit(current, p) {
		return domain.ErrStale
	}
	if current.Unknown {
		return domain.ErrUnknownOutcome
	}
	delete(s.permits, p.ID)
	return s.save(ctx)
}
func (s *MaintenanceService) MarkUnknown(ctx context.Context, p domain.OperationPermit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.permits[p.ID]
	if !ok || !samePermit(current, p) {
		return domain.ErrStale
	}
	current.Unknown = true
	s.permits[p.ID] = current
	return s.save(ctx)
}
func (s *MaintenanceService) ValidatePermit(ctx context.Context, p domain.OperationPermit) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.permits[p.ID]
	if !ok || !samePermit(current, p) || current.Unknown || s.failed {
		return domain.ErrMaintenance
	}
	if s.state.Phase == "open" || s.state.Phase == "draining" {
		return nil
	}
	if (s.state.Phase == "bootstrap" || s.state.Phase == "recovering") && recoveryKind(p.Kind) {
		return nil
	}
	return domain.ErrMaintenance
}
func (s *MaintenanceService) Begin(ctx context.Context, id string) (domain.MaintenanceFence, error) {
	if !validAdmissionName(id) {
		return domain.MaintenanceFence{}, domain.ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed {
		return domain.MaintenanceFence{}, domain.ErrMaintenance
	}
	if s.state.Phase != "open" {
		if s.state.Fence.OperationID == id {
			return s.state.Fence, nil
		}
		return domain.MaintenanceFence{}, domain.ErrConflict
	}
	if s.state.Fence.Generation == ^uint64(0) {
		return domain.MaintenanceFence{}, domain.ErrUnavailable
	}
	s.state.Fence = domain.MaintenanceFence{OperationID: id, Generation: s.state.Fence.Generation + 1}
	s.state.Phase = "draining"
	s.state.RecoveryComplete = false
	if err := s.save(ctx); err != nil {
		return domain.MaintenanceFence{}, err
	}
	return s.state.Fence, nil
}
func (s *MaintenanceService) WaitDrained(ctx context.Context, f domain.MaintenanceFence) error {
	for {
		s.mu.Lock()
		if s.state.Fence != f {
			s.mu.Unlock()
			return domain.ErrStale
		}
		if s.failed {
			s.mu.Unlock()
			return domain.ErrMaintenance
		}
		if len(s.permits) == 0 {
			s.mu.Unlock()
			return nil
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}
func (s *MaintenanceService) SetPhase(ctx context.Context, f domain.MaintenanceFence, phase string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f != s.state.Fence {
		return domain.ErrStale
	}
	if s.failed {
		return domain.ErrMaintenance
	}
	current := s.state.Phase
	if phase == current {
		return nil
	}
	allowed := phase == "failed" || (phase == "offline" && (current == "draining" || current == "recovering")) || (phase == "recovering" && (current == "offline" || current == "draining" || current == "bootstrap" || current == "failed"))
	if !allowed || len(s.permits) != 0 {
		return domain.ErrMaintenance
	}
	s.state.Phase = phase
	s.state.RecoveryComplete = false
	return s.save(ctx)
}
func (s *MaintenanceService) MarkRecoveryComplete(ctx context.Context, f domain.MaintenanceFence) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f != s.state.Fence {
		return domain.ErrStale
	}
	if s.failed || s.state.Phase != "recovering" || len(s.permits) != 0 {
		return domain.ErrMaintenance
	}
	s.state.RecoveryComplete = true
	return s.save(ctx)
}
func (s *MaintenanceService) Resume(ctx context.Context, f domain.MaintenanceFence) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f != s.state.Fence {
		return domain.ErrStale
	}
	if s.failed || s.state.Phase != "recovering" || !s.state.RecoveryComplete || len(s.permits) != 0 {
		return domain.ErrMaintenance
	}
	s.state.Phase = "open"
	s.state.RecoveryComplete = false
	return s.save(ctx)
}
func (s *MaintenanceService) RecordBackup(ctx context.Context, f domain.MaintenanceFence, id string, completedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f != s.state.Fence {
		return domain.ErrStale
	}
	if s.failed || s.state.Phase != "offline" || !validAdmissionName(id) || completedAt.IsZero() {
		return domain.ErrMaintenance
	}
	s.state.BackupID = id
	s.state.LastSuccessfulBackup = completedAt.UTC()
	return s.save(ctx)
}

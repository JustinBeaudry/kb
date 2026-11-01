package service

import (
	"context"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"time"
)

type HealthAdmission interface {
	OperationAdmission
	State(context.Context) (domain.MaintenanceState, error)
}
type HealthProbe interface{ Probe(context.Context) error }
type HealthJournal interface {
	CheckHealth(context.Context) (uint64, uint64, error)
}
type HealthService struct {
	admission     HealthAdmission
	graph, object HealthProbe
	journal       HealthJournal
	timeout       time.Duration
	instance      string
}

func NewHealthService(admission HealthAdmission, graph, object HealthProbe, journal HealthJournal, timeout time.Duration, instance string) *HealthService {
	if timeout <= 0 || timeout > 10*time.Second {
		timeout = 2 * time.Second
	}
	return &HealthService{admission: admission, graph: graph, object: object, journal: journal, timeout: timeout, instance: instance}
}
func (s *HealthService) Liveness() domain.HealthReport { return domain.HealthReport{Live: true} }
func (s *HealthService) Readiness(ctx context.Context) domain.HealthReport {
	r := domain.HealthReport{Live: true}
	ctx, cancel := context.WithTimeout(domain.WithOperationOutcome(ctx), s.timeout)
	defer cancel()
	if s.admission == nil {
		r.Reasons = []string{"maintenance"}
		return r
	}
	st, err := s.admission.State(ctx)
	r.Maintenance = st
	if err != nil || st.Phase != "open" {
		r.Reasons = []string{"maintenance"}
		return r
	}
	p, err := s.admission.Admit(ctx, s.instance, "health")
	if err != nil {
		r.Reasons = []string{"maintenance"}
		return r
	}
	// Probes are synchronous: no unjoined goroutine survives the permit.
	if s.graph == nil || s.graph.Probe(ctx) != nil {
		r.Reasons = append(r.Reasons, "graph_unavailable")
	}
	if s.object == nil || s.object.Probe(ctx) != nil {
		r.Reasons = append(r.Reasons, "objects_unavailable")
	}
	if s.journal == nil {
		r.Reasons = append(r.Reasons, "journal_unavailable")
	} else {
		seq, capacity, err := s.journal.CheckHealth(ctx)
		r.JournalSequence = seq
		r.JournalCapacity = capacity
		r.JournalCapacityWarning = capacity > 0 && seq >= capacity*9/10
		if err != nil {
			r.Reasons = append(r.Reasons, "journal_unavailable")
		} else if capacity == 0 || seq >= capacity {
			r.Reasons = append(r.Reasons, "journal_capacity")
		}
	}
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), s.timeout)
	defer finishCancel()
	if !domain.OperationSettled(ctx) || s.admission.Finish(finishCtx, p) != nil {
		r.Reasons = append(r.Reasons, "maintenance")
	}
	r.Ready = len(r.Reasons) == 0
	return r
}

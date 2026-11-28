package domain

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

var ErrMaintenance = errors.New("maintenance admission closed")

type MaintenanceFence struct {
	OperationID string `json:"operation_id"`
	Generation  uint64 `json:"generation"`
}
type OperationPermit struct {
	ID         string `json:"id"`
	InstanceID string `json:"instance_id"`
	Kind       string `json:"kind"`
	Generation uint64 `json:"generation"`
	Unknown    bool   `json:"unknown,omitempty"`
	// Restored marks a permit that was still registered when an earlier
	// supervisor process stopped and that its holder never declared uncertain.
	// Only its holder's exact Finish, or a host operator's settle after proving
	// the holder ended, removes it. It stays unknown for admission until then.
	Restored bool `json:"restored,omitempty"`
	// HoldUntil is when a holder that declared a hold has stopped its work.
	// The holder cancels the operation at its own deadline, measured from
	// before it asked for the permit, so the supervisor may settle the permit
	// once this time and a grace period for in-flight calls have passed.
	// It is zero for permits whose holder declared no hold.
	HoldUntil time.Time `json:"hold_until,omitzero"`
}
type MaintenanceState struct {
	Fence                MaintenanceFence `json:"fence"`
	Phase                string           `json:"phase"`
	Active               int              `json:"active"`
	Unknown              int              `json:"unknown"`
	LastSuccessfulBackup time.Time        `json:"last_successful_backup"`
	BackupID             string           `json:"backup_id,omitempty"`
	RecoveryComplete     bool             `json:"recovery_complete"`
}
type HealthReport struct {
	Live, Ready                      bool
	Reasons                          []string
	Maintenance                      MaintenanceState
	JournalSequence, JournalCapacity uint64
	JournalCapacityWarning           bool
}

// PermitHold is the longest a service-role operation of kind may run under
// its permit. A holder that declares it enforces it as a context deadline set
// before asking for the permit. Kinds without a hold return zero.
func PermitHold(kind string) time.Duration {
	switch kind {
	case "health":
		return time.Minute
	case "sweep", "access-audit":
		return 2 * time.Minute
	case "api":
		return 5 * time.Minute
	case "worker":
		return 15 * time.Minute
	case "api-startup", "worker-startup":
		return 30 * time.Minute
	}
	return 0
}

type permitHoldKey struct{}

type permitHold struct {
	kind string
	hold time.Duration
}

// HoldOverrunSlack is how long past its hold a held operation may take to
// return before the overrun hook ends the holder's process.
const HoldOverrunSlack = 30 * time.Second

var holdOverrun atomic.Pointer[func(kind string)]

// SetHoldOverrun installs the hook that ends this process when a held
// operation is still running, or ended with an uncertain outcome, HoldOverrunSlack
// after its hold. Only a process with the hook installed declares holds,
// because only then has every physical attempt of a held operation stopped by
// the permit's HoldUntil: the operation returned with a settled outcome, or the
// process is gone. A service's main installs it; nil removes it.
func SetHoldOverrun(f func(kind string)) {
	if f == nil {
		holdOverrun.Store(nil)
		return
	}
	holdOverrun.Store(&f)
}

// PermitHoldRelease ends a held operation. Pass the context carrying the
// operation's outcome (see WithOperationOutcome), or nil when no permit was
// granted. The deadline is cancelled either way. The overrun hook stays armed
// unless no permit was granted or the outcome settled, since an uncertain
// outcome cannot prove an earlier physical attempt stopped.
type PermitHoldRelease func(outcome context.Context)

// WithPermitHold bounds an operation of kind by its PermitHold. When the
// overrun hook is installed it also arms it and marks the context so that the
// admission request declares the hold. The deadline starts now, before the
// permit is asked for, so the holder always stops no later than the
// supervisor's HoldUntil. Kinds without a hold return ctx unchanged.
func WithPermitHold(ctx context.Context, kind string) (context.Context, PermitHoldRelease) {
	return withPermitHoldFor(ctx, kind, PermitHold(kind), HoldOverrunSlack)
}
func withPermitHoldFor(ctx context.Context, kind string, hold, slack time.Duration) (context.Context, PermitHoldRelease) {
	if hold == 0 {
		return ctx, func(context.Context) {}
	}
	ctx, cancel := context.WithTimeout(ctx, hold)
	hook := holdOverrun.Load()
	if hook == nil {
		return ctx, func(context.Context) { cancel() }
	}
	timer := time.AfterFunc(hold+slack, func() { (*hook)(kind) })
	var once sync.Once
	release := func(outcome context.Context) {
		once.Do(func() {
			cancel()
			if outcome == nil || OperationSettled(outcome) {
				timer.Stop()
			}
		})
	}
	return context.WithValue(ctx, permitHoldKey{}, permitHold{kind: kind, hold: hold}), release
}

// DeclaredPermitHold returns the hold to declare when asking for a permit of
// kind under ctx. It is zero unless WithPermitHold armed it for that same kind
// and ctx still carries a deadline no later than the hold allows.
func DeclaredPermitHold(ctx context.Context, kind string) time.Duration {
	h, ok := ctx.Value(permitHoldKey{}).(permitHold)
	if !ok || h.kind != kind {
		return 0
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > h.hold {
		return 0
	}
	return h.hold
}

type declaredHoldKey struct{}

// WithDeclaredHold carries a hold that a holder declared over the control API
// into the supervisor's admission. Only the control handler sets it.
func WithDeclaredHold(ctx context.Context, hold time.Duration) context.Context {
	return context.WithValue(ctx, declaredHoldKey{}, hold)
}

// DeclaredHold is the hold WithDeclaredHold or WithPermitHold put on ctx for
// kind, or zero.
func DeclaredHold(ctx context.Context, kind string) time.Duration {
	if h, ok := ctx.Value(declaredHoldKey{}).(time.Duration); ok {
		return h
	}
	return DeclaredPermitHold(ctx, kind)
}

package platform

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// WaitForAdmission retries until a permit of kind is granted. It returns the
// context the admitted operation must run on, bounded by the kind's
// domain.PermitHold from just before the granting request, and its release,
// which the caller calls with the operation's outcome context once the
// operation returns and before it finishes the permit.
// Only the wait observes ctx's own deadline being reached before admission.
func WaitForAdmission(ctx context.Context, a WorkerAdmission, instance, kind string) (domain.OperationPermit, context.Context, domain.PermitHoldRelease, error) {
	for {
		held, cancel := domain.WithPermitHold(ctx, kind)
		p, err := a.Admit(held, instance, kind)
		if err == nil {
			return p, held, cancel, nil
		}
		cancel(nil)
		if ctx.Err() != nil {
			return domain.OperationPermit{}, nil, nil, ctx.Err()
		}
		if !errors.Is(err, domain.ErrMaintenance) && !errors.Is(err, domain.ErrUnavailable) {
			return domain.OperationPermit{}, nil, nil, err
		}
		if !waitWorker(ctx, time.Second) {
			return domain.OperationPermit{}, nil, nil, ctx.Err()
		}
	}
}

// FinishOperation leaves uncertain effects durably registered. A failed Finish
// can be retried by exact identity; it is never treated as proof of drain.
func FinishOperation(ctx context.Context, a WorkerAdmission, p domain.OperationPermit, outcome error) error {
	if !domain.OperationSettled(ctx) {
		return errors.Join(outcome, domain.ErrUnknownOutcome)
	}
	if err := finishWithRetry(context.WithoutCancel(ctx), a, p); err != nil {
		return err
	}
	return outcome
}

// finishRetryWindow bounds how long a holder keeps retrying Finish while the
// supervisor is unreachable, such as during a graph restart. The supervisor
// keeps the permit across its restart, so the exact retry releases it there.
const finishRetryWindow = 10 * time.Minute

func finishWithRetry(ctx context.Context, a WorkerAdmission, p domain.OperationPermit) error {
	deadline := time.Now().Add(finishRetryWindow)
	delay := 250 * time.Millisecond
	for {
		attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := a.Finish(attempt, p)
		cancel()
		if err == nil || !errors.Is(err, domain.ErrUnavailable) || time.Now().Add(delay).After(deadline) {
			return err
		}
		time.Sleep(delay)
		delay = min(delay*2, 15*time.Second)
	}
}

func AdmittedHandler(a WorkerAdmission, instance string, next http.Handler) http.Handler {
	return AdmittedHandlerRefusing(a, instance, next, func(w http.ResponseWriter, _ *http.Request) { WriteUnready(w, "maintenance") })
}

// AdmittedHandlerRefusing is AdmittedHandler with the response written when
// admission refuses a request, for endpoints whose clients expect their own
// protocol's error.
func AdmittedHandlerRefusing(a WorkerAdmission, instance string, next http.Handler, refuse http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The request stops at its hold, which the permit declares, so the
		// supervisor can settle the permit if this process dies mid-request.
		held, release := domain.WithPermitHold(r.Context(), "api")
		ctx := domain.WithOperationOutcome(held)
		r = r.WithContext(ctx)
		p, err := a.Admit(ctx, instance, "api")
		if err != nil {
			refuse(w, r)
			release(nil)
			return
		}
		next.ServeHTTP(w, r)
		release(ctx)
		finishRequest(ctx, a, p)
	})
}

// finishRequest finishes a request's permit without holding back its response:
// if the supervisor is unreachable, the retry continues after the handler
// returns.
func finishRequest(ctx context.Context, a WorkerAdmission, p domain.OperationPermit) {
	if !domain.OperationSettled(ctx) {
		return
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	err := a.Finish(cleanup, p)
	cancel()
	if errors.Is(err, domain.ErrUnavailable) {
		go func() { _ = finishWithRetry(context.WithoutCancel(ctx), a, p) }()
	}
}

func WriteUnready(w http.ResponseWriter, reason string) {
	if reason != "maintenance" {
		reason = "starting"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`{"live":true,"ready":false,"reasons":["` + reason + `"]}`))
}

// InstallHoldWatchdog makes this process declare permit holds, and ends it if
// a held operation is still running, or ended with an uncertain outcome,
// domain.HoldOverrunSlack past its hold. Exiting is what lets the supervisor
// settle the permit after its HoldUntil without anyone proving the process
// ended: by then either the operation settled or no process is left to run
// it. Only a service's main calls this.
func InstallHoldWatchdog(logger *slog.Logger) {
	domain.SetHoldOverrun(func(kind string) {
		logger.Error("permit_hold_overrun", "reason", "maintenance", "operation_kind", kind)
		os.Exit(1)
	})
}

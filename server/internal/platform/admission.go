package platform

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

func WaitForAdmission(ctx context.Context, a WorkerAdmission, instance, kind string) (domain.OperationPermit, error) {
	for {
		p, err := a.Admit(ctx, instance, kind)
		if err == nil {
			return p, nil
		}
		if ctx.Err() != nil {
			return domain.OperationPermit{}, ctx.Err()
		}
		if !errors.Is(err, domain.ErrMaintenance) && !errors.Is(err, domain.ErrUnavailable) {
			return domain.OperationPermit{}, err
		}
		if !waitWorker(ctx, time.Second) {
			return domain.OperationPermit{}, ctx.Err()
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
		ctx := domain.WithOperationOutcome(r.Context())
		r = r.WithContext(ctx)
		p, err := a.Admit(ctx, instance, "api")
		if err != nil {
			refuse(w, r)
			return
		}
		next.ServeHTTP(w, r)
		finishRequest(r.Context(), a, p)
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

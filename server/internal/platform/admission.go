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
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := a.Finish(cleanup, p); err != nil {
		return err
	}
	return outcome
}

func AdmittedHandler(a WorkerAdmission, instance string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := domain.WithOperationOutcome(r.Context())
		r = r.WithContext(ctx)
		p, err := a.Admit(ctx, instance, "api")
		if err != nil {
			WriteUnready(w, "maintenance")
			return
		}
		next.ServeHTTP(w, r)
		_ = FinishOperation(r.Context(), a, p, nil)
	})
}

func WriteUnready(w http.ResponseWriter, reason string) {
	if reason != "maintenance" {
		reason = "starting"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`{"live":true,"ready":false,"reasons":["` + reason + `"]}`))
}

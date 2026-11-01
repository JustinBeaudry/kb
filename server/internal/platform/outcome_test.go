package platform_test

import (
	"context"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAdmissionUsesEffectsInsteadOfFinalStatus(t *testing.T) {
	for _, code := range []int{403, 409, 410, 503} {
		for _, uncertain := range []bool{false, true} {
			a := &workerAdmission{make(chan struct{}, 1), make(chan struct{}, 1)}
			h := platform.AdmittedHandler(a, "instance", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if uncertain {
					domain.MarkEffectUncertain(r.Context())
				}
				w.WriteHeader(code)
			}))
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
			assert.Equal(t, !uncertain, len(a.finished) == 1)
		}
	}
}
func TestFinishRequiresTrackerAndPreservesKnownReadError(t *testing.T) {
	a := &workerAdmission{make(chan struct{}, 1), make(chan struct{}, 1)}
	require.ErrorIs(t, platform.FinishOperation(t.Context(), a, domain.OperationPermit{}, nil), domain.ErrUnknownOutcome)
	assert.Empty(t, a.finished)
	ctx := domain.WithOperationOutcome(t.Context())
	require.ErrorIs(t, platform.FinishOperation(ctx, a, domain.OperationPermit{}, domain.ErrIncomplete), domain.ErrIncomplete)
	assert.Len(t, a.finished, 1)
}

func TestWorkerKnownBusinessErrorCannotHideEarlierUnknownMutation(t *testing.T) {
	a := &workerAdmission{make(chan struct{}, 1), make(chan struct{}, 1)}
	jobs := workerScript{claim: func(context.Context, string, []string, time.Time) (domain.Job, domain.Lease, error) {
		return domain.Job{ID: "job"}, domain.Lease{JobID: "job"}, nil
	}, fail: func(context.Context, domain.Lease, time.Time, string) error { return nil }}
	worker := platform.Worker{ID: "worker", InstanceID: "instance", Admission: a, Kinds: []string{"verify"}, Jobs: jobs, RunJob: func(ctx context.Context, _ domain.Job, _ domain.Lease) error {
		domain.MarkEffectUncertain(ctx)
		return domain.ErrDeleted
	}}
	require.ErrorIs(t, worker.Run(t.Context()), domain.ErrUnknownOutcome)
	assert.Empty(t, a.finished)
}
func TestWorkerKnownClaimReadFailureFinishesPermit(t *testing.T) {
	a := &workerAdmission{make(chan struct{}, 1), make(chan struct{}, 1)}
	jobs := workerScript{claim: func(context.Context, string, []string, time.Time) (domain.Job, domain.Lease, error) {
		return domain.Job{}, domain.Lease{}, domain.ErrUnavailable
	}}
	worker := platform.Worker{ID: "worker", InstanceID: "instance", Admission: a, Kinds: []string{"verify"}, Jobs: jobs, RunJob: func(context.Context, domain.Job, domain.Lease) error { return nil }}
	require.ErrorIs(t, worker.Run(t.Context()), domain.ErrUnavailable)
	assert.Len(t, a.finished, 1)
}

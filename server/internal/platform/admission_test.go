package platform_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPAdmissionCoversHandlerAndRetainsUncertainWrites(t *testing.T) {
	for _, status := range []int{200, 403, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			a := &workerAdmission{make(chan struct{}, 1), make(chan struct{}, 1)}
			h := platform.AdmittedHandler(a, "instance", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Len(t, a.admitted, 1)
				assert.Empty(t, a.finished)
				w.WriteHeader(status)
			}))
			response := httptest.NewRecorder()
			h.ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), "POST", "/v1/artifacts", nil))
			assert.Equal(t, status, response.Code)
			assert.Len(t, a.finished, 1, "status alone is not mutation uncertainty")
		})
	}
}

type flakyFinish struct {
	mu       sync.Mutex
	failures int
	calls    int
}

func (f *flakyFinish) finishedCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *flakyFinish) Admit(context.Context, string, string) (domain.OperationPermit, error) {
	return domain.OperationPermit{ID: "permit", InstanceID: "instance", Kind: "api", Generation: 1}, nil
}
func (f *flakyFinish) Finish(context.Context, domain.OperationPermit) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls <= f.failures {
		return domain.ErrUnavailable
	}
	return nil
}

func TestFinishOperationRetriesWhileTheSupervisorRestarts(t *testing.T) {
	a := &flakyFinish{failures: 2}
	ctx := domain.WithOperationOutcome(t.Context())
	require.NoError(t, platform.FinishOperation(ctx, a, domain.OperationPermit{ID: "permit", InstanceID: "instance", Kind: "api", Generation: 1}, nil))
	assert.Equal(t, 3, a.calls)
}

type refusingFinish struct{ calls int }

func (f *refusingFinish) Admit(context.Context, string, string) (domain.OperationPermit, error) {
	return domain.OperationPermit{}, nil
}
func (f *refusingFinish) Finish(context.Context, domain.OperationPermit) error {
	f.calls++
	return domain.ErrStale
}

func TestFinishOperationDoesNotRetryARefusal(t *testing.T) {
	a := &refusingFinish{}
	ctx := domain.WithOperationOutcome(t.Context())
	require.ErrorIs(t, platform.FinishOperation(ctx, a, domain.OperationPermit{ID: "permit"}, nil), domain.ErrStale)
	assert.Equal(t, 1, a.calls)
}

func TestAdmittedHandlerAnswersBeforeRetryingAnUnreachableFinish(t *testing.T) {
	a := &flakyFinish{failures: 1}
	h := platform.AdmittedHandler(a, "instance", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), "GET", "/v1/x", nil))
	assert.Equal(t, http.StatusNoContent, response.Code)
	assert.Eventually(t, func() bool { return a.finishedCalls() == 2 }, 5*time.Second, 10*time.Millisecond)
}

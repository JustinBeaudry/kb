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

// holdRecorder records the hold each admission declared.
type holdRecorder struct {
	mu    sync.Mutex
	holds map[string]time.Duration
}

func (r *holdRecorder) Admit(ctx context.Context, instance, kind string) (domain.OperationPermit, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.holds == nil {
		r.holds = map[string]time.Duration{}
	}
	r.holds[kind] = domain.DeclaredPermitHold(ctx, kind)
	return domain.OperationPermit{ID: kind, InstanceID: instance, Kind: kind, Generation: 1}, nil
}
func (r *holdRecorder) Finish(context.Context, domain.OperationPermit) error { return nil }
func (r *holdRecorder) hold(kind string) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.holds[kind]
}

func installNoopWatchdog(t *testing.T) {
	t.Helper()
	domain.SetHoldOverrun(func(string) {})
	t.Cleanup(func() { domain.SetHoldOverrun(nil) })
}

func TestAdmittedRequestsDeclareAndKeepTheAPIHold(t *testing.T) {
	installNoopWatchdog(t)
	a := &holdRecorder{}
	var deadline time.Time
	var hasDeadline bool
	h := platform.AdmittedHandler(a, "api-one", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deadline, hasDeadline = r.Context().Deadline()
		w.WriteHeader(http.StatusNoContent)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	assert.Equal(t, 5*time.Minute, a.hold("api"))
	require.True(t, hasDeadline, "the handler runs under the hold it declared")
	assert.WithinDuration(t, time.Now().Add(5*time.Minute), deadline, 5*time.Second)
}

func TestStartupAdmissionReturnsTheHeldContext(t *testing.T) {
	installNoopWatchdog(t)
	a := &holdRecorder{}
	p, held, release, err := platform.WaitForAdmission(context.Background(), a, "worker-one", "worker-startup")
	require.NoError(t, err)
	defer release(nil)
	assert.Equal(t, "worker-startup", p.Kind)
	assert.Equal(t, 30*time.Minute, a.hold("worker-startup"))
	deadline, ok := held.Deadline()
	require.True(t, ok)
	assert.WithinDuration(t, time.Now().Add(30*time.Minute), deadline, 5*time.Second)
}

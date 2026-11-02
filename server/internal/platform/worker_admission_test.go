package platform_test

import (
	"context"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type workerAdmission struct{ admitted, finished chan struct{} }

func (a *workerAdmission) Admit(context.Context, string, string) (domain.OperationPermit, error) {
	a.admitted <- struct{}{}
	return domain.OperationPermit{ID: "permit", InstanceID: "instance", Kind: "worker", Generation: 1}, nil
}
func (a *workerAdmission) Finish(context.Context, domain.OperationPermit) error {
	a.finished <- struct{}{}
	return nil
}

func TestWorkerDoesNotReleasePermitWhileCanceledHandlerStillRuns(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	started, release := make(chan struct{}), make(chan struct{})
	admission := &workerAdmission{make(chan struct{}, 1), make(chan struct{}, 1)}
	jobs := workerScript{
		claim: func(context.Context, string, []string, time.Time) (domain.Job, domain.Lease, error) {
			select {
			case <-admission.admitted:
			default:
				t.Error("claim started without admission")
			}
			return domain.Job{ID: "job", Kind: "verify"}, domain.Lease{JobID: "job"}, nil
		},
		fail: func(context.Context, domain.Lease, time.Time, string) error { return nil },
	}
	worker := platform.Worker{ID: "worker", InstanceID: "instance", Admission: admission, Kinds: []string{"verify"}, Jobs: jobs, RunJob: func(ctx context.Context, _ domain.Job, _ domain.Lease) error {
		close(started)
		<-release
		domain.MarkEffectUncertain(ctx)
		return nil
	}}
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	<-started
	cancel()
	select {
	case early := <-done:
		assert.NoError(t, early)
		t.Error("worker returned before handler joined")
	case <-time.After(20 * time.Millisecond):
	}
	assert.Empty(t, admission.finished)
	close(release)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("worker failed to join")
	}
	assert.Empty(t, admission.finished, "cancellation cannot prove remote effect settlement")
}

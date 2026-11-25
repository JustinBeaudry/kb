package repository_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scriptedLLM struct {
	mu       sync.Mutex
	proposal []domain.Proposal
	calls    int
}

func (s *scriptedLLM) Generate(_ context.Context, r domain.GenerationRequest) (domain.Generation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	out := make([]domain.Proposal, len(s.proposal))
	copy(out, s.proposal)
	for i := range out {
		out[i].Status = domain.ClaimCandidate
	}
	return domain.Generation{Proposals: out, StopReason: "end_turn", Usage: domain.Usage{Tokens: 10, TokensKnown: true, CostMicroUSD: 3, CostKnown: true}}, nil
}

func (s *scriptedLLM) InputBound(r domain.GenerationRequest) (int64, error) {
	return 4096 + 6*r.InputBytes(), nil
}

type recordingSender struct{ sent []string }

func (r *recordingSender) Send(_ context.Context, key, _ string, _ string) error {
	r.sent = append(r.sent, key)
	return nil
}

func seedDreamProject(t *testing.T, store *graph.Store, memory *repository.MemoryRepository, routes []string) {
	t.Helper()
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		for _, c := range []domain.ClaimRevision{
			{ClaimID: "claim-a", Revision: "rev-a", Text: "Service listens on 8080 in staging.", Kind: "observation", Scope: domain.Scope{ProjectID: "demo", TaskID: "task"}, Status: domain.ClaimCandidate, SubmittedBy: "agent"},
			{ClaimID: "claim-b", Revision: "rev-b", Text: "Health check probes 9090 in staging.", Kind: "observation", Scope: domain.Scope{ProjectID: "demo", TaskID: "task"}, Status: domain.ClaimCandidate, SubmittedBy: "agent"},
		} {
			if err := memory.PutCandidate(ctx, tx, c); err != nil {
				return err
			}
		}
		return nil
	}))
	seedCapturePolicy(t, store, domain.Policy{Revision: "policy-1", AllowedModelRoutes: routes})
}

func TestModelBudgetLedgerOnRealGraph(t *testing.T) {
	store, _, _ := jobsGraph(t)
	repo := repository.NewModelBudgetRepository(store)
	now := time.Now().UTC()
	budget := service.NewModelBudgetService(store, repo, 1000, func() time.Time { return now })

	t.Run("concurrent reservations stay within cap", func(t *testing.T) {
		var wg sync.WaitGroup
		var mu sync.Mutex
		granted := 0
		for range 12 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := budget.Reserve(context.Background(), domain.Scope{ProjectID: "demo"}, "primary", 300)
				if err == nil {
					mu.Lock()
					granted++
					mu.Unlock()
				} else if !errors.Is(err, domain.ErrBudget) {
					t.Errorf("unexpected reservation error: %v", err)
				}
			}()
		}
		wg.Wait()

		status, err := budget.Status(t.Context())

		require.NoError(t, err)
		assert.Equal(t, 3, granted)
		assert.Equal(t, int64(900), status.CommittedMicroUSD)
	})
	t.Run("unknown settlement keeps the hold until reconciled", func(t *testing.T) {
		other := service.NewModelBudgetService(store, repo, 1000, func() time.Time { return now.AddDate(0, 0, 1) })
		r, err := other.Reserve(t.Context(), domain.Scope{ProjectID: "demo"}, "primary", 400)
		require.NoError(t, err)
		require.NoError(t, other.Settle(t.Context(), r.ID, domain.Usage{}))
		held, err := other.Status(t.Context())
		require.NoError(t, err)

		require.NoError(t, other.Reconcile(t.Context(), r.ID, domain.Usage{Tokens: 5, TokensKnown: true, CostMicroUSD: 25, CostKnown: true}))

		released, err := other.Status(t.Context())
		require.NoError(t, err)
		assert.Equal(t, int64(400), held.CommittedMicroUSD)
		assert.Equal(t, 1, held.UnreconciledReservations)
		assert.Equal(t, int64(25), released.CommittedMicroUSD)
		assert.Zero(t, released.UnreconciledReservations)
	})
}

func TestDreamCycleOnRealGraph(t *testing.T) {
	store, jobsRepo, jobs := jobsGraph(t)
	memory := repository.NewMemoryRepository(store)
	findings := repository.NewFindingsRepository(store)
	seedDreamProject(t, store, memory, []string{"primary"})
	route := domain.ModelRoute{Name: "primary", Model: "fixture", MaxOutputTokens: 100, InputMicroUSDPerMTok: 1_000_000, OutputMicroUSDPerMTok: 1_000_000}
	budget := service.NewModelBudgetService(store, repository.NewModelBudgetRepository(store), 1_000_000, nil)
	llm := &scriptedLLM{proposal: []domain.Proposal{{Kind: domain.ProposalContradiction, Text: "Staging service and health check disagree on port.", Rationale: "8080 vs 9090", SupportRevisionIDs: []string{"rev-a", "rev-b"}}}}
	dreaming := service.NewDreamingService(store, findings, memory, jobs, jobsRepo, budget, llm, service.DreamingConfig{Routes: []domain.ModelRoute{route}, DigestHour: 9})
	run := func(kind string, handler func(context.Context, domain.Job, domain.Lease) error) int {
		t.Helper()
		ran := 0
		for {
			job, lease, err := jobs.Claim(t.Context(), "worker", []string{kind}, time.Now().UTC().Add(48*time.Hour))
			if errors.Is(err, domain.ErrIncomplete) {
				return ran
			}
			require.NoError(t, err)
			require.NoError(t, handler(t.Context(), job, lease))
			ran++
		}
	}
	enqueued, err := dreaming.Schedule(t.Context(), time.Time{})
	require.NoError(t, err)
	require.Equal(t, 1, enqueued)

	require.Equal(t, 1, run(service.DreamJobKind, dreaming.Process))
	sender := &recordingSender{}
	notify := service.NewNotificationService(store, findings, jobs, sender, "thread")
	require.Equal(t, 1, run(service.NotifyJobKind, notify.Dispatch))
	again, err := dreaming.Schedule(t.Context(), time.Time{})

	require.NoError(t, err)
	assert.Zero(t, again, "unchanged inputs schedule no work")
	assert.Equal(t, 1, llm.calls)
	assert.Len(t, sender.sent, 1)
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		inputs, err := findings.Inputs(ctx, tx, "demo", 50)
		require.NoError(t, err)
		var dream []domain.DreamInput
		for _, in := range inputs {
			if in.SubmittedBy == domain.DreamClientID {
				dream = append(dream, in)
			}
		}
		require.Len(t, dream, 1)
		assert.Equal(t, domain.ClaimCandidate, dream[0].Status)
		pending, reasons, err := findings.DreamStatus(ctx, tx, []string{"demo"})
		assert.Zero(t, pending)
		assert.Empty(t, reasons)
		return err
	}))
}

func TestDreamWithoutRouteStaysPendingOnRealGraph(t *testing.T) {
	store, jobsRepo, jobs := jobsGraph(t)
	memory := repository.NewMemoryRepository(store)
	findings := repository.NewFindingsRepository(store)
	seedDreamProject(t, store, memory, nil)
	budget := service.NewModelBudgetService(store, repository.NewModelBudgetRepository(store), 1000, nil)
	dreaming := service.NewDreamingService(store, findings, memory, jobs, jobsRepo, budget, &scriptedLLM{}, service.DreamingConfig{})
	_, err := dreaming.Schedule(t.Context(), time.Time{})
	require.NoError(t, err)
	job, lease, err := jobs.Claim(t.Context(), "worker", []string{service.DreamJobKind}, time.Now().UTC())
	require.NoError(t, err)

	require.NoError(t, dreaming.Process(t.Context(), job, lease))

	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		queued, err := jobsRepo.List(ctx, tx, domain.JobQueued, 10)
		require.NoError(t, err)
		require.Len(t, queued, 1)
		assert.Equal(t, "model_route_unavailable", queued[0].ReasonCode)
		assert.Zero(t, queued[0].Attempts, "deferral does not consume attempts")
		pending, reasons, err := findings.DreamStatus(ctx, tx, []string{"demo"})
		assert.Equal(t, 1, pending)
		assert.Equal(t, []string{"model_route_unavailable"}, reasons)
		return err
	}))
}

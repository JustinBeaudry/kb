package service_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lockTx serializes callbacks like the OwnerState lock in the real store.
type lockTx struct{ mu sync.Mutex }

func (l *lockTx) Within(ctx context.Context, fn func(context.Context, service.Tx) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return fn(ctx, nil)
}

type fakeBudgetRepo struct {
	reservations map[string]domain.Reservation
}

func newFakeBudgetRepo() *fakeBudgetRepo {
	return &fakeBudgetRepo{reservations: map[string]domain.Reservation{}}
}
func (f *fakeBudgetRepo) Committed(_ context.Context, _ service.Tx, day string) (int64, error) {
	var total int64
	for _, r := range f.reservations {
		if r.Day == day {
			total += r.Committed()
		}
	}
	return total, nil
}
func (f *fakeBudgetRepo) PutReservation(_ context.Context, _ service.Tx, r domain.Reservation) error {
	f.reservations[r.ID] = r
	return nil
}
func (f *fakeBudgetRepo) Reservation(_ context.Context, _ service.Tx, id string) (domain.Reservation, error) {
	r, ok := f.reservations[id]
	if !ok {
		return r, domain.ErrConflict
	}
	return r, nil
}
func (f *fakeBudgetRepo) UpdateReservation(_ context.Context, _ service.Tx, r domain.Reservation) error {
	f.reservations[r.ID] = r
	return nil
}
func (f *fakeBudgetRepo) Unreconciled(_ context.Context, _ service.Tx) (int, error) {
	n := 0
	for _, r := range f.reservations {
		if r.State == domain.ReservationUnreconciled {
			n++
		}
	}
	return n, nil
}

type fakeDreamRepo struct {
	policy       domain.Policy
	inputs       []service.DreamInput
	current      map[string]bool
	cursor       map[string]string
	findings     map[string]domain.Finding
	notices      map[string]domain.Notice
	onRevisions  func()
	changed      []string
	pendingJobs  int
	pendingCodes []string
}

func newFakeDreamRepo(routes ...string) *fakeDreamRepo {
	return &fakeDreamRepo{policy: domain.Policy{Revision: "policy-1", AllowedModelRoutes: routes}, current: map[string]bool{}, cursor: map[string]string{}, findings: map[string]domain.Finding{}, notices: map[string]domain.Notice{}}
}
func (f *fakeDreamRepo) add(in service.DreamInput) {
	f.inputs = append(f.inputs, in)
	f.current[in.Revision] = true
}
func (f *fakeDreamRepo) Policy(context.Context, service.Tx, string) (domain.Policy, error) {
	return f.policy, nil
}
func (f *fakeDreamRepo) Inputs(context.Context, service.Tx, string, int) ([]service.DreamInput, error) {
	return slices.Clone(f.inputs), nil
}
func (f *fakeDreamRepo) Revisions(_ context.Context, _ service.Tx, ids []string) ([]service.DreamInput, error) {
	if f.onRevisions != nil {
		f.onRevisions()
	}
	var out []service.DreamInput
	for _, in := range f.inputs {
		if slices.Contains(ids, in.Revision) {
			in.Current = f.current[in.Revision]
			out = append(out, in)
		}
	}
	return out, nil
}
func (f *fakeDreamRepo) Cursor(_ context.Context, _ service.Tx, project string) (string, error) {
	return f.cursor[project], nil
}
func (f *fakeDreamRepo) PutCursor(_ context.Context, _ service.Tx, project, digest string) error {
	f.cursor[project] = digest
	return nil
}
func (f *fakeDreamRepo) ChangedProjects(context.Context, service.Tx, time.Time, int) ([]string, error) {
	return f.changed, nil
}
func (f *fakeDreamRepo) FindingByDigest(_ context.Context, _ service.Tx, digest string) (domain.Finding, bool, error) {
	for _, x := range f.findings {
		if x.Digest == digest {
			return x, true, nil
		}
	}
	return domain.Finding{}, false, nil
}
func (f *fakeDreamRepo) PutFinding(_ context.Context, _ service.Tx, x domain.Finding) error {
	f.findings[x.ID] = x
	return nil
}
func (f *fakeDreamRepo) AddToNotice(_ context.Context, _ service.Tx, n domain.Notice) error {
	existing, ok := f.notices[n.ID]
	if ok {
		existing.FindingIDs = append(existing.FindingIDs, n.FindingIDs...)
		f.notices[n.ID] = existing
		return nil
	}
	f.notices[n.ID] = n
	return nil
}
func (f *fakeDreamRepo) Notice(_ context.Context, _ service.Tx, id string) (domain.Notice, error) {
	n, ok := f.notices[id]
	if !ok {
		return n, domain.ErrConflict
	}
	return n, nil
}
func (f *fakeDreamRepo) Findings(_ context.Context, _ service.Tx, ids []string) ([]domain.Finding, error) {
	var out []domain.Finding
	for _, id := range ids {
		if x, ok := f.findings[id]; ok {
			out = append(out, x)
		}
	}
	return out, nil
}
func (f *fakeDreamRepo) MarkNotice(_ context.Context, _ service.Tx, id string, state domain.NoticeState, delivered, suppressed []string) error {
	n := f.notices[id]
	n.State = state
	f.notices[id] = n
	for _, d := range delivered {
		x := f.findings[d]
		x.Status = domain.FindingNotified
		f.findings[d] = x
	}
	for _, d := range suppressed {
		x := f.findings[d]
		if x.Status == domain.FindingPending {
			x.Status = domain.FindingSuppressed
		}
		f.findings[d] = x
	}
	return nil
}
func (f *fakeDreamRepo) DreamStatus(context.Context, service.Tx, []string) (int, []string, error) {
	return f.pendingJobs, f.pendingCodes, nil
}

type fakeCandidates struct{ written []domain.ClaimRevision }

func (f *fakeCandidates) PutCandidate(_ context.Context, _ service.Tx, c domain.ClaimRevision) error {
	f.written = append(f.written, c)
	return nil
}

type fakeEnqueuer struct{ jobs []domain.Job }

func (f *fakeEnqueuer) Enqueue(_ context.Context, _ service.Tx, j domain.Job) error {
	f.jobs = append(f.jobs, j)
	return nil
}

type fakeDreamJobs struct {
	tx        *lockTx
	committed int
	deferred  []string
}

func (f *fakeDreamJobs) Commit(ctx context.Context, _ domain.Lease, _ string, apply func(context.Context, service.Tx) error) error {
	err := f.tx.Within(ctx, apply)
	if err == nil {
		f.committed++
	}
	return err
}
func (f *fakeDreamJobs) Defer(_ context.Context, _ domain.Lease, _ time.Time, reason string) error {
	f.deferred = append(f.deferred, reason)
	return nil
}

type fakeLLM struct {
	mu        sync.Mutex
	responses []func(domain.GenerationRequest) (domain.Generation, error)
	requests  []domain.GenerationRequest
	bound     int64
}

func (f *fakeLLM) InputBound(domain.GenerationRequest) (int64, error) {
	if f.bound == 0 {
		return 1000, nil
	}
	return f.bound, nil
}

func (f *fakeLLM) Generate(_ context.Context, r domain.GenerationRequest) (domain.Generation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r)
	if len(f.responses) == 0 {
		return domain.Generation{}, domain.ErrUnavailable
	}
	next := f.responses[0]
	if len(f.responses) > 1 {
		f.responses = f.responses[1:]
	}
	return next(r)
}

func knownUsage(cost int64) domain.Usage {
	return domain.Usage{Tokens: 100, TokensKnown: true, CostMicroUSD: cost, CostKnown: true}
}
func propose(proposals ...domain.Proposal) func(domain.GenerationRequest) (domain.Generation, error) {
	return func(domain.GenerationRequest) (domain.Generation, error) {
		for i := range proposals {
			proposals[i].Status = domain.ClaimCandidate
		}
		return domain.Generation{Proposals: proposals, Usage: knownUsage(10), StopReason: "end_turn"}, nil
	}
}

var pricedRoute = domain.ModelRoute{Name: "primary", Model: "fixture-model", MaxOutputTokens: 1000, InputMicroUSDPerMTok: 1_000_000, OutputMicroUSDPerMTok: 5_000_000}

type dreamHarness struct {
	tx         *lockTx
	repo       *fakeDreamRepo
	budgetRepo *fakeBudgetRepo
	budget     *service.ModelBudgetService
	candidates *fakeCandidates
	enqueued   *fakeEnqueuer
	jobs       *fakeDreamJobs
	llm        *fakeLLM
	service    *service.DreamingService
}

var dreamNow = time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)

func newDreamHarness(t *testing.T, routes []domain.ModelRoute, dailyCap int64, repo *fakeDreamRepo) *dreamHarness {
	t.Helper()
	h := &dreamHarness{tx: &lockTx{}, repo: repo, budgetRepo: newFakeBudgetRepo(), candidates: &fakeCandidates{}, enqueued: &fakeEnqueuer{}, llm: &fakeLLM{}}
	h.jobs = &fakeDreamJobs{tx: h.tx}
	h.budget = service.NewModelBudgetService(h.tx, h.budgetRepo, dailyCap, func() time.Time { return dreamNow })
	h.service = service.NewDreamingService(h.tx, repo, h.candidates, h.jobs, h.enqueued, h.budget, h.llm, service.DreamingConfig{Routes: routes, DigestHour: 9, Now: func() time.Time { return dreamNow }})
	return h
}

var dreamJob = domain.Job{ID: "dream-job", Kind: service.DreamJobKind, InputDigest: "digest-1", PolicyRevision: "policy-1", Scope: domain.Scope{ProjectID: "project"}, DueAt: dreamNow}
var dreamLease = domain.Lease{JobID: "dream-job", WorkerID: "worker", Fence: 1}

func observedInputs(repo *fakeDreamRepo) {
	repo.add(service.DreamInput{Revision: "rev-a", ClaimID: "claim-a", Text: "Service uses port 8080 in staging.", Scope: domain.Scope{ProjectID: "project", TaskID: "task"}, Status: domain.ClaimVerified, SubmittedBy: "agent"})
	repo.add(service.DreamInput{Revision: "rev-b", ClaimID: "claim-b", Text: "Staging health check targets port 9090.", Scope: domain.Scope{ProjectID: "project", TaskID: "task"}, Status: domain.ClaimCandidate, SubmittedBy: "agent"})
}

func TestDreamCannotVerifyOwnConclusion(t *testing.T) {
	for _, tc := range []struct {
		name     string
		proposal domain.Proposal
		written  int
	}{
		{"narrow observation stays candidate", domain.Proposal{Kind: domain.ProposalContradiction, Text: "Port expectations disagree between service and health check.", Rationale: "rev-a and rev-b name different ports", SupportRevisionIDs: []string{"rev-a", "rev-b"}}, 1},
		{"own earlier conclusion is not support", domain.Proposal{Kind: domain.ProposalConnection, Text: "The port conflict is confirmed.", Rationale: "earlier dream said so", SupportRevisionIDs: []string{"rev-dream"}}, 0},
		{"mixed own and observed support is rejected", domain.Proposal{Kind: domain.ProposalConnection, Text: "Port conflict confirmed by repetition.", Rationale: "rev-a plus earlier dream", SupportRevisionIDs: []string{"rev-a", "rev-dream"}}, 0},
		{"unsupported fact is rejected", domain.Proposal{Kind: domain.ProposalLesson, Text: "Always use port 443.", Rationale: "general knowledge", SupportRevisionIDs: []string{"rev-unknown"}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeDreamRepo("primary")
			observedInputs(repo)
			repo.add(service.DreamInput{Revision: "rev-dream", ClaimID: "dream-x", Text: "Port conflict exists.", Scope: domain.Scope{ProjectID: "project", TaskID: service.DreamTaskID}, Status: domain.ClaimCandidate, SubmittedBy: service.DreamClientID})
			h := newDreamHarness(t, []domain.ModelRoute{pricedRoute}, 1_000_000, repo)
			h.llm.responses = append(h.llm.responses, propose(tc.proposal))

			err := h.service.Process(context.Background(), dreamJob, dreamLease)

			require.NoError(t, err)
			require.Len(t, h.candidates.written, tc.written)
			for _, proposal := range h.candidates.written {
				assert.Equal(t, "candidate", string(proposal.Status))
				assert.Equal(t, service.DreamClientID, proposal.SubmittedBy)
			}
			for _, excerpt := range h.llm.requests[0].Excerpts {
				assert.NotEqual(t, "rev-dream", excerpt.RevisionID, "own conclusions are never sent as evidence")
			}
		})
	}
}

func TestSupportChangesBeforeDreamCommit(t *testing.T) {
	repo := newFakeDreamRepo("primary")
	observedInputs(repo)
	h := newDreamHarness(t, []domain.ModelRoute{pricedRoute}, 1_000_000, repo)
	h.llm.responses = append(h.llm.responses, propose(domain.Proposal{Kind: domain.ProposalContradiction, Text: "Ports disagree.", Rationale: "different ports", SupportRevisionIDs: []string{"rev-a", "rev-b"}}))
	// Support is invalidated after generation and before the commit recheck.
	repo.onRevisions = func() { repo.current["rev-a"] = false }

	err := h.service.Process(context.Background(), dreamJob, dreamLease)

	assert.ErrorIs(t, err, domain.ErrStale)
	assert.Empty(t, h.candidates.written)
	assert.Empty(t, repo.findings)
	assert.Empty(t, repo.cursor)
}

func TestUnknownUsageDoesNotBecomeZero(t *testing.T) {
	for _, tc := range []struct {
		name  string
		usage domain.Usage
		err   error
	}{
		{"provider omitted usage", domain.Usage{}, nil},
		{"timeout after reservation", domain.Usage{}, context.DeadlineExceeded},
		{"tokens known without price", domain.Usage{Tokens: 50, TokensKnown: true}, domain.ErrIncomplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeDreamRepo("primary")
			observedInputs(repo)
			h := newDreamHarness(t, []domain.ModelRoute{pricedRoute}, 1_000_000, repo)
			h.llm.responses = append(h.llm.responses, func(domain.GenerationRequest) (domain.Generation, error) {
				return domain.Generation{Usage: tc.usage}, tc.err
			})

			_ = h.service.Process(context.Background(), dreamJob, dreamLease)

			require.NotEmpty(t, h.budgetRepo.reservations)
			for _, r := range h.budgetRepo.reservations {
				settledUsage := r.Usage
				assert.False(t, settledUsage.CostKnown)
				assert.Equal(t, domain.ReservationUnreconciled, r.State)
				stillReservedMicros, err := h.budgetRepo.Committed(context.Background(), nil, r.Day)
				require.NoError(t, err)
				reservedMicros := r.MaxCostMicroUSD * int64(len(h.budgetRepo.reservations))
				assert.Equal(t, reservedMicros, stillReservedMicros)
			}
		})
	}
}

func TestConcurrentReservationsStayWithinBudget(t *testing.T) {
	const dailyCap, each, workers = 1000, 300, 16
	budget := service.NewModelBudgetService(&lockTx{}, newFakeBudgetRepo(), dailyCap, func() time.Time { return dreamNow })
	var wg sync.WaitGroup
	var mu sync.Mutex
	granted, exhausted := 0, 0
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := budget.Reserve(context.Background(), domain.Scope{ProjectID: "project"}, "primary", each)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				granted++
			} else if errors.Is(err, domain.ErrBudget) {
				exhausted++
			}
		}()
	}

	wg.Wait()

	assert.Equal(t, 3, granted)
	assert.Equal(t, workers-3, exhausted)
	status, err := budget.Status(context.Background())
	require.NoError(t, err)
	assert.LessOrEqual(t, status.CommittedMicroUSD, int64(dailyCap))
}

func TestNoAuthorizedRouteLeavesJobPending(t *testing.T) {
	for _, tc := range []struct {
		name     string
		routes   []domain.ModelRoute
		policy   []string
		cap      int64
		noLLM    bool
		reason   string
		reserved bool
	}{
		{"policy allows no route", []domain.ModelRoute{pricedRoute}, nil, 1000, false, "model_route_unavailable", false},
		{"policy names unconfigured route", []domain.ModelRoute{pricedRoute}, []string{"other"}, 1000, false, "model_route_unavailable", false},
		{"no provider configured", []domain.ModelRoute{pricedRoute}, []string{"primary"}, 1000, true, "model_route_unavailable", false},
		{"route has no pricing", []domain.ModelRoute{{Name: "primary", Model: "m", MaxOutputTokens: 100}}, []string{"primary"}, 1000, false, "model_pricing_unavailable", false},
		{"owner set no cap", []domain.ModelRoute{pricedRoute}, []string{"primary"}, 0, false, "model_budget_unconfigured", false},
		{"budget exhausted", []domain.ModelRoute{pricedRoute}, []string{"primary"}, 1, false, "model_budget_exhausted", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeDreamRepo(tc.policy...)
			observedInputs(repo)
			h := newDreamHarness(t, tc.routes, tc.cap, repo)
			if tc.noLLM {
				h.service = service.NewDreamingService(h.tx, repo, h.candidates, h.jobs, h.enqueued, h.budget, nil, service.DreamingConfig{Routes: tc.routes, Now: func() time.Time { return dreamNow }})
			}

			err := h.service.Process(context.Background(), dreamJob, dreamLease)

			require.NoError(t, err)
			assert.Equal(t, []string{tc.reason}, h.jobs.deferred)
			assert.Empty(t, h.llm.requests)
			assert.Empty(t, h.budgetRepo.reservations)
			assert.Zero(t, h.jobs.committed)
		})
	}
}

type fakeSender struct {
	sent []string
	err  error
}

func (f *fakeSender) Send(_ context.Context, key, _ string, body string) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, key+"|"+body)
	return nil
}

func notifyJob(noticeID string) domain.Job {
	return domain.Job{ID: noticeID, Kind: service.NotifyJobKind, InputDigest: noticeID, PolicyRevision: service.NoticePolicyRevision, DueAt: dreamNow}
}

func TestNoChangeFindingSendsNoNotice(t *testing.T) {
	repo := newFakeDreamRepo("primary")
	observedInputs(repo)
	h := newDreamHarness(t, []domain.ModelRoute{pricedRoute}, 1_000_000, repo)
	finding := domain.Proposal{Kind: domain.ProposalContradiction, Text: "Ports disagree.", Rationale: "different ports", SupportRevisionIDs: []string{"rev-a", "rev-b"}}
	h.llm.responses = append(h.llm.responses, propose(finding))
	require.NoError(t, h.service.Process(context.Background(), dreamJob, dreamLease))
	sender := &fakeSender{}
	notify := service.NewNotificationService(h.tx, repo, h.jobs, sender, "thread")
	require.Len(t, h.enqueued.jobs, 1)
	require.NoError(t, notify.Dispatch(context.Background(), h.enqueued.jobs[0], dreamLease))
	require.Len(t, sender.sent, 1)
	// A new input appears, but the model reaches the same conclusion.
	repo.add(service.DreamInput{Revision: "rev-c", ClaimID: "claim-c", Text: "Unrelated note.", Scope: domain.Scope{ProjectID: "project", TaskID: "task"}, Status: domain.ClaimCandidate, SubmittedBy: "agent"})
	h.llm.responses = []func(domain.GenerationRequest) (domain.Generation, error){propose(domain.Proposal{Kind: domain.ProposalContradiction, Text: "  ports DISAGREE. ", Rationale: "same", SupportRevisionIDs: []string{"rev-b", "rev-a"}})}
	sender.sent = nil
	require.NoError(t, h.service.Process(context.Background(), dreamJob, dreamLease))

	for _, job := range h.enqueued.jobs {
		require.NoError(t, notify.Dispatch(context.Background(), job, dreamLease))
	}

	sentNotices := sender.sent
	assert.Empty(t, sentNotices) // unchanged finding
	assert.Len(t, h.candidates.written, 1)
}

func TestSensitiveFindingDoesNotReachBuzz(t *testing.T) {
	for _, tc := range []struct {
		name     string
		proposal domain.Proposal
	}{
		{"model marks sensitive", domain.Proposal{Kind: domain.ProposalContradiction, Text: "Ports disagree in a private deployment.", Rationale: "private", SupportRevisionIDs: []string{"rev-a", "rev-b"}, Sensitive: true}},
		{"credential-like text", domain.Proposal{Kind: domain.ProposalContradiction, Text: "Staging uses api_key sk-abcdefghijklmnop for the health check.", Rationale: "rev-b", SupportRevisionIDs: []string{"rev-a", "rev-b"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeDreamRepo("primary")
			observedInputs(repo)
			h := newDreamHarness(t, []domain.ModelRoute{pricedRoute}, 1_000_000, repo)
			h.llm.responses = append(h.llm.responses, propose(tc.proposal))
			require.NoError(t, h.service.Process(context.Background(), dreamJob, dreamLease))
			sender := &fakeSender{}
			notify := service.NewNotificationService(h.tx, repo, h.jobs, sender, "thread")

			for _, job := range h.enqueued.jobs {
				require.NoError(t, notify.Dispatch(context.Background(), job, dreamLease))
			}

			assert.Empty(t, sender.sent)
			require.Len(t, repo.findings, 1)
			for _, f := range repo.findings {
				assert.Equal(t, domain.FindingWithheld, f.Status)
				assert.True(t, f.Sensitive)
			}
		})
	}
}

// Defense in depth: a sensitive finding that somehow reaches the outbox, or
// one whose support changed, is rechecked and never sent.
func TestDispatchRechecksRelevanceBeforeSending(t *testing.T) {
	repo := newFakeDreamRepo("primary")
	observedInputs(repo)
	repo.findings["f-sensitive"] = domain.Finding{ID: "f-sensitive", Digest: "d1", Text: "secret", Sensitive: true, Status: domain.FindingPending, SourceRevisions: []string{"rev-a"}}
	repo.findings["f-stale"] = domain.Finding{ID: "f-stale", Digest: "d2", Text: "stale", Status: domain.FindingPending, SourceRevisions: []string{"rev-b"}}
	repo.findings["f-live"] = domain.Finding{ID: "f-live", Digest: "d3", Kind: "contradiction", Text: "live finding", Status: domain.FindingPending, SourceRevisions: []string{"rev-a"}}
	repo.current["rev-b"] = false
	repo.notices["notice-1"] = domain.Notice{ID: "notice-1", Urgency: domain.FindingImmediate, ProjectID: "project", FindingIDs: []string{"f-sensitive", "f-stale", "f-live"}, State: domain.NoticePending}
	tx := &lockTx{}
	sender := &fakeSender{}
	notify := service.NewNotificationService(tx, repo, &fakeDreamJobs{tx: tx}, sender, "thread")

	err := notify.Dispatch(context.Background(), notifyJob("notice-1"), dreamLease)

	require.NoError(t, err)
	require.Len(t, sender.sent, 1)
	assert.Contains(t, sender.sent[0], "live finding")
	assert.NotContains(t, sender.sent[0], "secret")
	assert.Equal(t, domain.FindingNotified, repo.findings["f-live"].Status)
	assert.Equal(t, domain.FindingSuppressed, repo.findings["f-stale"].Status)
	assert.Equal(t, domain.NoticeSent, repo.notices["notice-1"].State)
}

func TestDispatchFailureKeepsNoticePendingForIdempotentRetry(t *testing.T) {
	repo := newFakeDreamRepo()
	observedInputs(repo)
	repo.findings["f"] = domain.Finding{ID: "f", Digest: "d", Kind: "connection", Text: "finding", Status: domain.FindingPending, SourceRevisions: []string{"rev-a"}}
	repo.notices["n"] = domain.Notice{ID: "n", ProjectID: "project", FindingIDs: []string{"f"}, State: domain.NoticePending}
	tx := &lockTx{}
	sender := &fakeSender{err: domain.ErrUnavailable}
	notify := service.NewNotificationService(tx, repo, &fakeDreamJobs{tx: tx}, sender, "thread")
	require.ErrorIs(t, notify.Dispatch(context.Background(), notifyJob("n"), dreamLease), domain.ErrUnavailable)
	sender.err = nil

	err := notify.Dispatch(context.Background(), notifyJob("n"), dreamLease)

	require.NoError(t, err)
	require.Len(t, sender.sent, 1)
	assert.True(t, strings.HasPrefix(sender.sent[0], "n|"), "operation key is the stable notice ID")
	require.NoError(t, notify.Dispatch(context.Background(), notifyJob("n"), dreamLease))
	assert.Len(t, sender.sent, 1, "a sent notice is never resent")
}

func TestDreamBoundsAttemptsAndContext(t *testing.T) {
	repo := newFakeDreamRepo("primary")
	for i := range 40 {
		id := "rev-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		repo.add(service.DreamInput{Revision: id, ClaimID: "c" + id, Text: strings.Repeat("x", 1000), Scope: domain.Scope{ProjectID: "project", TaskID: "task"}, Status: domain.ClaimCandidate, SubmittedBy: "agent"})
	}
	h := newDreamHarness(t, []domain.ModelRoute{pricedRoute}, 1_000_000, repo)
	malformed := func(domain.GenerationRequest) (domain.Generation, error) {
		return domain.Generation{Usage: knownUsage(5)}, domain.ErrIncomplete
	}
	h.llm.responses = append(h.llm.responses, malformed)

	err := h.service.Process(context.Background(), dreamJob, dreamLease)

	require.Error(t, err)
	assert.Len(t, h.llm.requests, domain.MaxGenerationAttempts)
	for _, r := range h.llm.requests {
		assert.LessOrEqual(t, r.InputBytes(), int64(domain.MaxExcerptBytes))
		assert.False(t, r.Deadline.IsZero())
		assert.NotEmpty(t, r.IdempotencyKey)
	}
	assert.Empty(t, h.candidates.written)
	for _, r := range h.budgetRepo.reservations {
		assert.Equal(t, domain.ReservationSettled, r.State)
	}
}

func TestUnchangedInputsDoNoWork(t *testing.T) {
	repo := newFakeDreamRepo("primary")
	observedInputs(repo)
	h := newDreamHarness(t, []domain.ModelRoute{pricedRoute}, 1_000_000, repo)
	h.llm.responses = append(h.llm.responses, propose())
	require.NoError(t, h.service.Process(context.Background(), dreamJob, dreamLease))
	require.Len(t, h.llm.requests, 1)

	err := h.service.Process(context.Background(), dreamJob, dreamLease)

	require.NoError(t, err)
	assert.Len(t, h.llm.requests, 1)
	assert.Equal(t, 2, h.jobs.committed)
}

func TestScheduleEnqueuesChangedProjectsOnly(t *testing.T) {
	repo := newFakeDreamRepo("primary")
	observedInputs(repo)
	repo.changed = []string{"project"}
	h := newDreamHarness(t, []domain.ModelRoute{pricedRoute}, 1_000_000, repo)

	enqueued, err := h.service.Schedule(context.Background(), dreamNow.Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, enqueued)
	repo.cursor["project"] = h.enqueued.jobs[0].InputDigest

	enqueued, err = h.service.Schedule(context.Background(), dreamNow.Add(-time.Hour))

	require.NoError(t, err)
	assert.Zero(t, enqueued)
	assert.Equal(t, service.DreamJobKind, h.enqueued.jobs[0].Kind)
}

func TestReservationCoversGatewayInputBound(t *testing.T) {
	for _, tc := range []struct {
		name     string
		bound    int64
		cap      int64
		reserved bool
	}{
		// pricedRoute: 1 micro-USD per input byte-token, 5 per output token.
		{"reservation uses the serialized bound", 40_000, 1_000_000, true},
		{"bound beyond the remaining cap defers without a call", 1_000_000, 1_000_000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeDreamRepo("primary")
			observedInputs(repo)
			h := newDreamHarness(t, []domain.ModelRoute{pricedRoute}, tc.cap, repo)
			h.llm.bound = tc.bound
			h.llm.responses = append(h.llm.responses, propose())

			err := h.service.Process(context.Background(), dreamJob, dreamLease)

			require.NoError(t, err)
			status, err := h.budget.Status(context.Background())
			require.NoError(t, err)
			assert.LessOrEqual(t, status.CommittedMicroUSD, tc.cap)
			if !tc.reserved {
				assert.Equal(t, []string{"model_budget_exhausted"}, h.jobs.deferred)
				assert.Empty(t, h.llm.requests)
				assert.Empty(t, h.budgetRepo.reservations)
				return
			}
			require.Len(t, h.budgetRepo.reservations, 1)
			for _, r := range h.budgetRepo.reservations {
				assert.Equal(t, pricedRoute.WorstCaseMicroUSD(tc.bound), r.MaxCostMicroUSD)
			}
		})
	}
}

func TestModelBudgetSettlementAndReconciliation(t *testing.T) {
	repo := newFakeBudgetRepo()
	budget := service.NewModelBudgetService(&lockTx{}, repo, 1000, func() time.Time { return dreamNow })
	known, err := budget.Reserve(context.Background(), domain.Scope{ProjectID: "p"}, "primary", 400)
	require.NoError(t, err)
	unknown, err := budget.Reserve(context.Background(), domain.Scope{ProjectID: "p"}, "primary", 400)
	require.NoError(t, err)
	require.NoError(t, budget.Settle(context.Background(), known.ID, knownUsage(50)))
	require.NoError(t, budget.Settle(context.Background(), unknown.ID, domain.Usage{}))
	assert.ErrorIs(t, budget.Settle(context.Background(), known.ID, knownUsage(1)), domain.ErrConflict)
	assert.ErrorIs(t, budget.Reconcile(context.Background(), unknown.ID, domain.Usage{}), domain.ErrInvalidInput)

	err = budget.Reconcile(context.Background(), unknown.ID, knownUsage(70))

	require.NoError(t, err)
	status, err := budget.Status(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(120), status.CommittedMicroUSD)
	assert.Zero(t, status.UnreconciledReservations)
	_, err = budget.Reserve(context.Background(), domain.Scope{ProjectID: "p"}, "primary", 0)
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
}

package service_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNonUrgentFindingsShareOneDailyDigest(t *testing.T) {
	repo := newFakeDreamRepo("primary")
	observedInputs(repo)
	h := newDreamHarness(t, []domain.ModelRoute{pricedRoute}, 1_000_000, repo)
	long := strings.Repeat("é", 400)
	h.llm.responses = append(h.llm.responses, propose(
		domain.Proposal{Kind: domain.ProposalConnection, Text: "Both claims describe staging networking.", Rationale: "shared topic", SupportRevisionIDs: []string{"rev-a", "rev-b"}},
		domain.Proposal{Kind: domain.ProposalStale, Text: long, Rationale: "old", SupportRevisionIDs: []string{"rev-b"}},
	))

	err := h.service.Process(context.Background(), dreamJob, dreamLease)

	require.NoError(t, err)
	require.Len(t, repo.notices, 1)
	for _, n := range repo.notices {
		assert.Equal(t, domain.FindingDaily, n.Urgency)
		assert.Len(t, n.FindingIDs, 2)
		// Digest hour 9 UTC has passed at 15:00, so the digest is due tomorrow.
		assert.Equal(t, time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC), n.DueAt)
	}
	sender := &fakeSender{}
	notify := service.NewNotificationService(h.tx, repo, h.jobs, sender, "thread")
	require.NoError(t, notify.Dispatch(context.Background(), h.enqueued.jobs[0], dreamLease))
	require.Len(t, sender.sent, 1)
	assert.Contains(t, sender.sent[0], "daily findings digest")
	assert.Contains(t, sender.sent[0], "...")
}

func TestDispatchWithoutSenderLeavesNoticePending(t *testing.T) {
	repo := newFakeDreamRepo()
	observedInputs(repo)
	repo.findings["f"] = domain.Finding{ID: "f", Digest: "d", Kind: "connection", Text: "finding", Status: domain.FindingPending, SourceRevisions: []string{"rev-a"}}
	repo.notices["n"] = domain.Notice{ID: "n", ProjectID: "project", FindingIDs: []string{"f"}, State: domain.NoticePending}
	tx := &lockTx{}
	notify := service.NewNotificationService(tx, repo, &fakeDreamJobs{tx: tx}, nil, "")

	err := notify.Dispatch(context.Background(), notifyJob("n"), dreamLease)

	assert.ErrorIs(t, err, domain.ErrUnavailable)
	assert.Equal(t, domain.NoticePending, repo.notices["n"].State)
	assert.ErrorIs(t, notify.Dispatch(context.Background(), domain.Job{Kind: "other"}, dreamLease), domain.ErrInvalidInput)
}

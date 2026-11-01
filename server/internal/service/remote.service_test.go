package service

import (
	"context"
	"strings"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type remoteAPI struct {
	status   domain.Status
	calls    int
	result   domain.RetrievalResult
	claim    domain.ClaimRevision
	err      error
	retrieve domain.RetrievalRequest
}

func (a *remoteAPI) Retrieve(_ context.Context, r domain.RetrievalRequest) (domain.RetrievalResult, error) {
	a.calls++
	a.retrieve = r
	return a.result, a.err
}
func (a *remoteAPI) Fetch(context.Context, domain.FetchRequest) (domain.RetrievalResult, error) {
	a.calls++
	return a.result, a.err
}
func (a *remoteAPI) Submit(context.Context, domain.CandidateInput) (domain.ClaimRevision, error) {
	a.calls++
	return a.claim, a.err
}
func (a *remoteAPI) PutCheckpoint(_ context.Context, _ string, c domain.Checkpoint) (domain.Checkpoint, error) {
	a.calls++
	return c, a.err
}
func (a *remoteAPI) Status(context.Context) (domain.Status, error) {
	a.calls++
	if a.status.ConnectionState == "" {
		return domain.Status{ConnectionState: "available"}, a.err
	}
	return a.status, a.err
}

func TestRemoteMemoryValidatesBeforeAuthenticatedCalls(t *testing.T) {
	for _, tc := range []struct {
		name string
		act  func(*RemoteMemoryService) error
	}{
		{"retrieval without project", func(s *RemoteMemoryService) error {
			_, e := s.Retrieve(t.Context(), domain.RetrievalRequest{Mode: domain.RetrievalEvidence})
			return e
		}},
		{"working retrieval without task", func(s *RemoteMemoryService) error {
			_, e := s.Retrieve(t.Context(), domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "demo"}, Mode: domain.RetrievalWorking})
			return e
		}},
		{"orientation above 4 KiB", func(s *RemoteMemoryService) error {
			_, e := s.Retrieve(t.Context(), domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "demo"}, Mode: domain.RetrievalOrientation, BudgetBytes: 4097})
			return e
		}},
		{"publication fetch with task scope", func(s *RemoteMemoryService) error {
			_, e := s.Fetch(t.Context(), domain.FetchRequest{ArtifactID: "a", Locator: "l", BudgetBytes: 100, Scope: domain.Scope{ProjectID: "demo", TaskID: "t"}})
			return e
		}},
		{"working fetch without task", func(s *RemoteMemoryService) error {
			_, e := s.Fetch(t.Context(), domain.FetchRequest{ArtifactID: "a", Locator: "l", BudgetBytes: 100, Mode: domain.FetchWorking, Scope: domain.Scope{ProjectID: "demo"}})
			return e
		}},
		{"project-wide candidate", func(s *RemoteMemoryService) error {
			_, e := s.Submit(t.Context(), domain.CandidateInput{ClaimID: "c", Text: "x", Kind: "finding", Scope: domain.Scope{ProjectID: "demo"}})
			return e
		}},
		{"checkpoint without task", func(s *RemoteMemoryService) error {
			_, e := s.PutCheckpoint(t.Context(), "", domain.Checkpoint{ID: "cp", Scope: domain.Scope{ProjectID: "demo"}})
			return e
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &remoteAPI{}

			err := tc.act(NewRemoteMemoryService(api))

			assert.ErrorIs(t, err, domain.ErrInvalidInput)
			assert.Zero(t, api.calls)
		})
	}
}

func TestRemoteMemoryRejectsUnsafeResponses(t *testing.T) {
	api := &remoteAPI{result: domain.RetrievalResult{SchemaVersion: "2"}, claim: domain.ClaimRevision{ClaimID: "c", Status: domain.ClaimVerified}}
	s := NewRemoteMemoryService(api)

	_, retrieveErr := s.Retrieve(t.Context(), domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "demo"}, Mode: domain.RetrievalEvidence})
	_, submitErr := s.Submit(t.Context(), domain.CandidateInput{ClaimID: "c", Text: "x", Kind: "finding", Scope: domain.Scope{ProjectID: "demo", TaskID: "t"}})

	assert.ErrorIs(t, retrieveErr, domain.ErrUnavailable, "only schema 3 remote envelopes are accepted")
	assert.ErrorIs(t, submitErr, domain.ErrUnknownOutcome, "a submission is never presented as verified, and the accepted write may have committed")
}

func TestRemoteMemoryAppliesDocumentedDefaultsAndPropagatesFailures(t *testing.T) {
	api := &remoteAPI{result: domain.RetrievalResult{SchemaVersion: "3"}}
	s := NewRemoteMemoryService(api)

	got, err := s.Retrieve(t.Context(), domain.RetrievalRequest{Scope: domain.Scope{ProjectID: "demo"}, Mode: domain.RetrievalEvidence, MaxDepth: 2})
	require.NoError(t, err)
	assert.Equal(t, "3", got.SchemaVersion)
	assert.Equal(t, domain.EvidenceBudget, api.retrieve.BudgetBytes)
	assert.Equal(t, domain.MaxGraphVisited, api.retrieve.MaxVisited)

	api.err = domain.ErrUnauthorized
	_, err = s.Status(t.Context())
	assert.ErrorIs(t, err, domain.ErrUnauthorized, "failures are reported, never substituted")
}

func TestRemoteMemoryRevisionBoundComesFromServerStatus(t *testing.T) {
	for _, tc := range []struct {
		name      string
		advertise int
		err       error
		want      int
	}{
		{"advertised", 64, nil, 64},
		{"smaller advertised bound", 26, nil, 26},
		{"older server advertises nothing", 0, nil, 0},
		{"bound beyond client contract", domain.MaxRevisionBytes + 1, nil, 0},
		{"status unavailable", 64, domain.ErrUnavailable, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &remoteAPI{status: domain.Status{ConnectionState: "available", MaxRevisionBytes: tc.advertise}, err: tc.err}
			s := NewRemoteMemoryService(api)

			got := s.MaxRevisionBytes(t.Context())

			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRemoteMemoryCachesOnlyAnAdvertisedBound(t *testing.T) {
	api := &remoteAPI{status: domain.Status{ConnectionState: "available", MaxRevisionBytes: 64}, err: domain.ErrUnavailable}
	s := NewRemoteMemoryService(api)
	require.Zero(t, s.MaxRevisionBytes(t.Context()), "an unreachable server is not cached as unbounded")
	api.err = nil
	require.Equal(t, 64, s.MaxRevisionBytes(t.Context()))
	calls := api.calls

	got := s.MaxRevisionBytes(t.Context())

	assert.Equal(t, 64, got)
	assert.Equal(t, calls, api.calls, "a confirmed bound is not re-fetched before every write")
}

func TestIssuedRevisionsFitAdvertisedBound(t *testing.T) {
	for range 100 {
		revision := newRevision()

		assert.NotEmpty(t, revision)
		assert.LessOrEqual(t, len(revision), domain.MaxRevisionBytes)
		assert.NoError(t, domain.ValidateIDs(revision))
	}
}

func TestRemoteMemoryReceiptsBeyondAdvertisedBoundAreUnknownOutcomes(t *testing.T) {
	api := &remoteAPI{status: domain.Status{ConnectionState: "available", MaxRevisionBytes: 26}, claim: domain.ClaimRevision{ClaimID: "c", Revision: strings.Repeat("r", 40), Status: domain.ClaimCandidate}}
	s := NewRemoteMemoryService(api)
	require.Equal(t, 26, s.MaxRevisionBytes(t.Context()))
	input := domain.CandidateInput{ClaimID: "c", Text: "x", Kind: "finding", Scope: domain.Scope{ProjectID: "demo", TaskID: "task"}}

	_, submitErr := s.Submit(t.Context(), input)
	_, checkpointErr := s.PutCheckpoint(t.Context(), "", domain.Checkpoint{ID: "cp", Revision: strings.Repeat("r", 40), Scope: input.Scope})

	assert.ErrorIs(t, submitErr, domain.ErrUnknownOutcome)
	assert.ErrorIs(t, checkpointErr, domain.ErrUnknownOutcome)
}

func TestRemoteMemoryCommittedNonCandidateIsUnknownOutcome(t *testing.T) {
	api := &remoteAPI{claim: domain.ClaimRevision{ClaimID: "c", Revision: "r", Status: domain.ClaimVerified}}
	s := NewRemoteMemoryService(api)

	_, err := s.Submit(t.Context(), domain.CandidateInput{ClaimID: "c", Text: "x", Kind: "finding", Scope: domain.Scope{ProjectID: "demo", TaskID: "task"}})

	assert.ErrorIs(t, err, domain.ErrUnknownOutcome)
}

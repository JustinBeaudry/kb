package service

import (
	"context"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

type candidateFixture struct {
	c   domain.ClaimRevision
	err error
}

func (f *candidateFixture) Head(context.Context, Tx, string) (domain.ClaimRevision, string, error) {
	return f.c, f.c.SubmittedBy, f.err
}
func (f *candidateFixture) PutCandidate(_ context.Context, _ Tx, c domain.ClaimRevision) error {
	f.c = c
	return f.err
}

type mutateReference struct {
	calls *int
	ref   EvidenceReferencer
	after func()
}

func (m mutateReference) Reference(ctx context.Context, p domain.Principal, r domain.EvidenceRef, scope domain.Scope) (domain.EvidenceRef, error) {
	if m.calls != nil {
		(*m.calls)++
	}
	e, err := m.ref.Reference(ctx, p, r, scope)
	if m.after != nil {
		m.after()
	}
	return e, err
}
func TestMemoryCandidatesRemainUntrustedAndRevalidate(t *testing.T) {
	for _, tc := range []struct {
		name string
		want error
	}{{"authentic", nil}, {"invalid input", domain.ErrInvalidInput}, {"no task", domain.ErrInvalidInput}, {"invalid entity", domain.ErrInvalidInput}, {"missing capability", domain.ErrForbidden}, {"bad digest", domain.ErrInvalidInput}, {"missing source", domain.ErrForbidden}, {"other task owner", domain.ErrForbidden}, {"head conflict", domain.ErrConflict}, {"head owner", domain.ErrForbidden}, {"source unavailable after IO", domain.ErrIncomplete}, {"source scope after IO", domain.ErrForbidden}, {"source digest after IO", domain.ErrConflict}, {"source policy after IO", domain.ErrStale}, {"source excluded after IO", domain.ErrForbidden}, {"missing dependency", domain.ErrIncomplete}, {"candidate dependency", domain.ErrIncomplete}, {"verified dependency", nil}, {"repository error", domain.ErrUnavailable}} {
		t.Run(tc.name, func(t *testing.T) {
			capture, evidence, _, p, m := captureFixture(t)
			p.Capabilities = append(p.Capabilities, "candidate:write", "retrieval:read")
			p.Role = domain.RoleAgent
			a := readyCapture(t, capture, p, m)
			auth := &retrievalFixture{snapshot: RetrievalSnapshot{Principal: p}, eligible: map[string]bool{}}
			repo := &candidateFixture{}
			input := domain.CandidateInput{ClaimID: "candidate", Text: "finding", Kind: "finding", Scope: m.Scope, Evidence: []domain.EvidenceRef{{ArtifactID: a.ID, Digest: a.Digest, Locator: a.SourceLocator, SourceRevision: a.Generation, Origin: domain.Origin{ClientID: "owner", Trust: domain.TrustOwnerAuthenticated}}}}
			referenceCalls := 0
			reference := mutateReference{ref: capture, calls: &referenceCalls}
			switch tc.name {
			case "invalid input":
				input.Text = ""
			case "no task":
				input.Scope.TaskID = ""
			case "invalid entity":
				input.EntityIDs = []string{" "}
			case "missing capability":
				p.Capabilities = nil
			case "bad digest":
				input.Evidence[0].Digest = "bad"
			case "missing source":
				input.Evidence[0].ArtifactID = "missing"
			case "other task owner":
				auth.snapshot.TaskOwner = "other"
			case "head conflict":
				input.ExpectedRevision = "stale"
			case "head owner":
				repo.c = domain.ClaimRevision{Revision: "existing", Scope: m.Scope, SubmittedBy: "other"}
			case "source unavailable after IO":
				reference.after = func() { c := evidence.artifacts[a.ID]; c.ObjectAvailable = false; evidence.artifacts[a.ID] = c }
			case "source scope after IO":
				reference.after = func() { c := evidence.artifacts[a.ID]; c.Scope.TaskID = "other"; evidence.artifacts[a.ID] = c }
			case "source digest after IO":
				reference.after = func() { c := evidence.artifacts[a.ID]; c.Digest = TokenDigest("other"); evidence.artifacts[a.ID] = c }
			case "source policy after IO":
				reference.after = func() { evidence.policy.Paused = true }
			case "source excluded after IO":
				reference.after = func() {
					evidence.policy.ExcludedPaths = []string{"private"}
					c := evidence.artifacts[a.ID]
					c.SourcePath = "private/file"
					evidence.artifacts[a.ID] = c
				}
			case "missing dependency":
				input.DependsOn = []string{"missing"}
			case "candidate dependency", "verified dependency":
				input.DependsOn = []string{"dep"}
				status := domain.ClaimCandidate
				if tc.name == "verified dependency" {
					status = domain.ClaimVerified
				}
				auth.claims = []domain.ClaimRevision{{Revision: "dep", Status: status}}
				auth.eligible["dep"] = true
			case "repository error":
				repo.err = domain.ErrUnavailable
			}
			memory := NewMemoryService(directTx{}, repo, auth, evidence, reference)
			got, err := memory.Submit(t.Context(), p, input)
			if tc.name == "other task owner" {
				assert.Zero(t, referenceCalls)
			}
			if tc.want != nil {
				assert.ErrorIs(t, err, tc.want)
				assert.Empty(t, got.Revision)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, domain.ClaimCandidate, got.Status)
			assert.Equal(t, domain.TrustSubmitted, got.Evidence[0].Origin.Trust)
			assert.Equal(t, "client", got.Evidence[0].Origin.ClientID)
			assert.NotEmpty(t, got.Revision)
			assert.Equal(t, "owner", input.Evidence[0].Origin.ClientID)
		})
	}
}
func TestCheckpointAuthorizedReads(t *testing.T) {
	p := domain.Principal{ClientID: "client", ProjectIDs: []string{"demo"}, Capabilities: []string{"retrieval:read"}}
	r := &checkpointRepo{c: domain.Checkpoint{ID: "cp", Revision: "r", Scope: domain.Scope{ProjectID: "demo", TaskID: "task"}}, client: p.ClientID}
	auth := &retrievalFixture{snapshot: RetrievalSnapshot{Principal: p, TaskOwner: p.ClientID}}
	s := NewCheckpointService(directTx{}, r, auth)
	for _, id := range []string{"", "cp"} {
		got, e := s.Get(t.Context(), p, id)
		if id == "" {
			assert.ErrorIs(t, e, domain.ErrInvalidInput)
		} else {
			require.NoError(t, e)
			assert.Equal(t, r.c.Revision, got.Revision)
		}
	}
	_, e := s.Latest(t.Context(), p, domain.Scope{ProjectID: "demo"})
	assert.ErrorIs(t, e, domain.ErrInvalidInput)
	got, e := s.Latest(t.Context(), p, r.c.Scope)
	require.NoError(t, e)
	assert.Equal(t, "cp", got.ID)
	p.Capabilities = nil
	_, e = s.Get(t.Context(), p, "cp")
	assert.ErrorIs(t, e, domain.ErrForbidden)
}

func TestMemoryEvidenceFreeHypothesisIsTaskLocal(t *testing.T) {
	_, evidence, _, p, m := captureFixture(t)
	p.Capabilities = append(p.Capabilities, "candidate:write", "retrieval:read")
	auth := &retrievalFixture{snapshot: RetrievalSnapshot{Principal: p}, eligible: map[string]bool{}}
	repo := &candidateFixture{}
	memory := NewMemoryService(directTx{}, repo, auth, evidence, nil)
	c, e := memory.Submit(t.Context(), p, domain.CandidateInput{ClaimID: "hypothesis", Text: "A task hypothesis without evidence", Kind: "hypothesis", Scope: m.Scope})
	require.NoError(t, e)
	assert.Equal(t, domain.ClaimCandidate, c.Status)
	assert.Empty(t, c.Evidence)
	auth.claims = []domain.ClaimRevision{c}
	auth.eligible[c.Revision] = true
	retrieval, e := NewRetrievalService(directTx{}, auth, auth, evidence, nil, []byte("12345678901234567890123456789012"))
	require.NoError(t, e)
	working, e := retrieval.Retrieve(t.Context(), p, domain.RetrievalRequest{Scope: m.Scope, Mode: domain.RetrievalWorking})
	require.NoError(t, e)
	require.Len(t, working.Chunks, 1)
	assert.Equal(t, domain.TrustSubmitted, working.Chunks[0].OriginTrust)
	assert.Nil(t, working.Chunks[0].VerificationOutcome)
	ordinary, e := retrieval.Retrieve(t.Context(), p, domain.RetrievalRequest{Scope: domain.Scope{ProjectID: m.Scope.ProjectID}, Mode: domain.RetrievalEvidence})
	require.NoError(t, e)
	assert.Empty(t, ordinary.Chunks)
}

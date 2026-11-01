package service

import (
	"context"
	"fmt"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"slices"
	"testing"
	"time"
)

func TestOwnerTargetFieldsAreExplicitAndDigestBound(t *testing.T) {
	draft := AdjudicationDraft{ProjectID: "project", CandidateRevision: "revision", ExpectedCandidateHead: "revision", Action: "verify", IdempotencyKey: "key"}
	assert.ErrorIs(t, validateAdjudication(draft), domain.ErrInvalidInput)
	draft.TargetID = "target"
	draft.TargetText = "approved"
	draft.TargetKind = "finding"
	draft.Excerpts = []ExcerptSelection{{ArtifactID: "source", SourceRevision: "r", SourceDigest: TokenDigest("bytes"), ExpectedExcerptDigest: TokenDigest("bytes"), Start: 0, End: 5}}
	require.NoError(t, validateAdjudication(draft))
	a := captureID(draft)
	draft.TargetText = "changed"
	assert.NotEqual(t, a, captureID(draft))
	draft.TargetApplicability.Environment = "new"
	assert.NotEqual(t, a, captureID(draft))
}
func TestRejectOnlyCandidateAndDoesNotPublish(t *testing.T) {
	draft := AdjudicationDraft{ProjectID: "project", CandidateRevision: "revision", ExpectedCandidateHead: "revision", Action: "reject", IdempotencyKey: "key"}
	require.NoError(t, validateAdjudication(draft))
	draft.TargetText = "must not publish"
	assert.ErrorIs(t, validateAdjudication(draft), domain.ErrInvalidInput)
}
func TestPublicationClosureRejectsMissingDuplicateCyclicOrOversizedSupport(t *testing.T) {
	source := domain.ClaimRevision{Revision: "source", DependsOn: []string{"dep", "dep"}}
	assert.ErrorIs(t, validateDependencies(source), domain.ErrInvalidInput)
	source.DependsOn = []string{"source"}
	assert.ErrorIs(t, validateDependencies(source), domain.ErrInvalidInput)
	source.DependsOn = make([]string, 1001)
	assert.ErrorIs(t, validateDependencies(source), domain.ErrInvalidInput)
}

func TestInvalidationPreviewRequiresValidSupportAndBindsExactState(t *testing.T) {
	s, f, _ := verificationFixture(t)
	_, err := s.PreviewInvalidationHost(t.Context(), "candidate")
	assert.ErrorIs(t, err, domain.ErrConflict)
	state := f.states["candidate"]
	state.Status = domain.ClaimVerified
	state.Valid = true
	f.states["candidate"] = state
	input, err := s.PreviewInvalidationHost(t.Context(), "candidate")
	require.NoError(t, err)
	assert.Equal(t, "candidate", input.ExpectedHead)
	assert.Equal(t, "verified", input.ExpectedStatus)
	assert.NotEmpty(t, input.ExpectedDigest)
	input.IdempotencyKey = "invalidate"
	_, err = s.InvalidateHost(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, domain.ClaimInvalidated, f.states["candidate"].Status)
	replay, err := s.InvalidateHost(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, "invalidated", replay.State)
}

type verificationRepoFixture struct {
	states       map[string]domain.RevisionState
	heads        map[string]string
	decisions    map[string]domain.OwnerDecision
	publications []domain.Publication
	lineage      []domain.ExcerptLineage
	epoch        int
}

func (f *verificationRepoFixture) LoadRevision(_ context.Context, _ Tx, id string) (domain.RevisionState, error) {
	v, ok := f.states[id]
	if !ok {
		return v, domain.ErrIncomplete
	}
	return v, nil
}
func (f *verificationRepoFixture) Head(_ context.Context, _ Tx, id string) (string, error) {
	return f.heads[id], nil
}
func (f *verificationRepoFixture) LoadDecisionByKey(_ context.Context, _ Tx, owner, key string) (domain.OwnerDecision, error) {
	v, ok := f.decisions[owner+key]
	if !ok {
		return v, domain.ErrIncomplete
	}
	return v, nil
}
func (f *verificationRepoFixture) PutDecision(_ context.Context, _ Tx, v domain.OwnerDecision) error {
	f.decisions[v.OwnerID+v.Key] = v
	return nil
}
func (f *verificationRepoFixture) PutVerification(_ context.Context, _ Tx, v domain.Verification, status domain.ClaimStatus, valid bool) error {
	c := f.states[v.ClaimRevision]
	c.Status = status
	c.Valid = valid
	f.states[v.ClaimRevision] = c
	return nil
}
func (f *verificationRepoFixture) AdvanceRevisionEpoch(context.Context, Tx) error {
	f.epoch++
	return nil
}
func (f *verificationRepoFixture) PutTarget(_ context.Context, _ Tx, c domain.ClaimRevision, expected string) error {
	if f.heads[c.ClaimID] != expected {
		return domain.ErrConflict
	}
	f.states[c.Revision] = domain.RevisionState{Claim: c, Status: c.Status, Valid: true}
	f.heads[c.ClaimID] = c.Revision
	return nil
}
func (f *verificationRepoFixture) PutPublication(_ context.Context, _ Tx, p domain.Publication) error {
	f.publications = append(f.publications, p)
	return nil
}
func (f *verificationRepoFixture) PutExcerptLineage(_ context.Context, _ Tx, p domain.ExcerptLineage) error {
	f.lineage = append(f.lineage, p)
	return nil
}
func (f *verificationRepoFixture) InvalidateSupport(_ context.Context, _ Tx, id string) error {
	c := f.states[id]
	c.Valid = false
	c.Status = domain.ClaimInvalidated
	f.states[id] = c
	for _, p := range f.publications {
		if slices.Contains(p.SupportRevisions, id) {
			target := f.states[p.ClaimRevision]
			target.Valid = false
			target.Status = domain.ClaimInvalidated
			f.states[p.ClaimRevision] = target
		}
	}
	return nil
}
func verificationFixture(t *testing.T) (*VerificationService, *verificationRepoFixture, AdjudicationDraft) {
	t.Helper()
	capture, r, o, p, m := captureFixture(t)
	a := readyCapture(t, capture, p, m)
	_, owners := newIdentityFixture()
	owners.owner = OwnerIdentity{ID: "owner", PublicKey: ownerNostrPublicKey}
	host := NewHostCaptureService(directTx{}, r, capture.jobs, o, owners)
	candidate := domain.ClaimRevision{Revision: "candidate", ClaimID: "claim", Scope: m.Scope, Text: "private", Kind: "finding", Status: domain.ClaimCandidate, Evidence: []domain.EvidenceRef{{ArtifactID: a.ID, Digest: a.Digest, Locator: a.SourceLocator, SourceRevision: "source", Origin: domain.Origin{ClientID: p.ClientID, Trust: domain.TrustSubmitted}}}}
	f := &verificationRepoFixture{states: map[string]domain.RevisionState{"candidate": {Claim: candidate, Status: domain.ClaimCandidate}}, heads: map[string]string{"claim": "candidate"}, decisions: map[string]domain.OwnerDecision{}}
	d := AdjudicationDraft{ProjectID: "project", CandidateRevision: "candidate", ExpectedCandidateHead: "candidate", Action: "verify", IdempotencyKey: "decision", TargetID: "target", TargetText: "approved", TargetKind: "finding", Excerpts: []ExcerptSelection{{ArtifactID: a.ID, SourceRevision: "source", SourceDigest: a.Digest, Start: 0, End: 4, ExpectedExcerptDigest: TokenDigest("exit")}}}
	return NewVerificationService(directTx{}, f, f, owners, r, host), f, d
}
func TestOwnerAdjudicationRequiresExactHeadAndDigest(t *testing.T) {
	s, _, d := verificationFixture(t)
	preview, err := s.PreviewHost(t.Context(), d)
	require.NoError(t, err)
	input := AdjudicationInput{AdjudicationDraft: d, ExpectedDigest: TokenDigest("wrong")}
	_, err = s.AdjudicateHost(t.Context(), input)
	assert.ErrorIs(t, err, domain.ErrStale)
	input.ExpectedDigest = preview.Digest
	input.ExpectedCandidateHead = "changed"
	_, err = s.AdjudicateHost(t.Context(), input)
	assert.ErrorIs(t, err, domain.ErrConflict)
	input.ExpectedCandidateHead = d.ExpectedCandidateHead
	input.TargetText = "not confirmed"
	_, err = s.AdjudicateHost(t.Context(), input)
	assert.ErrorIs(t, err, domain.ErrStale)
}
func TestOwnerAdjudicationReplayReturnsOriginalReceipt(t *testing.T) {
	s, f, d := verificationFixture(t)
	preview, err := s.PreviewHost(t.Context(), d)
	require.NoError(t, err)
	input := AdjudicationInput{d, preview.Digest}
	result, err := s.AdjudicateHost(t.Context(), input)
	require.NoError(t, err)
	require.Len(t, f.publications, 1)
	f.heads["claim"] = "new-head"
	again, err := s.AdjudicateHost(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, result, again)
	assert.Len(t, f.publications, 1)
	assert.Equal(t, 1, f.epoch)
	input.TargetText = "changed"
	_, err = s.AdjudicateHost(t.Context(), input)
	assert.ErrorIs(t, err, domain.ErrConflict)
}
func TestRejectedRevisionCannotBeVerified(t *testing.T) {
	s, f, d := verificationFixture(t)
	d.Action = "reject"
	d.TargetID = ""
	d.TargetText = ""
	d.TargetKind = ""
	d.Excerpts = nil
	preview, err := s.PreviewHost(t.Context(), d)
	require.NoError(t, err)
	result, err := s.AdjudicateHost(t.Context(), AdjudicationInput{d, preview.Digest})
	require.NoError(t, err)
	assert.Empty(t, result.TargetRevision)
	assert.Empty(t, f.publications)
	assert.Equal(t, domain.ClaimRejected, f.states["candidate"].Status)
	assert.Equal(t, "candidate", f.heads["claim"])
	d.Action = "verify"
	d.IdempotencyKey = "new"
	d.TargetID = "target"
	d.TargetText = "approved"
	d.TargetKind = "finding"
	d.Excerpts = []ExcerptSelection{{ArtifactID: "a", SourceRevision: "r", SourceDigest: TokenDigest("a"), ExpectedExcerptDigest: TokenDigest("a"), End: 1}}
	_, err = s.PreviewHost(t.Context(), d)
	assert.ErrorIs(t, err, domain.ErrConflict)
}
func TestPublicationRequiresHealthyEvidence(t *testing.T) {
	s, f, d := verificationFixture(t)
	capture := s.capture.(*HostCaptureService).capture
	repo := capture.repo.(*captureRepo)
	a := repo.artifacts[d.Excerpts[0].ArtifactID]
	a.ObjectAvailable = false
	repo.artifacts[a.ID] = a
	_, err := s.PreviewHost(t.Context(), d)
	assert.ErrorIs(t, err, domain.ErrIncomplete)
	assert.Empty(t, f.publications)
}
func TestPublicationRequiresExplicitProjectExcerpts(t *testing.T) {
	s, _, d := verificationFixture(t)
	d.Excerpts = nil
	_, err := s.PreviewHost(t.Context(), d)
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
}
func TestOwnerExcerptKeepsSubmittedTrustAndOriginalLineage(t *testing.T) {
	s, f, d := verificationFixture(t)
	preview, err := s.PreviewHost(t.Context(), d)
	require.NoError(t, err)
	result, err := s.AdjudicateHost(t.Context(), AdjudicationInput{d, preview.Digest})
	require.NoError(t, err)
	require.Len(t, f.lineage, 1)
	l := f.lineage[0]
	assert.Equal(t, "source", l.SourceRevision)
	assert.Equal(t, "candidate", l.SupportRevision)
	assert.Equal(t, domain.TrustSubmitted, l.SourceOrigin.Trust)
	assert.Equal(t, int64(0), l.Start)
	assert.Equal(t, int64(4), l.End)
	target := f.states[result.TargetRevision].Claim
	assert.Empty(t, target.Scope.TaskID)
	assert.Equal(t, domain.TrustSubmitted, target.Evidence[0].Origin.Trust)
	assert.NotContains(t, target.Evidence[0].Locator, d.CandidateRevision)
}
func TestPublicationClosureRejectsMissingAndCyclicSupport(t *testing.T) {
	for _, name := range []string{"missing", "cycle", "duplicate"} {
		t.Run(name, func(t *testing.T) {
			s, f, d := verificationFixture(t)
			c := f.states["candidate"]
			c.Claim.DependsOn = []string{"dep"}
			f.states["candidate"] = c
			if name != "missing" {
				dep := c
				dep.Claim.Revision = "dep"
				dep.Claim.DependsOn = []string{"candidate"}
				dep.Status = domain.ClaimVerified
				dep.Valid = true
				f.states["dep"] = dep
			}
			if name == "duplicate" {
				c.Claim.DependsOn = []string{"dep", "dep"}
				f.states["candidate"] = c
			}
			_, err := s.PreviewHost(t.Context(), d)
			require.Error(t, err)
			assert.Empty(t, f.publications)
		})
	}
}

func TestOwnerAdjudicationRejectsClientAndVerifierAuthority(t *testing.T) {
	for _, role := range []domain.Role{domain.RoleAgent, domain.RoleVerifier, domain.RoleOwner} {
		p := domain.Principal{Role: role, OwnerID: "owner", ProjectIDs: []string{"project"}, Capabilities: []string{"verification:write"}}
		if role != domain.RoleOwner {
			p.ClientID = "client"
		}
		assert.ErrorIs(t, Authorize(p, "owner:adjudicate", []string{"project"}), domain.ErrForbidden)
	}
	s, _, d := verificationFixture(t)
	s.owners = &identityFixture{owner: OwnerIdentity{}}
	_, err := s.PreviewHost(t.Context(), d)
	assert.ErrorIs(t, err, domain.ErrForbidden)
}
func TestOwnerAdjudicationKeyReuseWithDifferentDigestConflicts(t *testing.T) {
	s, _, d := verificationFixture(t)
	pre, err := s.PreviewHost(t.Context(), d)
	require.NoError(t, err)
	_, err = s.AdjudicateHost(t.Context(), AdjudicationInput{d, pre.Digest})
	require.NoError(t, err)
	_, err = s.AdjudicateHost(t.Context(), AdjudicationInput{d, TokenDigest("different")})
	assert.ErrorIs(t, err, domain.ErrConflict)
}
func TestPublicationRejectsApplicabilityBroadening(t *testing.T) {
	s, f, d := verificationFixture(t)
	state := f.states[d.CandidateRevision]
	state.Claim.Applicability = domain.Applicability{Environment: "production", RepositoryRevision: "commit", Versions: map[string]string{"go": "1.24"}}
	f.states[d.CandidateRevision] = state
	_, err := s.PreviewHost(t.Context(), d)
	assert.ErrorIs(t, err, domain.ErrForbidden)
	d.TargetApplicability = state.Claim.Applicability
	_, err = s.PreviewHost(t.Context(), d)
	require.NoError(t, err)
}
func TestOwnerExcerptRejectsIncorrectHashAndUTF8Span(t *testing.T) {
	for _, name := range []string{"hash", "boundary"} {
		t.Run(name, func(t *testing.T) {
			s, f, d := verificationFixture(t)
			if name == "hash" {
				d.Excerpts[0].ExpectedExcerptDigest = TokenDigest("wrong")
			} else {
				capture := s.capture.(*HostCaptureService).capture
				repo := capture.repo.(*captureRepo)
				objects := capture.objects.(*captureObjects)
				data := []byte("éxit_code=0")
				a := repo.artifacts[d.Excerpts[0].ArtifactID]
				a.Digest = TokenDigest(string(data))
				a.Bytes = int64(len(data))
				repo.artifacts[a.ID] = a
				objects.data[a.ObjectKey] = data
				state := f.states[d.CandidateRevision]
				state.Claim.Evidence[0].Digest = a.Digest
				f.states[d.CandidateRevision] = state
				d.Excerpts[0].SourceDigest = a.Digest
				d.Excerpts[0].Start = 1
				d.Excerpts[0].End = 2
				d.Excerpts[0].ExpectedExcerptDigest = TokenDigest(string(data[1:2]))
			}
			pre, err := s.PreviewHost(t.Context(), d)
			require.NoError(t, err)
			_, err = s.AdjudicateHost(t.Context(), AdjudicationInput{d, pre.Digest})
			require.Error(t, err)
			assert.Empty(t, f.publications)
		})
	}
}

func TestPolicyChangeDuringExcerptPreparationAborts(t *testing.T) {
	s, f, d := verificationFixture(t)
	pre, err := s.PreviewHost(t.Context(), d)
	require.NoError(t, err)
	repo := s.evidence.(*captureRepo)
	s.capture = &policyChangingOwnerEvidence{OwnerEvidence: s.capture, change: func() { repo.policy.Revision = "new-policy" }}
	_, err = s.AdjudicateHost(t.Context(), AdjudicationInput{d, pre.Digest})
	assert.ErrorIs(t, err, domain.ErrStale)
	assert.Empty(t, f.publications)
	assert.Empty(t, f.decisions)
}

type policyChangingOwnerEvidence struct {
	OwnerEvidence
	change  func()
	changed bool
}

func (e *policyChangingOwnerEvidence) ReadHost(ctx context.Context, id string) (domain.Artifact, []byte, error) {
	a, data, err := e.OwnerEvidence.ReadHost(ctx, id)
	if err == nil && !e.changed {
		e.changed = true
		e.change()
	}
	return a, data, err
}

func TestOwnerTargetMustNarrowEveryEvidenceApplicabilityAcrossClosure(t *testing.T) {
	future := time.Now().UTC().Add(time.Hour)
	past := time.Now().UTC().Add(-time.Hour)
	cases := []struct {
		name       string
		conditions domain.Applicability
	}{
		{"environment", domain.Applicability{Environment: "production"}},
		{"versions", domain.Applicability{Versions: map[string]string{"go": "1.24"}}},
		{"valid from", domain.Applicability{ValidFrom: &future}},
		{"expired", domain.Applicability{ValidUntil: &past}},
		{"repository", domain.Applicability{RepositoryRevision: "private-commit"}},
		{"dirty", domain.Applicability{DirtyDigest: "private-dirty"}},
	}
	for _, transitive := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("transitive=%t/%s", transitive, tc.name), func(t *testing.T) {
				s, f, d := verificationFixture(t)
				root := f.states[d.CandidateRevision]
				if transitive {
					dep := root
					dep.Claim.Revision = "dependency"
					dep.Claim.ClaimID = "dependency-claim"
					dep.Claim.Evidence = slices.Clone(root.Claim.Evidence)
					dep.Claim.Evidence[0].Applicability = tc.conditions
					dep.Status = domain.ClaimVerified
					dep.Valid = true
					f.states[dep.Claim.Revision] = dep
					root.Claim.DependsOn = []string{dep.Claim.Revision}
				} else {
					root.Claim.Evidence[0].Applicability = tc.conditions
				}
				f.states[d.CandidateRevision] = root
				_, err := s.PreviewHost(t.Context(), d)
				assert.ErrorIs(t, err, domain.ErrForbidden, "unqualified owner target must not erase evidence constraints")
				d.TargetApplicability = tc.conditions
				pre, err := s.PreviewHost(t.Context(), d)
				require.NoError(t, err)
				result, err := s.AdjudicateHost(t.Context(), AdjudicationInput{d, pre.Digest})
				require.NoError(t, err)
				target := f.states[result.TargetRevision].Claim
				require.Len(t, target.Evidence, 1)
				assert.Equal(t, tc.conditions, target.Evidence[0].Applicability, "project refs carry only explicitly approved public conditions")
			})
		}
	}
}

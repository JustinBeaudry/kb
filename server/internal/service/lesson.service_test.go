package service

import (
	"context"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (w *checkWorld) PutLesson(_ context.Context, _ Tx, c domain.ClaimRevision, expected string) error {
	if w.heads[c.ClaimID] != expected {
		return domain.ErrConflict
	}
	w.states[c.Revision] = domain.RevisionState{Claim: c, Status: c.Status, Valid: true}
	w.heads[c.ClaimID] = c.Revision
	return nil
}
func (w *checkWorld) lessons() *LessonService { return NewLessonService(directTx{}, w, w, w) }

// verified seeds a project-scoped verified support with conditions.
func (w *checkWorld) verified(revision string, a domain.Applicability, dependsOn ...string) {
	c := domain.ClaimRevision{ClaimID: "claim-" + revision, Revision: revision, Text: "finding " + revision, Kind: "finding", Scope: domain.Scope{ProjectID: "demo"}, Applicability: a, DependsOn: dependsOn, Status: domain.ClaimVerified, EntityIDs: []string{"billing-service"}, Evidence: []domain.EvidenceRef{{ArtifactID: "artifact-" + revision, Locator: "src/billing/retry.go#L10"}}}
	w.states[revision] = domain.RevisionState{Claim: c, Status: domain.ClaimVerified, Valid: true}
	w.heads[c.ClaimID] = revision
}

var ciGo = domain.Applicability{Environment: "ci", Versions: map[string]string{"go": "1.27"}}

func lessonInput(scope string) domain.LessonInput {
	return domain.LessonInput{ProjectID: "demo", LessonID: "lesson-retry", Text: "Retry flaky network tests at most once in CI.", SharingScope: scope, Applicability: ciGo, Counterexamples: []string{"tests asserting timing"}, SupportRevisions: []string{"s1"}}
}

func TestPublishLessonCreatesSeparateQualifiedLesson(t *testing.T) {
	w := newCheckWorld()
	w.verified("s0", domain.Applicability{Environment: "ci"})
	w.verified("s1", ciGo, "s0")

	publication, err := w.lessons().PublishLesson(t.Context(), verifier, lessonInput(domain.SharingCrossProject))

	require.NoError(t, err)
	assert.Equal(t, domain.PublicationPublished, publication.State)
	assert.Equal(t, domain.SharingCrossProject, publication.SharingScope)
	assert.ElementsMatch(t, []string{"s0", "s1"}, publication.SupportRevisions)
	lesson := w.states[publication.ClaimRevision].Claim
	assert.Equal(t, domain.KindLesson, lesson.Kind)
	assert.Equal(t, "lesson-retry", lesson.ClaimID)
	assert.NotEqual(t, "s1", lesson.Revision)
	assert.Equal(t, domain.Scope{ProjectID: "demo"}, lesson.Scope)
	assert.Equal(t, lesson.Text, publication.GeneralizedText)
	assert.Contains(t, lesson.Text, "Not applicable when: tests asserting timing")
	assert.Empty(t, lesson.Evidence, "restricted source references are not copied")
	assert.Empty(t, lesson.EntityIDs)
	assert.Equal(t, ciGo, lesson.Applicability)
	require.Len(t, w.publications, 1)
	assert.Equal(t, 1, w.epoch)
}

func TestPublishLessonPolicyOutcomes(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*checkWorld, *domain.LessonInput)
		state  string
		reason domain.ReasonCode
	}{
		{"missing applicability", func(_ *checkWorld, in *domain.LessonInput) { in.Applicability = domain.Applicability{} }, domain.PublicationRejected, domain.ReasonApplicabilityMissing},
		{"drops version condition", func(_ *checkWorld, in *domain.LessonInput) {
			in.Applicability = domain.Applicability{Environment: "ci"}
		}, domain.PublicationRejected, domain.ReasonOvergeneralized},
		{"wrong environment", func(_ *checkWorld, in *domain.LessonInput) {
			in.Applicability = domain.Applicability{Environment: "production", Versions: ciGo.Versions}
		}, domain.PublicationRejected, domain.ReasonEnvironmentMismatch},
		{"candidate support", func(w *checkWorld, _ *domain.LessonInput) {
			s := w.states["s1"]
			s.Status, s.Valid = domain.ClaimCandidate, false
			w.states["s1"] = s
		}, domain.PublicationRejected, domain.ReasonSupportNotVerified},
		{"invalidated transitive support", func(w *checkWorld, _ *domain.LessonInput) {
			w.verified("s1", ciGo, "s0")
			w.verified("s0", domain.Applicability{})
			s := w.states["s0"]
			s.Status, s.Valid = domain.ClaimInvalidated, false
			w.states["s0"] = s
		}, domain.PublicationRejected, domain.ReasonSupportNotVerified},
		{"disputed support stays pending", func(w *checkWorld, _ *domain.LessonInput) {
			s := w.states["s1"]
			s.Status = domain.ClaimDisputed
			w.states["s1"] = s
		}, domain.PublicationPending, domain.ReasonDisputedSupport},
		{"source identifier in shared text", func(_ *checkWorld, in *domain.LessonInput) { in.Text = "Retry billing-service tests once." }, domain.PublicationPending, domain.ReasonRestrictedSourceContent},
		{"source path in counterexample", func(_ *checkWorld, in *domain.LessonInput) {
			in.Counterexamples = []string{"see src/billing/retry.go#L10"}
		}, domain.PublicationPending, domain.ReasonRestrictedSourceContent},
		{"repository revision is project private", func(_ *checkWorld, in *domain.LessonInput) {
			in.Applicability.RepositoryRevision = checkHead
		}, domain.PublicationPending, domain.ReasonRestrictedSourceContent},
		{"possible secret", func(_ *checkWorld, in *domain.LessonInput) {
			in.Text = "Use token ghp_abcdefghijklmnopqrstuvwxyz0123456789 for retries."
		}, domain.PublicationPending, domain.ReasonPossibleSecret},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newCheckWorld()
			w.verified("s1", ciGo)
			in := lessonInput(domain.SharingCrossProject)
			tc.mutate(w, &in)

			publication, err := w.lessons().PublishLesson(t.Context(), verifier, in)

			require.NoError(t, err)
			assert.Equal(t, tc.state, publication.State)
			assert.Contains(t, publication.Reasons, tc.reason)
			assert.Empty(t, w.publications)
			assert.Empty(t, w.heads["lesson-retry"])
		})
	}
}

func TestProjectLessonMayNameProjectIdentifiers(t *testing.T) {
	w := newCheckWorld()
	w.verified("s1", ciGo)
	in := lessonInput(domain.SharingProject)
	in.Text = "Retry billing-service tests once."

	publication, err := w.lessons().PublishLesson(t.Context(), verifier, in)

	require.NoError(t, err)
	assert.Equal(t, domain.PublicationPublished, publication.State)
	assert.Empty(t, publication.GeneralizedText)
}

func TestPublishLessonAuthorityAndConcurrency(t *testing.T) {
	agent := verifier
	agent.Role = domain.RoleAgent
	cases := []struct {
		name      string
		principal domain.Principal
		mutate    func(*checkWorld, *domain.LessonInput)
		want      error
	}{
		{"agent cannot publish", agent, func(*checkWorld, *domain.LessonInput) {}, domain.ErrForbidden},
		{"revoked verifier", verifier, func(w *checkWorld, _ *domain.LessonInput) { w.revoked["verifier"] = true }, domain.ErrUnauthorized},
		{"stale expected head", verifier, func(w *checkWorld, _ *domain.LessonInput) { w.heads["lesson-retry"] = "newer" }, domain.ErrConflict},
		{"support from another project", verifier, func(w *checkWorld, _ *domain.LessonInput) {
			s := w.states["s1"]
			s.Claim.Scope.ProjectID = "other"
			w.states["s1"] = s
		}, domain.ErrForbidden},
		{"invalid sharing scope", verifier, func(_ *checkWorld, in *domain.LessonInput) { in.SharingScope = "public" }, domain.ErrInvalidInput},
		{"duplicate support", verifier, func(_ *checkWorld, in *domain.LessonInput) { in.SupportRevisions = []string{"s1", "s1"} }, domain.ErrInvalidInput},
		{"no support", verifier, func(_ *checkWorld, in *domain.LessonInput) { in.SupportRevisions = nil }, domain.ErrInvalidInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newCheckWorld()
			w.verified("s1", ciGo)
			in := lessonInput(domain.SharingCrossProject)
			tc.mutate(w, &in)

			_, err := w.lessons().PublishLesson(t.Context(), tc.principal, in)

			assert.ErrorIs(t, err, tc.want)
			assert.Empty(t, w.publications)
		})
	}
}

func TestLessonPreservesEveryTransitiveCondition(t *testing.T) {
	goOnly := domain.Applicability{Versions: map[string]string{"go": "1.27"}}
	chain := func(w *checkWorld) {
		w.verified("b", goOnly)
		w.verified("a", domain.Applicability{Environment: "ci"}, "b")
	}
	cases := []struct {
		name     string
		seed     func(*checkWorld)
		supports []string
		lesson   domain.Applicability
		state    string
		reason   domain.ReasonCode
	}{
		{"dependency environment applies through direct support", func(w *checkWorld) {
			w.verified("s0", domain.Applicability{Environment: "production"})
			w.verified("s1", goOnly, "s0")
		}, []string{"s1"}, domain.Applicability{Environment: "dev", Versions: goOnly.Versions}, domain.PublicationRejected, domain.ReasonEnvironmentMismatch},
		{"conflicting explicit support listed after its dependent", chain, []string{"a", "b"}, domain.Applicability{Environment: "ci", Versions: map[string]string{"go": "1.28"}}, domain.PublicationRejected, domain.ReasonOvergeneralized},
		{"conflicting explicit support listed before its dependent", chain, []string{"b", "a"}, domain.Applicability{Environment: "ci", Versions: map[string]string{"go": "1.28"}}, domain.PublicationRejected, domain.ReasonOvergeneralized},
		{"compatible explicit support after its dependent", chain, []string{"a", "b"}, ciGo, domain.PublicationPublished, ""},
		{"compatible explicit support before its dependent", chain, []string{"b", "a"}, ciGo, domain.PublicationPublished, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newCheckWorld()
			tc.seed(w)
			in := lessonInput(domain.SharingProject)
			in.SupportRevisions, in.Applicability = tc.supports, tc.lesson

			publication, err := w.lessons().PublishLesson(t.Context(), verifier, in)

			require.NoError(t, err)
			assert.Equal(t, tc.state, publication.State)
			if tc.reason != "" {
				assert.Contains(t, publication.Reasons, tc.reason)
			}
		})
	}
}

func TestShortSourceIdentifiersAreScreened(t *testing.T) {
	cases := []struct {
		name, project, text string
		state               string
	}{
		{"standalone short project id", "db", "Applies only to project db.", domain.PublicationPending},
		{"standalone short revision id", "demo", "Derived from s1 in CI.", domain.PublicationPending},
		{"short id inside a word", "db", "Retry the sdb cache tests1 once in CI.", domain.PublicationPublished},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newCheckWorld()
			w.verified("s1", ciGo)
			s := w.states["s1"]
			s.Claim.Scope.ProjectID = tc.project
			w.states["s1"] = s
			p := verifier
			p.ProjectIDs = []string{tc.project}
			in := lessonInput(domain.SharingCrossProject)
			in.ProjectID, in.Text = tc.project, tc.text

			publication, err := w.lessons().PublishLesson(t.Context(), p, in)

			require.NoError(t, err)
			assert.Equal(t, tc.state, publication.State)
			if tc.state == domain.PublicationPending {
				assert.Contains(t, publication.Reasons, domain.ReasonRestrictedSourceContent)
			}
		})
	}
}

func TestHostLessonPublicationRequiresPreviewedDigest(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*checkWorld, *LessonPublishInput)
		want   error
	}{
		{"exact confirmation publishes", func(*checkWorld, *LessonPublishInput) {}, nil},
		{"different digest", func(_ *checkWorld, in *LessonPublishInput) { in.ExpectedDigest = TokenDigest("other") }, domain.ErrStale},
		{"input changed after preview", func(_ *checkWorld, in *LessonPublishInput) { in.Text = "Retry flaky tests twice in CI." }, domain.ErrStale},
		{"support invalidated after preview", func(w *checkWorld, _ *LessonPublishInput) {
			s := w.states["s1"]
			s.Status, s.Valid = domain.ClaimInvalidated, false
			w.states["s1"] = s
		}, domain.ErrStale},
		{"owner missing", func(w *checkWorld, _ *LessonPublishInput) { w.owner = OwnerIdentity{} }, domain.ErrForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newCheckWorld()
			w.verified("s1", ciGo)
			lessons := w.lessons()
			in := lessonInput(domain.SharingCrossProject)
			preview, err := lessons.PreviewLessonHost(t.Context(), in)
			require.NoError(t, err)
			require.Equal(t, domain.PublicationPublished, preview.State)
			require.Empty(t, w.publications, "preview is read-only")
			confirm := LessonPublishInput{LessonInput: in, ExpectedDigest: preview.Digest}
			tc.mutate(w, &confirm)

			publication, err := lessons.PublishLessonHost(t.Context(), confirm)

			if tc.want != nil {
				assert.ErrorIs(t, err, tc.want)
				assert.Empty(t, w.publications)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, domain.PublicationPublished, publication.State)
			assert.Equal(t, w.owner.ID, w.states[publication.ClaimRevision].Claim.SubmittedBy)
			assert.Len(t, w.publications, 1)
		})
	}
}

func TestHostLessonPreviewReportsPolicyOutcome(t *testing.T) {
	w := newCheckWorld()
	w.verified("s1", ciGo)
	in := lessonInput(domain.SharingCrossProject)
	in.Applicability = domain.Applicability{Environment: "ci"}

	preview, err := w.lessons().PreviewLessonHost(t.Context(), in)

	require.NoError(t, err)
	assert.Equal(t, domain.PublicationRejected, preview.State)
	assert.Contains(t, preview.Reasons, domain.ReasonOvergeneralized)
	assert.Equal(t, []string{"s1"}, preview.SupportRevisions)
	assert.NotEmpty(t, preview.Digest)
}

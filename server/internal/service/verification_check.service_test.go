package service

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const checkHead = "0123456789abcdef0123456789abcdef01234567"

// checkWorld fakes every repository and gateway boundary used by Check.
type checkWorld struct {
	*verificationRepoFixture
	owner         OwnerIdentity
	revoked       map[string]bool
	artifacts     map[string]domain.Artifact
	uploaders     map[string]string
	objects       map[string][]byte
	attestations  map[string]domain.ExecutionAttestation
	verifications map[string]domain.Verification
	snapshots     map[string]domain.SourceSnapshot
	sourceReads   int
	policy        domain.Policy
}

func newCheckWorld() *checkWorld {
	return &checkWorld{
		verificationRepoFixture: &verificationRepoFixture{states: map[string]domain.RevisionState{}, heads: map[string]string{}, decisions: map[string]domain.OwnerDecision{}},
		owner:                   OwnerIdentity{ID: "owner", PublicKey: ownerNostrPublicKey},
		revoked:                 map[string]bool{}, artifacts: map[string]domain.Artifact{}, uploaders: map[string]string{}, objects: map[string][]byte{},
		attestations: map[string]domain.ExecutionAttestation{}, verifications: map[string]domain.Verification{}, snapshots: map[string]domain.SourceSnapshot{},
		policy: domain.Policy{Revision: "p1"},
	}
}
func (w *checkWorld) Owner(context.Context, Tx) (OwnerIdentity, error) { return w.owner, nil }
func (w *checkWorld) Snapshot(_ context.Context, _ Tx, p domain.Principal, _ domain.Scope) (domain.RetrievalSnapshot, error) {
	if w.revoked[p.ClientID] {
		return domain.RetrievalSnapshot{}, domain.ErrUnauthorized
	}
	return domain.RetrievalSnapshot{Principal: p, AuthEpoch: p.AuthEpoch}, nil
}
func (w *checkWorld) Artifact(_ context.Context, _ Tx, id string) (domain.Artifact, string, error) {
	a, ok := w.artifacts[id]
	if !ok {
		return a, "", domain.ErrIncomplete
	}
	return a, w.uploaders[id], nil
}
func (w *checkWorld) Policy(context.Context, Tx, string) (domain.Policy, error) {
	return w.policy, nil
}
func (w *checkWorld) Attestation(_ context.Context, _ Tx, id string) (domain.ExecutionAttestation, error) {
	a, ok := w.attestations[id]
	if !ok {
		return a, domain.ErrIncomplete
	}
	return a, nil
}
func (w *checkWorld) LoadVerification(_ context.Context, _ Tx, id string) (domain.Verification, error) {
	v, ok := w.verifications[id]
	if !ok {
		return v, domain.ErrIncomplete
	}
	return v, nil
}
func (w *checkWorld) PutVerification(ctx context.Context, tx Tx, v domain.Verification, status domain.ClaimStatus, valid bool) error {
	w.verifications[v.ID] = v
	return w.verificationRepoFixture.PutVerification(ctx, tx, v, status, valid)
}
func (w *checkWorld) ReadSnapshot(_ context.Context, repo, revision, path string) (domain.SourceSnapshot, error) {
	w.sourceReads++
	s, ok := w.snapshots[repo+"@"+revision+":"+path]
	if !ok {
		return s, domain.ErrIncomplete
	}
	return s, nil
}
func (w *checkWorld) Put(context.Context, string, io.Reader, string) error { return nil }
func (w *checkWorld) Delete(context.Context, string) error                 { return nil }
func (w *checkWorld) Open(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := w.objects[key]
	if !ok {
		return nil, domain.ErrIncomplete
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

var checkScope = domain.Scope{ProjectID: "demo", TaskID: "task"}

func (w *checkWorld) artifact(id, uploader string, scope domain.Scope, locator string, body []byte) domain.EvidenceRef {
	digest := TokenDigest(string(body))
	w.artifacts[id] = domain.Artifact{ID: id, ObjectKey: "obj/" + id, Digest: digest, PolicyRevision: "p1", SourceLocator: locator, Generation: "g1", Scope: scope, Bytes: int64(len(body)), State: domain.ArtifactReady, ObjectAvailable: true}
	w.uploaders[id] = uploader
	w.objects["obj/"+id] = body
	return domain.EvidenceRef{ArtifactID: id, Digest: digest, Locator: locator, SourceRevision: "source"}
}
func (w *checkWorld) gitFile(repo, path string, body []byte) {
	digest := TokenDigest(string(body))
	w.snapshots[repo+"@"+checkHead+":"+path] = domain.SourceSnapshot{Bytes: body, Digest: digest, Locator: domain.GitLocator(repo, path), Revision: checkHead, RepositoryID: repo, Path: path, Origin: domain.Origin{ClientID: "gateway:git", Trust: domain.TrustGatewayObserved, AttestationID: TokenDigest(repo + path + digest)}}
}
func (w *checkWorld) gitRef(id, repo, path string, body []byte) domain.EvidenceRef {
	ref := w.artifact(id, "agent", checkScope, domain.GitLocator(repo, path), body)
	ref.SourceRevision = checkHead
	return ref
}
func (w *checkWorld) candidate(revision, kind, text string, a domain.Applicability, refs ...domain.EvidenceRef) {
	c := domain.ClaimRevision{ClaimID: "claim-" + revision, Revision: revision, Text: text, Kind: kind, Scope: checkScope, Evidence: refs, Applicability: a, Status: domain.ClaimCandidate, SubmittedBy: "agent"}
	w.states[revision] = domain.RevisionState{Claim: c, Status: domain.ClaimCandidate}
	w.heads[c.ClaimID] = revision
}
func (w *checkWorld) service() *VerificationService {
	return NewVerificationService(directTx{}, w, w, w, nil, nil).WithEvidenceChecks(EvidenceChecks{Principals: w, Evidence: w, Attestations: w, Sources: w, Objects: w})
}

var verifier = domain.Principal{ClientID: "verifier", Role: domain.RoleVerifier, ProjectIDs: []string{"demo"}, Capabilities: []string{"verification:write"}}

func attested(w *checkWorld, id string, exit int, env string) domain.EvidenceRef {
	ref := w.artifact(id, "collector", checkScope, "run/"+id, []byte("collector output"))
	w.attestations[id] = domain.ExecutionAttestation{ID: "att-" + id, ArtifactID: id, ArtifactDigest: ref.Digest, CollectorID: "collector", Command: "go test ./...", ExitCode: exit, WorkspaceRevision: checkHead, Environment: env}
	return ref
}

func TestUnattestedTestOutputCannotVerify(t *testing.T) {
	w := newCheckWorld()
	ref := w.artifact("agent-log", "agent", checkScope, "log#1", []byte("ok  \texample\nexit status 0"))
	w.candidate("r1", domain.KindTestOutcome, `command "go test ./..." exited with status 0`, domain.Applicability{Environment: "ci", RepositoryRevision: checkHead}, ref)

	verification, err := w.service().Check(t.Context(), verifier, "r1")

	require.NoError(t, err)
	assert.Equal(t, "unsupported", string(verification.Outcome))
	assert.Contains(t, verification.Reasons, domain.ReasonUnattestedExecution)
	assert.Equal(t, domain.ClaimCandidate, w.states["r1"].Status)
	assert.False(t, w.states["r1"].Valid)
}

func TestAuthenticatedPreferenceRequiresExactStatement(t *testing.T) {
	const statement = "Prefer table-driven tests for Go services."
	cases := []struct {
		name, uploader, evidence string
		scope                    domain.Scope
		want                     domain.VerificationOutcome
		reason                   domain.ReasonCode
	}{
		{"exact owner statement", "", statement, domain.Scope{ProjectID: "demo"}, domain.VerificationSupported, ""},
		{"owner said something narrower", "", "Prefer table-driven tests for parsers.", domain.Scope{ProjectID: "demo"}, domain.VerificationUnsupported, domain.ReasonPreferenceNotExact},
		{"user-labeled transcript from agent", "agent", statement, checkScope, domain.VerificationUnsupported, domain.ReasonOwnerNotAuthenticated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newCheckWorld()
			uploader := tc.uploader
			if uploader == "" {
				uploader = ownerActorPrefix + TokenDigest(w.owner.ID)
			}
			ref := w.artifact("statement", uploader, tc.scope, "owner#1", []byte(tc.evidence))
			w.candidate("r1", domain.KindOwnerPreference, statement, domain.Applicability{}, ref)

			verification, err := w.service().Check(t.Context(), verifier, "r1")

			require.NoError(t, err)
			assert.Equal(t, tc.want, verification.Outcome)
			if tc.reason != "" {
				assert.Contains(t, verification.Reasons, tc.reason)
				assert.Equal(t, domain.ClaimCandidate, w.states["r1"].Status)
			} else {
				assert.Equal(t, domain.ClaimVerified, w.states["r1"].Status)
				assert.Equal(t, domain.MethodEvidencePolicy, verification.Method)
			}
		})
	}
}

func TestVersionRangeIsNotInstalledVersion(t *testing.T) {
	cases := []struct {
		name, manifest string
		want           domain.VerificationOutcome
		reason         domain.ReasonCode
	}{
		{"declared range", `{"dependencies":{"left-pad":"^1.3.0"}}`, domain.VerificationUnsupported, domain.ReasonVersionRangeNotInstalled},
		{"different exact version", `{"dependencies":{"left-pad":"1.2.0"}}`, domain.VerificationUnsupported, domain.ReasonVersionMismatch},
		{"exact declared version", `{"dependencies":{"left-pad":"1.3.0"}}`, domain.VerificationSupported, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newCheckWorld()
			w.gitFile("demo", "package.json", []byte(tc.manifest))
			ref := w.gitRef("manifest", "demo", "package.json", []byte(tc.manifest))
			w.candidate("r1", domain.KindDependencyVersion, "left-pad is 1.3.0", domain.Applicability{RepositoryRevision: checkHead, Versions: map[string]string{"left-pad": "1.3.0"}}, ref)

			verification, err := w.service().Check(t.Context(), verifier, "r1")

			require.NoError(t, err)
			assert.Equal(t, tc.want, verification.Outcome)
			if tc.reason != "" {
				assert.Contains(t, verification.Reasons, tc.reason)
			} else {
				assert.Equal(t, domain.TrustGatewayObserved, verification.Origin.Trust)
			}
		})
	}
}

func TestFailureDoesNotProveCause(t *testing.T) {
	cases := []struct {
		name, kind, text string
		want             domain.VerificationOutcome
	}{
		{"causal explanation", "causal", "Tests fail because the cache is stale.", domain.VerificationUnsupported},
		{"outcome with invented cause", domain.KindTestOutcome, `command "go test ./..." exited with status 1 because the cache is stale`, domain.VerificationUnsupported},
		{"narrow failed observation", domain.KindTestOutcome, `command "go test ./..." exited with status 1`, domain.VerificationSupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newCheckWorld()
			ref := attested(w, "run", 1, "ci")
			w.candidate("r1", tc.kind, tc.text, domain.Applicability{Environment: "ci", RepositoryRevision: checkHead}, ref)

			verification, err := w.service().Check(t.Context(), verifier, "r1")

			require.NoError(t, err)
			assert.Equal(t, tc.want, verification.Outcome)
			if tc.want != domain.VerificationSupported {
				assert.Contains(t, verification.Reasons, domain.ReasonFailureDoesNotProveCause)
			}
		})
	}
}

func TestRepeatedDerivedSourceCountsOnce(t *testing.T) {
	w := newCheckWorld()
	manifest := []byte(`{"dependencies":{"left-pad":"1.3.0"}}`)
	w.gitFile("demo", "package.json", manifest)
	refs := []domain.EvidenceRef{w.gitRef("copy-1", "demo", "package.json", manifest), w.gitRef("copy-2", "demo", "package.json", manifest), w.gitRef("copy-3", "demo", "package.json", manifest)}
	w.candidate("r1", domain.KindDependencyVersion, "left-pad is 1.3.0", domain.Applicability{RepositoryRevision: checkHead, Versions: map[string]string{"left-pad": "1.3.0"}}, refs...)

	verification, err := w.service().Check(t.Context(), verifier, "r1")

	require.NoError(t, err)
	distinctEvidenceOrigins := verification.DistinctOrigins
	assert.Len(t, distinctEvidenceOrigins, 1)
	assert.Equal(t, 1, w.sourceReads)
}

func TestRepeatedAssistantClaimIsNotCorroboration(t *testing.T) {
	w := newCheckWorld()
	refs := []domain.EvidenceRef{w.artifact("say-1", "agent", checkScope, "t#1", []byte("The cache causes it.")), w.artifact("say-2", "agent", checkScope, "t#2", []byte("As I said, the cache causes it."))}
	w.candidate("r1", "causal", "The cache causes it.", domain.Applicability{}, refs...)

	verification, err := w.service().Check(t.Context(), verifier, "r1")

	require.NoError(t, err)
	assert.Len(t, verification.DistinctOrigins, 1)
	assert.Equal(t, domain.VerificationAmbiguous, verification.Outcome)
	assert.Equal(t, []domain.ReasonCode{domain.ReasonRequiresOwnerAdjudication}, verification.Reasons)
	assert.Equal(t, domain.ClaimCandidate, w.states["r1"].Status)
}

func TestWrongEnvironmentCannotPublish(t *testing.T) {
	w := newCheckWorld()
	ref := attested(w, "run", 0, "local")
	w.candidate("r1", domain.KindTestOutcome, `command "go test ./..." exited with status 0`, domain.Applicability{Environment: "production", RepositoryRevision: checkHead}, ref)

	verification, err := w.service().Check(t.Context(), verifier, "r1")

	require.NoError(t, err)
	assert.Equal(t, domain.VerificationUnsupported, verification.Outcome)
	assert.Contains(t, verification.Reasons, domain.ReasonEnvironmentMismatch)
	assert.Equal(t, domain.ClaimCandidate, w.states["r1"].Status)
	lessons := w.lessons()
	publication, err := lessons.PublishLesson(t.Context(), verifier, domain.LessonInput{ProjectID: "demo", LessonID: "lesson", Text: "Run the suite before deploying.", SharingScope: domain.SharingProject, Applicability: domain.Applicability{Environment: "production"}, SupportRevisions: []string{"r1"}})
	require.NoError(t, err)
	assert.Equal(t, domain.PublicationRejected, publication.State)
	assert.Contains(t, publication.Reasons, domain.ReasonSupportNotVerified)
	assert.Empty(t, w.publications)
}

func TestCheckAuthority(t *testing.T) {
	agent := verifier
	agent.Role = domain.RoleAgent
	missing := verifier
	missing.Capabilities = []string{"retrieval:read"}
	other := verifier
	other.ProjectIDs = []string{"other"}
	cases := []struct {
		name      string
		principal domain.Principal
		revoke    bool
		want      error
	}{
		{"agent role", agent, false, domain.ErrForbidden},
		{"missing capability", missing, false, domain.ErrForbidden},
		{"other project", other, false, domain.ErrForbidden},
		{"revoked verifier", verifier, true, domain.ErrUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newCheckWorld()
			w.revoked["verifier"] = tc.revoke
			w.candidate("r1", "causal", "x", domain.Applicability{}, w.artifact("a", "agent", checkScope, "l", []byte("x")))

			_, err := w.service().Check(t.Context(), tc.principal, "r1")

			assert.ErrorIs(t, err, tc.want)
			assert.Empty(t, w.verifications)
		})
	}
}

func TestCheckIsIdempotentAndRequiresCurrentCandidate(t *testing.T) {
	w := newCheckWorld()
	ref := attested(w, "run", 0, "ci")
	w.candidate("r1", domain.KindTestOutcome, `command "go test ./..." exited with status 1`, domain.Applicability{Environment: "ci", RepositoryRevision: checkHead}, ref)
	s := w.service()
	first, err := s.Check(t.Context(), verifier, "r1")
	require.NoError(t, err)

	second, err := s.Check(t.Context(), verifier, "r1")

	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.Len(t, w.verifications, 1)
	t.Run("superseded head", func(t *testing.T) {
		w.heads["claim-r1"] = "r2"
		_, err := s.Check(t.Context(), verifier, "r1")
		assert.ErrorIs(t, err, domain.ErrConflict)
	})
	t.Run("unconfigured service", func(t *testing.T) {
		_, err := NewVerificationService(directTx{}, w, w, w, nil, nil).Check(t.Context(), verifier, "r1")
		assert.ErrorIs(t, err, domain.ErrUnavailable)
	})
}

func TestCheckRejectsTamperedEvidence(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*checkWorld)
		want   error
		reason domain.ReasonCode
	}{
		{"object bytes changed", func(w *checkWorld) { w.objects["obj/run"] = []byte("forged") }, domain.ErrIncomplete, ""},
		{"attestation for another collector", func(w *checkWorld) {
			a := w.attestations["run"]
			a.CollectorID = "agent"
			w.attestations["run"] = a
		}, nil, domain.ReasonUnattestedExecution},
		{"agent copy differs from repository", func(w *checkWorld) {
			w.gitFile("demo", "go.mod", []byte("module x\nrequire example.com/m v1.0.0\n"))
		}, nil, domain.ReasonSourceMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newCheckWorld()
			refs := []domain.EvidenceRef{attested(w, "run", 0, "ci")}
			kind, text := domain.KindTestOutcome, `command "go test ./..." exited with status 0`
			applicability := domain.Applicability{Environment: "ci", RepositoryRevision: checkHead}
			if tc.reason == domain.ReasonSourceMismatch {
				refs = []domain.EvidenceRef{w.gitRef("mod", "demo", "go.mod", []byte("module x\nrequire example.com/m v9.0.0\n"))}
				kind, text = domain.KindDependencyVersion, "m is 9.0.0"
				applicability = domain.Applicability{RepositoryRevision: checkHead, Versions: map[string]string{"example.com/m": "v9.0.0"}}
			}
			w.candidate("r1", kind, text, applicability, refs...)
			tc.mutate(w)

			verification, err := w.service().Check(t.Context(), verifier, "r1")

			if tc.want != nil {
				assert.ErrorIs(t, err, tc.want)
				return
			}
			require.NoError(t, err)
			assert.Contains(t, verification.Reasons, tc.reason)
		})
	}
}

func TestStoredTrustIsTheTrustThatVoted(t *testing.T) {
	w := newCheckWorld()
	manifest := []byte(`{"dependencies":{"left-pad":"1.3.0"}}`)
	w.gitFile("demo", "package.json", manifest)
	owned := w.artifact("owner-note", ownerActorPrefix+TokenDigest(w.owner.ID), domain.Scope{ProjectID: "demo"}, "owner#1", []byte("left-pad is 1.3.0"))
	w.candidate("r1", domain.KindDependencyVersion, "left-pad is 1.3.0", domain.Applicability{RepositoryRevision: checkHead, Versions: map[string]string{"left-pad": "1.3.0"}}, w.gitRef("manifest", "demo", "package.json", manifest), owned)

	verification, err := w.service().Check(t.Context(), verifier, "r1")

	require.NoError(t, err)
	assert.Equal(t, domain.VerificationSupported, verification.Outcome)
	assert.Equal(t, domain.TrustGatewayObserved, verification.Origin.Trust)
}

func TestInvalidatedRevisionNeverReplaysSupport(t *testing.T) {
	w := newCheckWorld()
	manifest := []byte(`{"dependencies":{"left-pad":"1.3.0"}}`)
	w.gitFile("demo", "package.json", manifest)
	w.candidate("r1", domain.KindDependencyVersion, "left-pad is 1.3.0", domain.Applicability{RepositoryRevision: checkHead, Versions: map[string]string{"left-pad": "1.3.0"}}, w.gitRef("manifest", "demo", "package.json", manifest))
	s := w.service()
	first, err := s.Check(t.Context(), verifier, "r1")
	require.NoError(t, err)
	require.Equal(t, domain.VerificationSupported, first.Outcome)
	state := w.states["r1"]
	state.Status, state.Valid = domain.ClaimInvalidated, false
	w.states["r1"] = state

	_, err = s.Check(t.Context(), verifier, "r1")

	assert.ErrorIs(t, err, domain.ErrConflict)
}

func TestExcludedGitLocatorPathIsNeverRead(t *testing.T) {
	cases := []struct {
		name, path string
		want       error
	}{
		{"excluded locator path", "secrets/package.json", domain.ErrForbidden},
		{"permitted locator path", "package.json", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newCheckWorld()
			w.policy.ExcludedPaths = []string{"secrets/"}
			manifest := []byte(`{"dependencies":{"left-pad":"1.3.0"}}`)
			w.gitFile("demo", tc.path, manifest)
			ref := w.gitRef("manifest", "demo", tc.path, manifest)
			a := w.artifacts["manifest"]
			a.SourcePath = "package.json"
			w.artifacts["manifest"] = a
			w.candidate("r1", domain.KindDependencyVersion, "left-pad is 1.3.0", domain.Applicability{RepositoryRevision: checkHead, Versions: map[string]string{"left-pad": "1.3.0"}}, ref)

			_, err := w.service().Check(t.Context(), verifier, "r1")

			if tc.want == nil {
				require.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tc.want)
			assert.Zero(t, w.sourceReads)
		})
	}
}

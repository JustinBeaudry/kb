package domain_test

import (
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
)

const rev = "0123456789abcdef0123456789abcdef01234567"

func gatewayObs(content string) domain.EvidenceObservation {
	return domain.EvidenceObservation{Origin: domain.Origin{Trust: domain.TrustGatewayObserved}, SourceKey: "git:" + content, Revision: rev, Content: []byte(content)}
}

func TestEvaluateEvidencePolicy(t *testing.T) {
	versions := func(v string) domain.Applicability {
		return domain.Applicability{RepositoryRevision: rev, Versions: map[string]string{"example.com/m": v}}
	}
	cases := []struct {
		name    string
		claim   domain.ClaimRevision
		obs     []domain.EvidenceObservation
		outcome domain.VerificationOutcome
		reason  domain.ReasonCode
	}{
		{"no evidence", domain.ClaimRevision{Kind: domain.KindAPIContract}, nil, domain.VerificationUnsupported, domain.ReasonNoEvidence},
		{"declared contract", domain.ClaimRevision{Kind: domain.KindAPIContract, Text: `"schema_version": "3"`, Applicability: domain.Applicability{RepositoryRevision: rev}}, []domain.EvidenceObservation{gatewayObs(`{"schema_version": "3"}`)}, domain.VerificationSupported, ""},
		{"contract absent", domain.ClaimRevision{Kind: domain.KindAPIContract, Text: `"schema_version": "4"`, Applicability: domain.Applicability{RepositoryRevision: rev}}, []domain.EvidenceObservation{gatewayObs(`{"schema_version": "3"}`)}, domain.VerificationUnsupported, domain.ReasonContractNotDeclared},
		{"contract without revision", domain.ClaimRevision{Kind: domain.KindAPIContract, Text: "x"}, []domain.EvidenceObservation{gatewayObs("x")}, domain.VerificationUnsupported, domain.ReasonRevisionUnbound},
		{"contract at other revision", domain.ClaimRevision{Kind: domain.KindAPIContract, Text: "x", Applicability: domain.Applicability{RepositoryRevision: "other"}}, []domain.EvidenceObservation{gatewayObs("x")}, domain.VerificationUnsupported, domain.ReasonRevisionMismatch},
		{"submitted copy of contract", domain.ClaimRevision{Kind: domain.KindAPIContract, Text: "x"}, []domain.EvidenceObservation{{Origin: domain.Origin{Trust: domain.TrustSubmitted}, Content: []byte("x")}}, domain.VerificationUnsupported, domain.ReasonSourceNotObserved},
		{"go.mod exact", domain.ClaimRevision{Kind: domain.KindDependencyVersion, Applicability: versions("v1.2.3")}, []domain.EvidenceObservation{gatewayObs("module x\n\nrequire example.com/m v1.2.3\n")}, domain.VerificationSupported, ""},
		{"undeclared dependency", domain.ClaimRevision{Kind: domain.KindDependencyVersion, Applicability: versions("v1.2.3")}, []domain.EvidenceObservation{gatewayObs("module x\n")}, domain.VerificationUnsupported, domain.ReasonVersionNotDeclared},
		{"two versions claimed", domain.ClaimRevision{Kind: domain.KindDependencyVersion, Applicability: domain.Applicability{RepositoryRevision: rev, Versions: map[string]string{"a": "1", "b": "2"}}}, []domain.EvidenceObservation{gatewayObs("{}")}, domain.VerificationUnsupported, domain.ReasonClaimNotStructured},
		{"trusted sources disagree", domain.ClaimRevision{Kind: domain.KindDependencyVersion, Applicability: versions("1.0.0")}, []domain.EvidenceObservation{gatewayObs(`"example.com/m": "1.0.0"`), gatewayObs(`"example.com/m": "2.0.0"`)}, domain.VerificationUnsupported, domain.ReasonConflictingEvidence},
		{"workspace dirty state differs", domain.ClaimRevision{Kind: domain.KindTestOutcome, Text: `command "t" exited with status 0`, Applicability: domain.Applicability{Environment: "ci", RepositoryRevision: rev, DirtyDigest: "dirty"}}, []domain.EvidenceObservation{{Origin: domain.Origin{Trust: domain.TrustCollectorAttested}, Execution: &domain.ExecutionAttestation{Command: "t", WorkspaceRevision: rev, Environment: "ci"}}}, domain.VerificationUnsupported, domain.ReasonWorkspaceStateMismatch},
		{"success overstated", domain.ClaimRevision{Kind: domain.KindTestOutcome, Text: "everything works", Applicability: domain.Applicability{Environment: "ci", RepositoryRevision: rev}}, []domain.EvidenceObservation{{Origin: domain.Origin{Trust: domain.TrustCollectorAttested}, Execution: &domain.ExecutionAttestation{Command: "t", WorkspaceRevision: rev, Environment: "ci"}}}, domain.VerificationUnsupported, domain.ReasonStatementExceedsObservation},
		{"environment unbound", domain.ClaimRevision{Kind: domain.KindTestOutcome, Applicability: domain.Applicability{RepositoryRevision: rev}}, []domain.EvidenceObservation{{Origin: domain.Origin{Trust: domain.TrustCollectorAttested}, Execution: &domain.ExecutionAttestation{}}}, domain.VerificationUnsupported, domain.ReasonEnvironmentUnbound},
		{"tampered source copy", domain.ClaimRevision{Kind: domain.KindAPIContract}, []domain.EvidenceObservation{{Mismatch: true}}, domain.VerificationUnsupported, domain.ReasonSourceMismatch},
		{"general lesson needs owner", domain.ClaimRevision{Kind: "lesson"}, []domain.EvidenceObservation{gatewayObs("x")}, domain.VerificationAmbiguous, domain.ReasonRequiresOwnerAdjudication},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := domain.EvaluateEvidencePolicy(tc.claim, tc.obs)

			assert.Equal(t, tc.outcome, d.Outcome)
			assert.Equal(t, domain.EvidencePolicyVersion, d.PolicyVersion)
			if tc.reason != "" {
				assert.Contains(t, d.Reasons, tc.reason)
			} else {
				assert.Empty(t, d.Reasons)
			}
		})
	}
}

func TestLessonTextKeepsCounterexamples(t *testing.T) {
	assert.Equal(t, "L", domain.LessonText("L", nil))
	assert.Equal(t, "L\nNot applicable when: a; b", domain.LessonText("L", []string{"a", "b"}))
}

func TestParseGitLocatorRejectsIncomplete(t *testing.T) {
	for _, locator := range []string{"git:", "git:repo", "git::path", "git:repo:"} {
		_, _, ok := domain.ParseGitLocator(locator)
		assert.False(t, ok, locator)
	}
}

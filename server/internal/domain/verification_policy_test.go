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
	dep := func(v, _ string) domain.ClaimRevision {
		return domain.ClaimRevision{Kind: domain.KindDependencyVersion, Text: "widget is " + v, Applicability: domain.Applicability{RepositoryRevision: rev, Versions: map[string]string{"widget": v}}}
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
		{"go.mod exact", domain.ClaimRevision{Kind: domain.KindDependencyVersion, Text: "example.com/m is v1.2.3", Applicability: versions("v1.2.3")}, []domain.EvidenceObservation{gatewayObs("module x\n\nrequire example.com/m v1.2.3\n")}, domain.VerificationSupported, ""},
		{"undeclared dependency", domain.ClaimRevision{Kind: domain.KindDependencyVersion, Applicability: versions("v1.2.3")}, []domain.EvidenceObservation{gatewayObs("module x\n")}, domain.VerificationUnsupported, domain.ReasonVersionNotDeclared},
		{"two versions claimed", domain.ClaimRevision{Kind: domain.KindDependencyVersion, Applicability: domain.Applicability{RepositoryRevision: rev, Versions: map[string]string{"a": "1", "b": "2"}}}, []domain.EvidenceObservation{gatewayObs("{}")}, domain.VerificationUnsupported, domain.ReasonClaimNotStructured},
		{"trusted sources disagree", domain.ClaimRevision{Kind: domain.KindDependencyVersion, Text: "example.com/m is 1.0.0", Applicability: versions("1.0.0")}, []domain.EvidenceObservation{gatewayObs(`{"dependencies":{"example.com/m":"1.0.0"}}`), gatewayObs(`{"dependencies":{"example.com/m":"2.0.0"}}`)}, domain.VerificationUnsupported, domain.ReasonConflictingEvidence},
		{"npm partial version is a range", dep("1.2", `{"dependencies":{"widget":"1.2"}}`), []domain.EvidenceObservation{gatewayObs(`{"dependencies":{"widget":"1.2"}}`)}, domain.VerificationUnsupported, domain.ReasonVersionRangeNotInstalled},
		{"npm full exact version", dep("1.2.3", `{"dependencies":{"widget":"1.2.3"}}`), []domain.EvidenceObservation{gatewayObs(`{"dependencies":{"widget":"1.2.3"}}`)}, domain.VerificationSupported, ""},
		{"script value is not a dependency", dep("1.2.3", ""), []domain.EvidenceObservation{gatewayObs(`{"scripts":{"widget":"1.2.3"},"dependencies":{"widget":"^2.0.0"}}`)}, domain.VerificationUnsupported, domain.ReasonVersionRangeNotInstalled},
		{"script only is undeclared", dep("1.2.3", ""), []domain.EvidenceObservation{gatewayObs(`{"scripts":{"widget":"1.2.3"}}`)}, domain.VerificationUnsupported, domain.ReasonVersionNotDeclared},
		{"lockfile installed version", dep("1.2.3", ""), []domain.EvidenceObservation{gatewayObs(`{"packages":{"":{"dependencies":{"widget":"^1.2.0"}},"node_modules/widget":{"version":"1.2.3"}}}`)}, domain.VerificationSupported, ""},
		{"go.mod replace block is not a requirement", domain.ClaimRevision{Kind: domain.KindDependencyVersion, Text: "example.com/m is v1.2.3", Applicability: versions("v1.2.3")}, []domain.EvidenceObservation{gatewayObs("module x\n\nreplace (\n\texample.com/m v1.2.3 => ../m\n)\n")}, domain.VerificationUnsupported, domain.ReasonVersionNotDeclared},
		{"go.mod require block", domain.ClaimRevision{Kind: domain.KindDependencyVersion, Text: "example.com/m is v1.2.3", Applicability: versions("v1.2.3")}, []domain.EvidenceObservation{gatewayObs("module x\n\nrequire (\n\texample.com/m v1.2.3 // indirect\n)\n")}, domain.VerificationSupported, ""},
		{"text names another version", domain.ClaimRevision{Kind: domain.KindDependencyVersion, Text: "widget is 2.0.0", Applicability: domain.Applicability{RepositoryRevision: rev, Versions: map[string]string{"widget": "1.2.3"}}}, []domain.EvidenceObservation{gatewayObs(`{"dependencies":{"widget":"1.2.3"}}`)}, domain.VerificationUnsupported, domain.ReasonStatementExceedsObservation},
		{"text omits the version", domain.ClaimRevision{Kind: domain.KindDependencyVersion, Text: "widget is pinned", Applicability: domain.Applicability{RepositoryRevision: rev, Versions: map[string]string{"widget": "1.2.3"}}}, []domain.EvidenceObservation{gatewayObs(`{"dependencies":{"widget":"1.2.3"}}`)}, domain.VerificationUnsupported, domain.ReasonStatementExceedsObservation},
		{"text names another dependency", domain.ClaimRevision{Kind: domain.KindDependencyVersion, Text: "gadget is 1.2.3", Applicability: domain.Applicability{RepositoryRevision: rev, Versions: map[string]string{"widget": "1.2.3"}}}, []domain.EvidenceObservation{gatewayObs(`{"dependencies":{"widget":"1.2.3"}}`)}, domain.VerificationUnsupported, domain.ReasonStatementExceedsObservation},
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

func TestRuleTrustNamesTheVotingTrust(t *testing.T) {
	cases := []struct {
		kind string
		want domain.OriginTrust
		ok   bool
	}{
		{domain.KindOwnerPreference, domain.TrustOwnerAuthenticated, true},
		{domain.KindDependencyVersion, domain.TrustGatewayObserved, true},
		{domain.KindAPIContract, domain.TrustGatewayObserved, true},
		{domain.KindTestOutcome, domain.TrustCollectorAttested, true},
		{"causal", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			got, ok := domain.RuleTrust(tc.kind)

			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.ok, ok)
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

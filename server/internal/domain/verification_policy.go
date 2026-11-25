package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// RevisionState keeps immutable submitted JSON separate from authoritative state.
type RevisionState struct {
	Claim  ClaimRevision
	Status ClaimStatus
	Valid  bool
}

// OwnerDecision is host-only idempotency state; RequestDigest binds the exact
// confirmation, so a replay never silently accepts a newly computed digest.
type OwnerDecision struct {
	ID, OwnerID, Key, InputDigest, RequestDigest, Action string
	CandidateRevision, TargetRevision                    string
	Receipt                                              Receipt
}

// ExcerptLineage is internal provenance. Both ends are pinned by this record;
// public retrieval never projects these original source identifiers.
type ExcerptLineage struct {
	ID, DecisionID, InputDigest                                      string
	SourceArtifactID, SourceRevision, SourceDigest, SourceGeneration string
	SupportRevision                                                  string
	Start, End                                                       int64
	ExcerptArtifactID, ExcerptDigest, ExcerptGeneration              string
	SourceOrigin                                                     Origin
}

// EvidencePolicyVersion names the deterministic rule set used by automatic
// verification. A rule change must bump it so recorded outcomes stay auditable.
const EvidencePolicyVersion = "evidence-policy.v1"

// Verification methods distinguish how a supported outcome was reached. An
// owner judgment is never relabeled as an empirical check.
const (
	MethodOwnerAdjudicated = "owner_adjudicated"
	MethodEvidencePolicy   = "evidence_policy"
	MethodLessonPolicy     = "lesson_policy"
)

// Claim kinds with mechanically checkable semantics. Every other kind (causal
// explanations, general lessons, deployment behavior) needs owner adjudication.
const (
	KindOwnerPreference   = "owner_preference"
	KindDependencyVersion = "dependency_version"
	KindAPIContract       = "api_contract"
	KindTestOutcome       = "test_outcome"
	KindLesson            = "lesson"
)

// ReasonCode explains why an outcome is not supported. Codes are stable API.
type ReasonCode string

const (
	ReasonNoEvidence                  ReasonCode = "no_evidence"
	ReasonOwnerNotAuthenticated       ReasonCode = "owner_not_authenticated"
	ReasonPreferenceNotExact          ReasonCode = "preference_not_exact"
	ReasonSourceNotObserved           ReasonCode = "source_not_independently_observed"
	ReasonSourceMismatch              ReasonCode = "source_mismatch"
	ReasonRevisionUnbound             ReasonCode = "revision_unbound"
	ReasonRevisionMismatch            ReasonCode = "revision_mismatch"
	ReasonClaimNotStructured          ReasonCode = "claim_not_structured"
	ReasonVersionNotDeclared          ReasonCode = "version_not_declared"
	ReasonVersionRangeNotInstalled    ReasonCode = "version_range_not_installed"
	ReasonVersionMismatch             ReasonCode = "version_mismatch"
	ReasonContractNotDeclared         ReasonCode = "contract_not_declared"
	ReasonUnattestedExecution         ReasonCode = "unattested_execution"
	ReasonEnvironmentUnbound          ReasonCode = "environment_unbound"
	ReasonEnvironmentMismatch         ReasonCode = "environment_mismatch"
	ReasonWorkspaceStateMismatch      ReasonCode = "workspace_state_mismatch"
	ReasonFailureDoesNotProveCause    ReasonCode = "failure_does_not_prove_cause"
	ReasonStatementExceedsObservation ReasonCode = "statement_exceeds_observation"
	ReasonConflictingEvidence         ReasonCode = "conflicting_evidence"
	ReasonRequiresOwnerAdjudication   ReasonCode = "requires_owner_adjudication"
	ReasonApplicabilityMissing        ReasonCode = "applicability_missing"
	ReasonOvergeneralized             ReasonCode = "overgeneralized_applicability"
	ReasonSupportNotVerified          ReasonCode = "support_not_verified"
	ReasonDisputedSupport             ReasonCode = "disputed_support"
	ReasonRestrictedSourceContent     ReasonCode = "restricted_source_content"
	ReasonPossibleSecret              ReasonCode = "possible_secret"
)

// ExecutionAttestation is written by a qualified collector outside agent write
// authority. It binds the actual invocation, result and workspace state.
type ExecutionAttestation struct {
	ID, ArtifactID, ArtifactDigest, CollectorID string
	Command                                     string
	ExitCode                                    int
	WorkspaceRevision, DirtyDigest, Environment string
}

// SourceSnapshot is an independent read of an approved repository at an exact
// observed revision. Origin is always gateway-derived.
type SourceSnapshot struct {
	Bytes                     []byte
	Digest, Locator, Revision string
	RepositoryID, Path        string
	Origin                    Origin
}

// EvidenceObservation is one authenticated view of evidence for policy checks.
// SourceKey identifies the underlying source: copies and excerpts derived from
// the same source share it and therefore count once.
type EvidenceObservation struct {
	Origin    Origin
	SourceKey string
	Revision  string
	Content   []byte
	Execution *ExecutionAttestation
	Mismatch  bool
}

// PolicyDecision is the deterministic result of the evidence policy.
type PolicyDecision struct {
	PolicyVersion   string
	Outcome         VerificationOutcome
	Method          string
	Reasons         []ReasonCode
	DistinctOrigins []string
}

// DistinctEvidenceOrigins collapses repeated derived sources to one origin.
func DistinctEvidenceOrigins(obs []EvidenceObservation) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, o := range obs {
		key := o.SourceKey
		if key == "" {
			key = string(o.Origin.Trust) + ":" + o.Origin.AttestationID
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	slices.Sort(out)
	return out
}

// ExecutionStatement renders the only statement an attested run supports.
func ExecutionStatement(a ExecutionAttestation) string {
	return fmt.Sprintf("command %q exited with status %d", a.Command, a.ExitCode)
}

// EvaluateEvidencePolicy applies versioned deterministic rules. It never runs
// commands and never consults model output; unsupported claims keep reasons.
func EvaluateEvidencePolicy(c ClaimRevision, obs []EvidenceObservation) PolicyDecision {
	d := PolicyDecision{PolicyVersion: EvidencePolicyVersion, Method: MethodEvidencePolicy, DistinctOrigins: DistinctEvidenceOrigins(obs)}
	reasons := policyReasons(c, obs)
	if len(reasons) == 0 {
		d.Outcome = VerificationSupported
		return d
	}
	d.Reasons = reasons
	d.Outcome = VerificationUnsupported
	if len(reasons) == 1 && reasons[0] == ReasonRequiresOwnerAdjudication {
		d.Outcome = VerificationAmbiguous
	}
	return d
}

// RuleTrust names the only origin trust whose observations vote for kind. A
// supported outcome is established solely by observations of this trust.
func RuleTrust(kind string) (OriginTrust, bool) {
	switch kind {
	case KindOwnerPreference:
		return TrustOwnerAuthenticated, true
	case KindDependencyVersion, KindAPIContract:
		return TrustGatewayObserved, true
	case KindTestOutcome:
		return TrustCollectorAttested, true
	}
	return "", false
}

func policyReasons(c ClaimRevision, obs []EvidenceObservation) []ReasonCode {
	if len(obs) == 0 {
		return []ReasonCode{ReasonNoEvidence}
	}
	for _, o := range obs {
		if o.Mismatch {
			return []ReasonCode{ReasonSourceMismatch}
		}
	}
	switch c.Kind {
	case KindOwnerPreference:
		return checkEach(obs, TrustOwnerAuthenticated, ReasonOwnerNotAuthenticated, func(o EvidenceObservation) []ReasonCode {
			if strings.TrimSpace(string(o.Content)) != strings.TrimSpace(c.Text) {
				return []ReasonCode{ReasonPreferenceNotExact}
			}
			return nil
		})
	case KindDependencyVersion:
		return checkEach(obs, TrustGatewayObserved, ReasonSourceNotObserved, func(o EvidenceObservation) []ReasonCode {
			return checkDeclaredVersion(c, o)
		})
	case KindAPIContract:
		return checkEach(obs, TrustGatewayObserved, ReasonSourceNotObserved, func(o EvidenceObservation) []ReasonCode {
			if r := checkRevision(c, o.Revision); r != nil {
				return r
			}
			if strings.TrimSpace(c.Text) == "" || !bytes.Contains(o.Content, []byte(c.Text)) {
				return []ReasonCode{ReasonContractNotDeclared}
			}
			return nil
		})
	case KindTestOutcome:
		return checkEach(obs, TrustCollectorAttested, ReasonUnattestedExecution, func(o EvidenceObservation) []ReasonCode {
			return checkExecution(c, o)
		})
	}
	for _, o := range obs {
		if o.Execution != nil && o.Origin.Trust == TrustCollectorAttested && o.Execution.ExitCode != 0 {
			return []ReasonCode{ReasonFailureDoesNotProveCause}
		}
	}
	return []ReasonCode{ReasonRequiresOwnerAdjudication}
}

// checkEach requires at least one observation with the required trust. Every
// trusted observation must agree; lower-trust copies never vote.
func checkEach(obs []EvidenceObservation, trust OriginTrust, missing ReasonCode, rule func(EvidenceObservation) []ReasonCode) []ReasonCode {
	supported, trusted := false, false
	var failed []ReasonCode
	for _, o := range obs {
		if o.Origin.Trust != trust || (trust == TrustCollectorAttested && o.Execution == nil) {
			continue
		}
		trusted = true
		if r := rule(o); len(r) > 0 {
			for _, code := range r {
				if !slices.Contains(failed, code) {
					failed = append(failed, code)
				}
			}
		} else {
			supported = true
		}
	}
	switch {
	case !trusted:
		return []ReasonCode{missing}
	case supported && len(failed) > 0:
		return []ReasonCode{ReasonConflictingEvidence}
	}
	return failed
}

func checkRevision(c ClaimRevision, observed string) []ReasonCode {
	if c.Applicability.RepositoryRevision == "" {
		return []ReasonCode{ReasonRevisionUnbound}
	}
	if c.Applicability.RepositoryRevision != observed {
		return []ReasonCode{ReasonRevisionMismatch}
	}
	return nil
}

func checkExecution(c ClaimRevision, o EvidenceObservation) []ReasonCode {
	a := *o.Execution
	if c.Applicability.Environment == "" {
		return []ReasonCode{ReasonEnvironmentUnbound}
	}
	if c.Applicability.Environment != a.Environment {
		return []ReasonCode{ReasonEnvironmentMismatch}
	}
	if r := checkRevision(c, a.WorkspaceRevision); r != nil {
		return r
	}
	if c.Applicability.DirtyDigest != a.DirtyDigest {
		return []ReasonCode{ReasonWorkspaceStateMismatch}
	}
	if strings.TrimSpace(c.Text) != ExecutionStatement(a) {
		if a.ExitCode != 0 {
			return []ReasonCode{ReasonFailureDoesNotProveCause}
		}
		return []ReasonCode{ReasonStatementExceedsObservation}
	}
	return nil
}

// exactVersion is a complete MAJOR.MINOR.PATCH version. npm and Go both treat
// shorter forms such as "1.2" as ranges, so they never prove an install.
var (
	exactVersion = regexp.MustCompile(`^[=v]?\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
	versionToken = regexp.MustCompile(`[=v]?\d+(?:\.\d+)+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?`)
)

// npmDependencyFields are the package.json maps that declare dependencies.
// Other fields (scripts, config, overrides) never declare an installed version.
var npmDependencyFields = []string{"dependencies", "devDependencies", "optionalDependencies", "peerDependencies"}

// DeclaredVersion extracts the declared specifier for name from a manifest or
// lockfile snapshot. JSON content is read structurally (package-lock installed
// entries first, then package.json dependency maps); other content is read as
// go.mod require directives. ok is false when absent.
func DeclaredVersion(content []byte, name string) (string, bool) {
	if json.Valid(content) {
		return declaredJSON(content, name)
	}
	return declaredGoMod(content, name)
}

func declaredJSON(content []byte, name string) (string, bool) {
	var doc map[string]json.RawMessage
	if json.Unmarshal(content, &doc) != nil {
		return "", false
	}
	if raw, ok := doc["packages"]; ok {
		var packages map[string]struct {
			Version *string `json:"version"`
		}
		if json.Unmarshal(raw, &packages) == nil {
			if p, ok := packages["node_modules/"+name]; ok && p.Version != nil {
				return *p.Version, true
			}
		}
	}
	for _, field := range npmDependencyFields {
		raw, ok := doc[field]
		if !ok {
			continue
		}
		var deps map[string]json.RawMessage
		if json.Unmarshal(raw, &deps) != nil {
			continue
		}
		entry, ok := deps[name]
		if !ok {
			continue
		}
		var spec string
		if json.Unmarshal(entry, &spec) == nil {
			return spec, true
		}
		// package-lock v1 nests the installed version in an object.
		var locked struct {
			Version *string `json:"version"`
		}
		if json.Unmarshal(entry, &locked) == nil && locked.Version != nil {
			return *locked.Version, true
		}
	}
	return "", false
}

// declaredGoMod reads only require directives; replace and exclude blocks
// name versions that are not requirements.
func declaredGoMod(content []byte, name string) (string, bool) {
	inRequire := false
	for line := range strings.Lines(string(content)) {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		switch {
		case len(fields) == 0:
			continue
		case inRequire && fields[0] == ")":
			inRequire = false
			continue
		case !inRequire && fields[0] == "require" && len(fields) == 2 && fields[1] == "(":
			inRequire = true
			continue
		case !inRequire && fields[0] == "require":
			fields = fields[1:]
		case !inRequire:
			continue
		}
		if len(fields) == 2 && fields[0] == name {
			return fields[1], true
		}
	}
	return "", false
}

func normalizeVersion(v string) string { return strings.TrimLeft(v, "=v") }

// statesVersion requires the displayed text to name the dependency and to
// state only the claimed version, so a verdict never endorses other text.
func statesVersion(text, name, claimed string) bool {
	if !strings.Contains(text, name) {
		return false
	}
	tokens := versionToken.FindAllString(strings.ReplaceAll(text, name, " "), -1)
	if len(tokens) == 0 {
		return false
	}
	for _, t := range tokens {
		if normalizeVersion(t) != normalizeVersion(claimed) {
			return false
		}
	}
	return true
}

func checkDeclaredVersion(c ClaimRevision, o EvidenceObservation) []ReasonCode {
	if len(c.Applicability.Versions) != 1 {
		return []ReasonCode{ReasonClaimNotStructured}
	}
	if r := checkRevision(c, o.Revision); r != nil {
		return r
	}
	for name, claimed := range c.Applicability.Versions {
		declared, ok := DeclaredVersion(o.Content, name)
		if !ok {
			return []ReasonCode{ReasonVersionNotDeclared}
		}
		if !exactVersion.MatchString(declared) {
			return []ReasonCode{ReasonVersionRangeNotInstalled}
		}
		if normalizeVersion(declared) != normalizeVersion(claimed) {
			return []ReasonCode{ReasonVersionMismatch}
		}
		if !statesVersion(c.Text, name, claimed) {
			return []ReasonCode{ReasonStatementExceedsObservation}
		}
	}
	return nil
}

// GitLocatorPrefix marks evidence that names an approved repository snapshot.
const GitLocatorPrefix = "git:"

// GitLocator renders the canonical locator for a repository snapshot.
func GitLocator(repoID, path string) string { return GitLocatorPrefix + repoID + ":" + path }

// ParseGitLocator splits "git:<repo>:<path>". Paths may contain colons.
func ParseGitLocator(locator string) (string, string, bool) {
	rest, ok := strings.CutPrefix(locator, GitLocatorPrefix)
	if !ok {
		return "", "", false
	}
	repo, path, ok := strings.Cut(rest, ":")
	if !ok || repo == "" || path == "" {
		return "", "", false
	}
	return repo, path, true
}

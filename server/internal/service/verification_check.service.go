package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// SourceReader independently reads an approved repository snapshot. It is
// implemented by the git gateway and never accepts URLs or arbitrary paths.
type SourceReader interface {
	ReadSnapshot(ctx context.Context, repoID, revision, path string) (domain.SourceSnapshot, error)
}

// VerifierPrincipals re-reads the caller's current, unrevoked authority under
// the transaction lock; a caller-supplied principal is never trusted alone.
type VerifierPrincipals interface {
	Snapshot(context.Context, Tx, domain.Principal, domain.Scope) (domain.RetrievalSnapshot, error)
}

// CheckEvidence is the read-only artifact metadata Check needs.
type CheckEvidence interface {
	Artifact(context.Context, Tx, string) (domain.Artifact, string, error)
	Policy(context.Context, Tx, string) (domain.Policy, error)
}

// EvidenceAttestations exposes qualified collector attestations and recorded
// verifications. Attestation returns ErrIncomplete when none exists.
type EvidenceAttestations interface {
	Attestation(context.Context, Tx, string) (domain.ExecutionAttestation, error)
	LoadVerification(context.Context, Tx, string) (domain.Verification, error)
}

// EvidenceChecks wires automatic verification. Without it Check is unavailable.
type EvidenceChecks struct {
	Principals   VerifierPrincipals
	Evidence     CheckEvidence
	Attestations EvidenceAttestations
	Sources      SourceReader
	Objects      ObjectStore
}

func (s *VerificationService) WithEvidenceChecks(c EvidenceChecks) *VerificationService {
	s.checks = &c
	return s
}

type plannedEvidence struct {
	Ref               domain.EvidenceRef
	Artifact          domain.Artifact
	Uploader          string
	Attestation       *domain.ExecutionAttestation
	GitRepo, GitPath  string
	OwnerAuthenticate bool
}

type checkPlan struct {
	State    domain.RevisionState
	Evidence []plannedEvidence
}

func verifierAuthority(ctx context.Context, tx Tx, principals VerifierPrincipals, p domain.Principal, project string) error {
	snap, err := principals.Snapshot(ctx, tx, p, domain.Scope{})
	if err != nil {
		return err
	}
	if snap.Principal.Role != domain.RoleVerifier || p.Role != domain.RoleVerifier {
		return domain.ErrForbidden
	}
	return Authorize(snap.Principal, "verification:write", []string{project})
}

// plan authenticates every evidence reference under the lock. Origin trust is
// derived only from server records: the host owner's actor, a qualified
// collector attestation bound to the exact artifact, or an approved snapshot.
func (s *VerificationService) plan(ctx context.Context, tx Tx, p domain.Principal, revision string) (checkPlan, error) {
	var plan checkPlan
	state, err := s.repo.LoadRevision(ctx, tx, revision)
	if err != nil {
		return plan, err
	}
	c := state.Claim
	if err = verifierAuthority(ctx, tx, s.checks.Principals, p, c.Scope.ProjectID); err != nil {
		return plan, err
	}
	head, err := s.repo.Head(ctx, tx, c.ClaimID)
	if err != nil {
		return plan, err
	}
	if head != revision || c.Scope.TaskID == "" {
		return plan, domain.ErrConflict
	}
	plan.State = state
	if len(c.Evidence) > 50 {
		return plan, domain.ErrBudget
	}
	ownerActor := ""
	if owner, e := s.owners.Owner(ctx, tx); e == nil && owner.ID != "" {
		ownerActor = ownerActorPrefix + TokenDigest(owner.ID)
	}
	policy, err := s.checks.Evidence.Policy(ctx, tx, c.Scope.ProjectID)
	if err != nil {
		return plan, err
	}
	if policy.Paused || policy.Revision == "" {
		return plan, domain.ErrStale
	}
	for _, ref := range c.Evidence {
		a, uploader, e := s.checks.Evidence.Artifact(ctx, tx, ref.ArtifactID)
		if e != nil {
			return plan, e
		}
		owned := ownerActor != "" && uploader == ownerActor
		if a.Scope != c.Scope && (!owned || a.Scope != (domain.Scope{ProjectID: c.Scope.ProjectID})) {
			return plan, domain.ErrForbidden
		}
		if a.Digest != ref.Digest || a.SourceLocator != ref.Locator {
			return plan, domain.ErrConflict
		}
		if a.State != domain.ArtifactReady || !a.ObjectAvailable {
			return plan, domain.ErrIncomplete
		}
		if policy.Revision != a.PolicyRevision && policy.Revision != a.CurrentPolicyRevision {
			return plan, domain.ErrStale
		}
		if e = validateEvidenceSource(policy, a); e != nil {
			return plan, e
		}
		item := plannedEvidence{Ref: ref, Artifact: a, Uploader: uploader, OwnerAuthenticate: owned}
		if repo, path, ok := domain.ParseGitLocator(ref.Locator); ok {
			item.GitRepo, item.GitPath = repo, path
		}
		att, e := s.checks.Attestations.Attestation(ctx, tx, a.ID)
		switch {
		case e == nil:
			// A collector attests only its own upload of these exact bytes, and
			// never the claim author's own submission.
			if att.ArtifactID == a.ID && att.ArtifactDigest == a.Digest && att.CollectorID == uploader && att.CollectorID != c.SubmittedBy {
				item.Attestation = &att
			}
		case !errors.Is(e, domain.ErrIncomplete):
			return plan, e
		}
		plan.Evidence = append(plan.Evidence, item)
	}
	return plan, nil
}

func (s *VerificationService) readObject(ctx context.Context, a domain.Artifact) ([]byte, error) {
	reader, err := s.checks.Objects.Open(ctx, a.ObjectKey)
	if err != nil {
		if errors.Is(err, domain.ErrIncomplete) {
			return nil, err
		}
		return nil, domain.ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(reader, MaxArtifactBytes+1))
	closeErr := reader.Close()
	if err != nil || closeErr != nil {
		return nil, domain.ErrUnavailable
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != a.Bytes || hex.EncodeToString(sum[:]) != a.Digest {
		return nil, domain.ErrIncomplete
	}
	return data, nil
}

// observe performs read-only I/O outside the graph transaction.
func (s *VerificationService) observe(ctx context.Context, plan checkPlan) ([]domain.EvidenceObservation, error) {
	snapshots := map[string]domain.SourceSnapshot{}
	out := make([]domain.EvidenceObservation, 0, len(plan.Evidence))
	for _, item := range plan.Evidence {
		if item.GitRepo != "" {
			key := item.GitRepo + "\x00" + item.Ref.SourceRevision + "\x00" + item.GitPath
			snap, ok := snapshots[key]
			if !ok {
				var err error
				if snap, err = s.checks.Sources.ReadSnapshot(ctx, item.GitRepo, item.Ref.SourceRevision, item.GitPath); err != nil {
					return nil, err
				}
				snapshots[key] = snap
			}
			out = append(out, domain.EvidenceObservation{Origin: snap.Origin, SourceKey: "git:" + snap.Locator + "@" + snap.Revision, Revision: snap.Revision, Content: snap.Bytes, Mismatch: snap.Digest != item.Artifact.Digest})
			continue
		}
		data, err := s.readObject(ctx, item.Artifact)
		if err != nil {
			return nil, err
		}
		o := domain.EvidenceObservation{Content: data, Origin: domain.Origin{ClientID: item.Uploader, Trust: domain.TrustSubmitted}, SourceKey: "submitted:" + item.Uploader}
		switch {
		case item.OwnerAuthenticate:
			o.Origin = domain.Origin{ClientID: item.Uploader, Trust: domain.TrustOwnerAuthenticated, AttestationID: item.Artifact.ID}
			o.SourceKey = "owner:" + item.Artifact.Digest
		case item.Attestation != nil:
			o.Origin = domain.Origin{ClientID: item.Uploader, Trust: domain.TrustCollectorAttested, AttestationID: item.Attestation.ID}
			o.SourceKey = "attested:" + item.Attestation.ID
			o.Execution = item.Attestation
			o.Revision = item.Attestation.WorkspaceRevision
		}
		out = append(out, o)
	}
	return out, nil
}

// supportingTrust records the trust of the observations the kind's rule used.
// Observations of other trusts never vote, so they never label the outcome.
func supportingTrust(d domain.PolicyDecision, kind string) domain.OriginTrust {
	if trust, ok := domain.RuleTrust(kind); ok && d.Outcome == domain.VerificationSupported {
		return trust
	}
	return domain.TrustSubmitted
}

// Check applies the versioned deterministic evidence policy to one candidate
// revision. Only a currently authorized verifier may call it. Supported claims
// become verified; every other outcome stays candidate with reason codes. No
// command from evidence or transcripts is ever executed.
func (s *VerificationService) Check(ctx context.Context, p domain.Principal, revisionID string) (domain.Verification, error) {
	if s.checks == nil {
		return domain.Verification{}, domain.ErrUnavailable
	}
	if domain.ValidateIDs(revisionID) != nil {
		return domain.Verification{}, domain.ErrInvalidInput
	}
	if p.Role != domain.RoleVerifier {
		return domain.Verification{}, domain.ErrForbidden
	}
	var plan checkPlan
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		var e error
		plan, e = s.plan(ctx, tx, p, revisionID)
		return e
	})
	if err != nil {
		return domain.Verification{}, err
	}
	observations, err := s.observe(ctx, plan)
	if err != nil {
		return domain.Verification{}, err
	}
	decision := domain.EvaluateEvidencePolicy(plan.State.Claim, observations)
	digests := make([]string, 0, len(observations))
	for _, o := range observations {
		sum := sha256.Sum256(o.Content)
		digests = append(digests, o.SourceKey+"\x00"+string(o.Origin.Trust)+"\x00"+hex.EncodeToString(sum[:]))
	}
	evidenceDigest := captureID(plan.State.Claim, plan.Evidence, digests)
	v := domain.Verification{ID: captureID("check", revisionID, domain.EvidencePolicyVersion, evidenceDigest), ClaimRevision: revisionID, PolicyRevision: domain.EvidencePolicyVersion, EvidenceDigest: evidenceDigest, Outcome: decision.Outcome, Method: decision.Method, Origin: domain.Origin{ClientID: p.ClientID, Trust: supportingTrust(decision, plan.State.Claim.Kind)}, CheckedAt: time.Now().UTC(), Reasons: decision.Reasons, DistinctOrigins: decision.DistinctOrigins}
	var out domain.Verification
	err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		current, e := s.plan(ctx, tx, p, revisionID)
		if e != nil {
			return e
		}
		if captureID(current) != captureID(plan) {
			return domain.ErrStale
		}
		// A withdrawn revision never replays an earlier supported result.
		if !current.State.Valid && current.State.Status != domain.ClaimCandidate {
			return domain.ErrConflict
		}
		if existing, e := s.checks.Attestations.LoadVerification(ctx, tx, v.ID); e == nil {
			out = existing
			return nil
		} else if !errors.Is(e, domain.ErrIncomplete) {
			return e
		}
		if current.State.Status != domain.ClaimCandidate {
			return domain.ErrConflict
		}
		supported := v.Outcome == domain.VerificationSupported
		status := domain.ClaimCandidate
		if supported {
			status = domain.ClaimVerified
		}
		if e = s.repo.PutVerification(ctx, tx, v, status, supported); e != nil {
			return e
		}
		out = v
		if supported {
			return s.repo.AdvanceRevisionEpoch(ctx, tx)
		}
		return nil
	})
	if err != nil {
		return domain.Verification{}, err
	}
	return out, nil
}

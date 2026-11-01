package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"io"
	"slices"
	"sort"
	"time"
	"unicode/utf8"
)

type ExcerptSelection struct {
	ArtifactID, SourceRevision, SourceDigest, ExpectedExcerptDigest string
	Start, End                                                      int64
}
type AdjudicationDraft struct {
	ProjectID, CandidateRevision, ExpectedCandidateHead, Action, IdempotencyKey string
	TargetID, ExpectedTargetHead, TargetText, TargetKind                        string
	TargetApplicability                                                         domain.Applicability
	EntityIDs                                                                   []string
	Excerpts                                                                    []ExcerptSelection
}
type AdjudicationInput struct {
	AdjudicationDraft
	ExpectedDigest string
}
type AdjudicationPreview struct{ Digest, CandidateHead, TargetHead, PolicyRevision string }
type AdjudicationResult struct {
	Receipt        domain.Receipt
	TargetRevision string `json:",omitempty"`
}
type InvalidationInput struct{ Revision, ExpectedHead, ExpectedDigest, ExpectedStatus, IdempotencyKey string }

type VerificationRepository interface {
	LoadRevision(context.Context, Tx, string) (domain.RevisionState, error)
	Head(context.Context, Tx, string) (string, error)
	LoadDecisionByKey(context.Context, Tx, string, string) (domain.OwnerDecision, error)
	PutVerification(context.Context, Tx, domain.Verification, domain.ClaimStatus, bool) error
	PutDecision(context.Context, Tx, domain.OwnerDecision) error
	AdvanceRevisionEpoch(context.Context, Tx) error
}
type PublicationRepository interface {
	PutTarget(context.Context, Tx, domain.ClaimRevision, string) error
	PutPublication(context.Context, Tx, domain.Publication) error
	PutExcerptLineage(context.Context, Tx, domain.ExcerptLineage) error
	InvalidateSupport(context.Context, Tx, string) error
}

// OwnerEvidence is the host F4 adapter; it proves bytes, never an execution.
type OwnerEvidence interface {
	ReadHost(context.Context, string) (domain.Artifact, []byte, error)
	StageHost(context.Context, ArtifactInput, io.Reader) (domain.Artifact, error)
	CompleteHost(context.Context, string, string) (domain.Receipt, error)
	ReferenceHost(context.Context, domain.EvidenceRef, domain.Scope) (domain.EvidenceRef, error)
}
type VerificationService struct {
	tx           Transactor
	repo         VerificationRepository
	publications PublicationRepository
	owners       HostOwnerRepository
	evidence     EvidenceRepository
	capture      OwnerEvidence
}

func NewVerificationService(tx Transactor, r VerificationRepository, p PublicationRepository, o HostOwnerRepository, e EvidenceRepository, c OwnerEvidence) *VerificationService {
	return &VerificationService{tx, r, p, o, e, c}
}
func validateAdjudication(d AdjudicationDraft) error {
	if domain.ValidateIDs(d.ProjectID, d.CandidateRevision, d.ExpectedCandidateHead, d.IdempotencyKey) != nil || len(d.IdempotencyKey) > 256 || len(d.Excerpts) > 50 || len(d.EntityIDs) > 50 {
		return domain.ErrInvalidInput
	}
	if d.Action == "reject" {
		if d.TargetID != "" || d.ExpectedTargetHead != "" || d.TargetText != "" || d.TargetKind != "" || len(d.Excerpts) > 0 || len(d.EntityIDs) > 0 || captureID(d.TargetApplicability) != captureID(domain.Applicability{}) {
			return domain.ErrInvalidInput
		}
		return nil
	}
	if d.Action != "verify" || domain.ValidateIDs(d.TargetID, d.TargetText, d.TargetKind) != nil || len(d.TargetText) > 16384 || !utf8.ValidString(d.TargetText) || len(d.Excerpts) < 1 {
		return domain.ErrInvalidInput
	}
	seen := map[string]bool{}
	for _, id := range d.EntityIDs {
		if domain.ValidateIDs(id) != nil || seen[id] {
			return domain.ErrInvalidInput
		}
		seen[id] = true
	}
	for _, e := range d.Excerpts {
		if domain.ValidateIDs(e.ArtifactID, e.SourceRevision) != nil || !validDigest(e.SourceDigest) || !validDigest(e.ExpectedExcerptDigest) || e.Start < 0 || e.End <= e.Start || e.End-e.Start > MaxArtifactBytes {
			return domain.ErrInvalidInput
		}
	}
	raw, err := json.Marshal(d)
	if err != nil || len(raw) > 1<<20 {
		return domain.ErrBudget
	}
	return nil
}
func validateDependencies(c domain.ClaimRevision) error {
	if len(c.DependsOn) > 1000 || len(c.Evidence) > 50 {
		return domain.ErrInvalidInput
	}
	seen := map[string]bool{}
	for _, id := range c.DependsOn {
		if id == c.Revision || domain.ValidateIDs(id) != nil || seen[id] {
			return domain.ErrInvalidInput
		}
		seen[id] = true
	}
	return nil
}

// A target may add conditions, but may not erase any supporting condition.
func narrows(target, source domain.Applicability) bool {
	if source.RepositoryRevision != "" && target.RepositoryRevision != source.RepositoryRevision {
		return false
	}
	if source.DirtyDigest != "" && target.DirtyDigest != source.DirtyDigest {
		return false
	}
	if source.Environment != "" && target.Environment != source.Environment {
		return false
	}
	for k, v := range source.Versions {
		if target.Versions[k] != v {
			return false
		}
	}
	if source.ValidFrom != nil && (target.ValidFrom == nil || target.ValidFrom.Before(*source.ValidFrom)) {
		return false
	}
	if source.ValidUntil != nil && (target.ValidUntil == nil || target.ValidUntil.After(*source.ValidUntil)) {
		return false
	}
	return target.ValidFrom == nil || target.ValidUntil == nil || !target.ValidFrom.After(*target.ValidUntil)
}

type adjudicationSnapshot struct {
	OwnerID    string
	Candidate  domain.RevisionState
	TargetHead string
	Policy     domain.Policy
	Support    []domain.RevisionState
	Artifacts  []domain.Artifact
}

func (s *VerificationService) artifact(ctx context.Context, tx Tx, ref domain.EvidenceRef, scope domain.Scope) (domain.Artifact, error) {
	a, _, err := s.evidence.Artifact(ctx, tx, ref.ArtifactID)
	if err != nil {
		return a, err
	}
	if a.Scope != scope || a.Digest != ref.Digest || a.SourceLocator != ref.Locator {
		return a, domain.ErrConflict
	}
	if a.State != domain.ArtifactReady || !a.ObjectAvailable {
		return a, domain.ErrIncomplete
	}
	p, err := s.evidence.Policy(ctx, tx, scope.ProjectID)
	if err != nil {
		return a, err
	}
	if p.Paused || p.Revision == "" || p.Revision != a.PolicyRevision {
		return a, domain.ErrStale
	}
	if err = validateSource(p, a.SourcePath); err != nil {
		return a, err
	}
	return a, nil
}
func (s *VerificationService) snapshot(ctx context.Context, tx Tx, d AdjudicationDraft) (adjudicationSnapshot, error) {
	var snap adjudicationSnapshot
	owner, err := hostOwner(ctx, tx, s.owners)
	if err != nil {
		return snap, err
	}
	snap.OwnerID = owner.OwnerID
	snap.Candidate, err = s.repo.LoadRevision(ctx, tx, d.CandidateRevision)
	if err != nil {
		return snap, err
	}
	c := snap.Candidate.Claim
	if c.Scope.ProjectID != d.ProjectID || c.Scope.TaskID == "" {
		return snap, domain.ErrForbidden
	}
	head, err := s.repo.Head(ctx, tx, c.ClaimID)
	if err != nil {
		return snap, err
	}
	if head != d.ExpectedCandidateHead || head != c.Revision {
		return snap, domain.ErrConflict
	}
	if snap.Candidate.Status != domain.ClaimCandidate {
		return snap, domain.ErrConflict
	}
	snap.Policy, err = s.evidence.Policy(ctx, tx, d.ProjectID)
	if err != nil {
		return snap, err
	}
	if snap.Policy.Paused || snap.Policy.Revision == "" {
		return snap, domain.ErrStale
	}
	if d.Action == "reject" {
		return snap, nil
	}
	if d.TargetID == c.ClaimID {
		return snap, domain.ErrInvalidInput
	}
	snap.TargetHead, err = s.repo.Head(ctx, tx, d.TargetID)
	if err != nil {
		return snap, err
	}
	if snap.TargetHead != d.ExpectedTargetHead {
		return snap, domain.ErrConflict
	}
	// Existing target revisions must themselves be project-scoped, never a task claim.
	if snap.TargetHead != "" {
		previous, e := s.repo.LoadRevision(ctx, tx, snap.TargetHead)
		if e != nil {
			return snap, e
		}
		if previous.Claim.Scope != (domain.Scope{ProjectID: d.ProjectID}) {
			return snap, domain.ErrForbidden
		}
	}
	if len(c.Evidence) == 0 {
		return snap, domain.ErrIncomplete
	}
	visiting := map[string]bool{}
	visited := map[string]bool{}
	var visit func(domain.RevisionState, bool) error
	visit = func(state domain.RevisionState, root bool) error {
		c := state.Claim
		if visiting[c.Revision] {
			return domain.ErrInvalidInput
		}
		if visited[c.Revision] {
			return nil
		}
		if !root && (state.Status != domain.ClaimVerified || !state.Valid) {
			return domain.ErrIncomplete
		}
		if c.Scope.ProjectID != d.ProjectID || !narrows(d.TargetApplicability, c.Applicability) {
			return domain.ErrForbidden
		}
		if err := validateDependencies(c); err != nil {
			return err
		}
		if len(c.Evidence) == 0 {
			return domain.ErrIncomplete
		}
		if len(visited)+len(visiting) >= 1000 {
			return domain.ErrBudget
		}
		visiting[c.Revision] = true
		for _, ref := range c.Evidence {
			a, e := s.artifact(ctx, tx, ref, c.Scope)
			if e != nil {
				return e
			}
			snap.Artifacts = append(snap.Artifacts, a)
		}
		for _, id := range c.DependsOn {
			dep, e := s.repo.LoadRevision(ctx, tx, id)
			if e != nil {
				return e
			}
			if e = visit(dep, false); e != nil {
				return e
			}
		}
		delete(visiting, c.Revision)
		visited[c.Revision] = true
		snap.Support = append(snap.Support, state)
		return nil
	}
	if err = visit(snap.Candidate, true); err != nil {
		return snap, err
	}
	for _, selection := range d.Excerpts {
		found := false
		for _, ref := range c.Evidence {
			if ref.ArtifactID == selection.ArtifactID && ref.Digest == selection.SourceDigest && ref.SourceRevision == selection.SourceRevision {
				found = true
				break
			}
		}
		if !found {
			return snap, domain.ErrConflict
		}
	}
	sort.Slice(snap.Support, func(i, j int) bool { return snap.Support[i].Claim.Revision < snap.Support[j].Claim.Revision })
	sort.Slice(snap.Artifacts, func(i, j int) bool { return snap.Artifacts[i].ID < snap.Artifacts[j].ID })
	return snap, nil
}
func snapshotDigest(d AdjudicationDraft, snap adjudicationSnapshot) (string, error) {
	// ObjectAvailable is omitted from artifact JSON; false observations already fail
	// snapshot validation. Generation and immutable metadata remain bound here.
	data, err := json.Marshal(struct {
		Draft    AdjudicationDraft
		Snapshot adjudicationSnapshot
	}{d, snap})
	if err != nil || len(data) > 1<<20 {
		return "", domain.ErrBudget
	}
	return TokenDigest(string(data)), nil
}
func (s *VerificationService) PreviewHost(ctx context.Context, d AdjudicationDraft) (AdjudicationPreview, error) {
	if err := validateAdjudication(d); err != nil {
		return AdjudicationPreview{}, err
	}
	var out AdjudicationPreview
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		snap, err := s.snapshot(ctx, tx, d)
		if err != nil {
			return err
		}
		out.Digest, err = snapshotDigest(d, snap)
		out.CandidateHead = d.ExpectedCandidateHead
		out.TargetHead = snap.TargetHead
		out.PolicyRevision = snap.Policy.Revision
		return err
	})
	if err != nil {
		return AdjudicationPreview{}, err
	}
	return out, nil
}
func (s *VerificationService) replay(ctx context.Context, tx Tx, owner, key, request string) (domain.OwnerDecision, bool, error) {
	dec, err := s.repo.LoadDecisionByKey(ctx, tx, owner, key)
	if errors.Is(err, domain.ErrIncomplete) {
		return dec, false, nil
	}
	if err != nil {
		return dec, false, err
	}
	if dec.RequestDigest != request {
		return dec, false, domain.ErrConflict
	}
	return dec, true, nil
}
func (s *VerificationService) AdjudicateHost(ctx context.Context, input AdjudicationInput) (AdjudicationResult, error) {
	d := input.AdjudicationDraft
	if err := validateAdjudication(d); err != nil {
		return AdjudicationResult{}, err
	}
	if !validDigest(input.ExpectedDigest) {
		return AdjudicationResult{}, domain.ErrInvalidInput
	}
	var snap adjudicationSnapshot
	var result AdjudicationResult
	replayed := false
	request := captureID(input)
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		owner, e := hostOwner(ctx, tx, s.owners)
		if e != nil {
			return e
		}
		dec, ok, e := s.replay(ctx, tx, owner.OwnerID, d.IdempotencyKey, request)
		if e != nil {
			return e
		}
		if ok {
			result = AdjudicationResult{Receipt: dec.Receipt, TargetRevision: dec.TargetRevision}
			replayed = true
			return nil
		}
		snap, e = s.snapshot(ctx, tx, d)
		if e != nil {
			return e
		}
		digest, e := snapshotDigest(d, snap)
		if e != nil {
			return e
		}
		if digest != input.ExpectedDigest {
			return domain.ErrStale
		}
		return nil
	})
	if err != nil {
		return AdjudicationResult{}, err
	}
	if replayed {
		return result, nil
	}
	decisionID := captureID(snap.OwnerID, d.IdempotencyKey)
	var refs []domain.EvidenceRef
	var lineage []domain.ExcerptLineage
	var prepared []domain.Artifact
	if d.Action == "verify" {
		// Verify all original supporting objects, including sources not excerpted.
		for _, artifact := range snap.Artifacts {
			current, _, e := s.capture.ReadHost(ctx, artifact.ID)
			if e != nil {
				return AdjudicationResult{}, e
			}
			if current.Generation != artifact.Generation || current.ObjectKey != artifact.ObjectKey {
				return AdjudicationResult{}, domain.ErrStale
			}
		}
		for i, selection := range d.Excerpts {
			source, data, e := s.capture.ReadHost(ctx, selection.ArtifactID)
			if e != nil {
				return AdjudicationResult{}, e
			}
			if source.Digest != selection.SourceDigest || selection.End > int64(len(data)) {
				return AdjudicationResult{}, domain.ErrConflict
			}
			excerpt := data[selection.Start:selection.End]
			if !utf8.Valid(excerpt) || TokenDigest(string(excerpt)) != selection.ExpectedExcerptDigest {
				return AdjudicationResult{}, domain.ErrInvalidInput
			}
			locator := "excerpt:" + captureID(decisionID, i, selection.ExpectedExcerptDigest)
			a, e := s.capture.StageHost(ctx, ArtifactInput{Scope: domain.Scope{ProjectID: d.ProjectID}, Digest: selection.ExpectedExcerptDigest, Bytes: int64(len(excerpt)), PolicyRevision: snap.Policy.Revision, SourceLocator: locator, SourcePath: source.SourcePath}, bytes.NewReader(excerpt))
			if e != nil {
				return AdjudicationResult{}, e
			}
			if _, e = s.capture.CompleteHost(ctx, a.ID, a.Digest); e != nil {
				return AdjudicationResult{}, e
			}
			ref, e := s.capture.ReferenceHost(ctx, domain.EvidenceRef{ArtifactID: a.ID, Digest: a.Digest, Locator: locator, SourceRevision: captureID(decisionID, i)}, a.Scope)
			if e != nil {
				return AdjudicationResult{}, e
			}
			refs = append(refs, ref)
			prepared = append(prepared, a)
			var origin domain.Origin
			for _, r := range snap.Candidate.Claim.Evidence {
				if r.ArtifactID == selection.ArtifactID {
					origin = r.Origin
					break
				}
			}
			lineage = append(lineage, domain.ExcerptLineage{ID: captureID(decisionID, i), DecisionID: decisionID, InputDigest: input.ExpectedDigest, SourceArtifactID: source.ID, SourceRevision: selection.SourceRevision, SupportRevision: d.CandidateRevision, SourceDigest: source.Digest, SourceGeneration: source.Generation, Start: selection.Start, End: selection.End, ExcerptArtifactID: a.ID, ExcerptDigest: a.Digest, ExcerptGeneration: a.Generation, SourceOrigin: origin})
		}
	}
	targetRevision := ""
	if d.Action == "verify" {
		targetRevision = newRevision()
	}
	err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		owner, e := hostOwner(ctx, tx, s.owners)
		if e != nil {
			return e
		}
		if owner.OwnerID != snap.OwnerID {
			return domain.ErrForbidden
		}
		dec, ok, e := s.replay(ctx, tx, owner.OwnerID, d.IdempotencyKey, request)
		if e != nil {
			return e
		}
		if ok {
			result = AdjudicationResult{Receipt: dec.Receipt, TargetRevision: dec.TargetRevision}
			return nil
		}
		current, e := s.snapshot(ctx, tx, d)
		if e != nil {
			return e
		}
		digest, e := snapshotDigest(d, current)
		if e != nil {
			return e
		}
		if digest != input.ExpectedDigest {
			return domain.ErrStale
		}
		for i, ref := range refs {
			a, e := s.artifact(ctx, tx, ref, domain.Scope{ProjectID: d.ProjectID})
			if e != nil {
				return e
			}
			if a.Generation != prepared[i].Generation {
				return domain.ErrStale
			}
		}
		outcome := domain.VerificationUnsupported
		status := domain.ClaimRejected
		valid := false
		if d.Action == "verify" {
			outcome = domain.VerificationSupported
			status = domain.ClaimVerified
			valid = true
		}
		verification := domain.Verification{ID: captureID(decisionID, "source"), ClaimRevision: d.CandidateRevision, PolicyRevision: snap.Policy.Revision, EvidenceDigest: input.ExpectedDigest, Outcome: outcome, Method: "owner_adjudicated", Origin: domain.Origin{Trust: domain.TrustOwnerAuthenticated, AttestationID: decisionID, ClientID: owner.OwnerID}, CheckedAt: time.Now().UTC()}
		if e = s.repo.PutVerification(ctx, tx, verification, status, valid); e != nil {
			return e
		}
		if valid {
			target := domain.ClaimRevision{ClaimID: d.TargetID, Revision: targetRevision, Text: d.TargetText, Kind: d.TargetKind, Scope: domain.Scope{ProjectID: d.ProjectID}, EntityIDs: slices.Clone(d.EntityIDs), Evidence: refs, DependsOn: []string{d.CandidateRevision}, Applicability: d.TargetApplicability, Status: domain.ClaimVerified}
			if e = s.publications.PutTarget(ctx, tx, target, d.ExpectedTargetHead); e != nil {
				return e
			}
			verification.ID = captureID(decisionID, "target")
			verification.ClaimRevision = targetRevision
			if e = s.repo.PutVerification(ctx, tx, verification, domain.ClaimVerified, true); e != nil {
				return e
			}
			supports := make([]string, 0, len(snap.Support))
			for _, support := range snap.Support {
				supports = append(supports, support.Claim.Revision)
			}
			if e = s.publications.PutPublication(ctx, tx, domain.Publication{ID: captureID(decisionID, "publication"), ClaimRevision: targetRevision, Revision: newRevision(), SharingScope: "project", SupportRevisions: supports, PolicyRevision: snap.Policy.Revision}); e != nil {
				return e
			}
			for _, pin := range lineage {
				if e = s.publications.PutExcerptLineage(ctx, tx, pin); e != nil {
					return e
				}
			}
		}
		receipt := domain.Receipt{ID: decisionID, State: map[bool]string{true: "published", false: "rejected"}[valid]}
		if e = s.repo.PutDecision(ctx, tx, domain.OwnerDecision{ID: decisionID, OwnerID: owner.OwnerID, Key: d.IdempotencyKey, InputDigest: input.ExpectedDigest, RequestDigest: request, Action: d.Action, CandidateRevision: d.CandidateRevision, TargetRevision: targetRevision, Receipt: receipt}); e != nil {
			return e
		}
		if e = s.repo.AdvanceRevisionEpoch(ctx, tx); e != nil {
			return e
		}
		result = AdjudicationResult{receipt, targetRevision}
		return nil
	})
	if err != nil {
		return AdjudicationResult{}, err
	}
	return result, nil
}
func (s *VerificationService) InvalidateHost(ctx context.Context, input InvalidationInput) (domain.Receipt, error) {
	if domain.ValidateIDs(input.Revision, input.ExpectedHead, input.ExpectedStatus, input.IdempotencyKey) != nil || !validDigest(input.ExpectedDigest) || len(input.IdempotencyKey) > 256 {
		return domain.Receipt{}, domain.ErrInvalidInput
	}
	var receipt domain.Receipt
	request := captureID("invalidate", input)
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		owner, e := hostOwner(ctx, tx, s.owners)
		if e != nil {
			return e
		}
		dec, ok, e := s.replay(ctx, tx, owner.OwnerID, input.IdempotencyKey, request)
		if e != nil {
			return e
		}
		if ok {
			receipt = dec.Receipt
			return nil
		}
		state, e := s.repo.LoadRevision(ctx, tx, input.Revision)
		if e != nil {
			return e
		}
		head, e := s.repo.Head(ctx, tx, state.Claim.ClaimID)
		if e != nil {
			return e
		}
		if head != input.ExpectedHead || string(state.Status) != input.ExpectedStatus || !state.Valid || captureID(state) != input.ExpectedDigest {
			return domain.ErrStale
		}
		if e = s.publications.InvalidateSupport(ctx, tx, input.Revision); e != nil {
			return e
		}
		receipt = domain.Receipt{ID: captureID(owner.OwnerID, input.IdempotencyKey), State: "invalidated"}
		if e = s.repo.PutDecision(ctx, tx, domain.OwnerDecision{ID: receipt.ID, OwnerID: owner.OwnerID, Key: input.IdempotencyKey, InputDigest: input.ExpectedDigest, RequestDigest: request, Action: "invalidate", CandidateRevision: input.Revision, Receipt: receipt}); e != nil {
			return e
		}
		return s.repo.AdvanceRevisionEpoch(ctx, tx)
	})
	if err != nil {
		return domain.Receipt{}, err
	}
	return receipt, nil
}

// PreviewInvalidationHost exposes only host metadata needed for an exact owner
// confirmation. It cannot withdraw anything and never mints an idempotency key.
func (s *VerificationService) PreviewInvalidationHost(ctx context.Context, revision string) (InvalidationInput, error) {
	if domain.ValidateIDs(revision) != nil {
		return InvalidationInput{}, domain.ErrInvalidInput
	}
	var out InvalidationInput
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := hostOwner(ctx, tx, s.owners); err != nil {
			return err
		}
		state, err := s.repo.LoadRevision(ctx, tx, revision)
		if err != nil {
			return err
		}
		if !state.Valid || state.Status != domain.ClaimVerified {
			return domain.ErrConflict
		}
		head, err := s.repo.Head(ctx, tx, state.Claim.ClaimID)
		if err != nil {
			return err
		}
		out = InvalidationInput{Revision: revision, ExpectedHead: head, ExpectedStatus: string(state.Status), ExpectedDigest: captureID(state)}
		return nil
	})
	if err != nil {
		return InvalidationInput{}, err
	}
	return out, nil
}

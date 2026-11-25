package service

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// LessonRepository persists a separate lesson revision and its publication.
// Every method runs inside the caller's OwnerState-locked transaction.
type LessonRepository interface {
	LoadRevision(context.Context, Tx, string) (domain.RevisionState, error)
	Head(context.Context, Tx, string) (string, error)
	PutLesson(context.Context, Tx, domain.ClaimRevision, string) error
	PutVerification(context.Context, Tx, domain.Verification, domain.ClaimStatus, bool) error
	PutPublication(context.Context, Tx, domain.Publication) error
	AdvanceRevisionEpoch(context.Context, Tx) error
}

// LessonService publishes qualified lessons derived from verified support.
// Only an authenticated verifier may invoke it; there is no agent route.
type LessonService struct {
	tx         Transactor
	repo       LessonRepository
	principals VerifierPrincipals
}

func NewLessonService(tx Transactor, r LessonRepository, p VerifierPrincipals) *LessonService {
	return &LessonService{tx: tx, repo: r, principals: p}
}

const (
	maxLessonText            = 4096
	maxLessonCounterexamples = 20
	maxSupportClosure        = 1000
)

// Recognizable credential shapes. Unknown secrets can evade heuristics, so a
// match only ever holds a lesson pending; absence never proves safety.
var secretPattern = regexp.MustCompile(`(?i)(-----BEGIN [A-Z ]*PRIVATE KEY-----|\bAKIA[0-9A-Z]{16}\b|\bgh[pousr]_[A-Za-z0-9]{30,}\b|\bsk-[A-Za-z0-9_-]{20,}\b|\bxox[abpr]-[A-Za-z0-9-]{10,}\b|(password|secret|token)\s*[:=]\s*\S{8,})`)

func validLessonInput(in domain.LessonInput) error {
	if domain.ValidateIDs(in.ProjectID, in.LessonID, in.Text) != nil || len(in.Text) > maxLessonText || !utf8.ValidString(in.Text) {
		return domain.ErrInvalidInput
	}
	if in.SharingScope != domain.SharingProject && in.SharingScope != domain.SharingCrossProject {
		return domain.ErrInvalidInput
	}
	if len(in.SupportRevisions) < 1 || len(in.SupportRevisions) > 50 || len(in.Counterexamples) > maxLessonCounterexamples {
		return domain.ErrInvalidInput
	}
	seen := map[string]bool{}
	for _, id := range in.SupportRevisions {
		if domain.ValidateIDs(id) != nil || seen[id] {
			return domain.ErrInvalidInput
		}
		seen[id] = true
	}
	for _, c := range in.Counterexamples {
		if domain.ValidateIDs(c) != nil || len(c) > 512 || !utf8.ValidString(c) {
			return domain.ErrInvalidInput
		}
	}
	return nil
}

func explicitApplicability(a domain.Applicability) bool {
	return a.Environment != "" || len(a.Versions) > 0 || a.ValidFrom != nil || a.ValidUntil != nil
}

// preservesConditions keeps every support condition. Repository revision and
// dirty state are project-private and checked only for project sharing.
func preservesConditions(lesson, support domain.Applicability, cross bool) []domain.ReasonCode {
	if support.Environment != "" && lesson.Environment != support.Environment {
		return []domain.ReasonCode{domain.ReasonEnvironmentMismatch}
	}
	if cross {
		support.RepositoryRevision, support.DirtyDigest = "", ""
		lesson.RepositoryRevision, lesson.DirtyDigest = "", ""
	}
	if !narrows(lesson, support) {
		return []domain.ReasonCode{domain.ReasonOvergeneralized}
	}
	return nil
}

type lessonReview struct {
	closure    []string
	restricted []string
	rejected   []domain.ReasonCode
	pending    []domain.ReasonCode
}

func addReason(list []domain.ReasonCode, codes ...domain.ReasonCode) []domain.ReasonCode {
	for _, c := range codes {
		if !slices.Contains(list, c) {
			list = append(list, c)
		}
	}
	return list
}

func (s *LessonService) review(ctx context.Context, tx Tx, in domain.LessonInput) (lessonReview, error) {
	var r lessonReview
	cross := in.SharingScope == domain.SharingCrossProject
	if !explicitApplicability(in.Applicability) {
		r.rejected = addReason(r.rejected, domain.ReasonApplicabilityMissing)
	}
	visited := map[string]bool{}
	var visit func(string, bool) error
	visit = func(id string, direct bool) error {
		if visited[id] {
			return nil
		}
		if len(visited) >= maxSupportClosure {
			return domain.ErrBudget
		}
		visited[id] = true
		state, err := s.repo.LoadRevision(ctx, tx, id)
		if err != nil {
			return err
		}
		c := state.Claim
		if c.Scope.ProjectID != in.ProjectID {
			return domain.ErrForbidden
		}
		switch {
		case state.Status == domain.ClaimDisputed && state.Valid:
			r.pending = addReason(r.pending, domain.ReasonDisputedSupport)
		case state.Status != domain.ClaimVerified || !state.Valid:
			r.rejected = addReason(r.rejected, domain.ReasonSupportNotVerified)
		}
		if direct && explicitApplicability(in.Applicability) {
			r.rejected = addReason(r.rejected, preservesConditions(in.Applicability, c.Applicability, cross)...)
		}
		r.restricted = append(r.restricted, c.ClaimID, c.Revision, c.Applicability.RepositoryRevision, c.Applicability.DirtyDigest)
		r.restricted = append(r.restricted, c.EntityIDs...)
		for _, ref := range c.Evidence {
			r.restricted = append(r.restricted, ref.ArtifactID, ref.Locator, ref.SourceRevision)
		}
		r.closure = append(r.closure, id)
		for _, dep := range c.DependsOn {
			if err = visit(dep, false); err != nil {
				return err
			}
		}
		return nil
	}
	for _, id := range in.SupportRevisions {
		if err := visit(id, true); err != nil {
			return r, err
		}
	}
	slices.Sort(r.closure)
	shared := lessonSurface(in)
	if secretPattern.MatchString(shared) {
		r.pending = addReason(r.pending, domain.ReasonPossibleSecret)
	}
	if cross {
		r.restricted = append(r.restricted, in.ProjectID)
		if in.Applicability.RepositoryRevision != "" || in.Applicability.DirtyDigest != "" {
			r.pending = addReason(r.pending, domain.ReasonRestrictedSourceContent)
		}
		for _, token := range r.restricted {
			if len(token) >= 4 && strings.Contains(shared, token) {
				r.pending = addReason(r.pending, domain.ReasonRestrictedSourceContent)
				break
			}
		}
	}
	return r, nil
}

// lessonSurface is every lesson field another project could read.
func lessonSurface(in domain.LessonInput) string {
	parts := append([]string{in.Text, in.Applicability.Environment, in.Applicability.RepositoryRevision}, in.Counterexamples...)
	for k, v := range in.Applicability.Versions {
		parts = append(parts, k, v)
	}
	return strings.Join(parts, "\n")
}

// PublishLesson atomically creates a separate lesson revision, its verification
// and its publication under the OwnerState lock. Support validity, the lesson
// head and authority are all read in the same transaction, so a concurrent
// invalidation either precedes it (refused) or follows it (cascades).
// Rejected or pending outcomes write nothing and return reason codes.
func (s *LessonService) PublishLesson(ctx context.Context, p domain.Principal, in domain.LessonInput) (domain.Publication, error) {
	if err := validLessonInput(in); err != nil {
		return domain.Publication{}, err
	}
	if p.Role != domain.RoleVerifier {
		return domain.Publication{}, domain.ErrForbidden
	}
	var out domain.Publication
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		if err := verifierAuthority(ctx, tx, s.principals, p, in.ProjectID); err != nil {
			return err
		}
		head, err := s.repo.Head(ctx, tx, in.LessonID)
		if err != nil {
			return err
		}
		if head != in.ExpectedHead {
			return domain.ErrConflict
		}
		if head != "" {
			previous, e := s.repo.LoadRevision(ctx, tx, head)
			if e != nil {
				return e
			}
			if previous.Claim.Kind != domain.KindLesson || previous.Claim.Scope != (domain.Scope{ProjectID: in.ProjectID}) {
				return domain.ErrForbidden
			}
		}
		review, err := s.review(ctx, tx, in)
		if err != nil {
			return err
		}
		out = domain.Publication{SharingScope: in.SharingScope}
		switch {
		case len(review.rejected) > 0:
			out.State, out.Reasons = domain.PublicationRejected, append(review.rejected, review.pending...)
			return nil
		case len(review.pending) > 0:
			out.State, out.Reasons = domain.PublicationPending, review.pending
			return nil
		}
		supports := slices.Sorted(slices.Values(in.SupportRevisions))
		lesson := domain.ClaimRevision{ClaimID: in.LessonID, Revision: newRevision(), Text: domain.LessonText(in.Text, in.Counterexamples), Kind: domain.KindLesson, Scope: domain.Scope{ProjectID: in.ProjectID}, DependsOn: supports, Applicability: in.Applicability, Status: domain.ClaimVerified, SubmittedBy: p.ClientID}
		if err = s.repo.PutLesson(ctx, tx, lesson, in.ExpectedHead); err != nil {
			return err
		}
		v := domain.Verification{ID: captureID("lesson", lesson.Revision), ClaimRevision: lesson.Revision, PolicyRevision: domain.EvidencePolicyVersion, EvidenceDigest: captureID(review.closure), Outcome: domain.VerificationSupported, Method: domain.MethodLessonPolicy, Origin: domain.Origin{ClientID: p.ClientID, Trust: domain.TrustSubmitted}, CheckedAt: time.Now().UTC()}
		if err = s.repo.PutVerification(ctx, tx, v, domain.ClaimVerified, true); err != nil {
			return err
		}
		out = domain.Publication{ID: captureID("lesson-publication", lesson.Revision), ClaimRevision: lesson.Revision, SharingScope: in.SharingScope, Revision: newRevision(), SupportRevisions: review.closure, PolicyRevision: domain.EvidencePolicyVersion, State: domain.PublicationPublished}
		if in.SharingScope == domain.SharingCrossProject {
			out.GeneralizedText = lesson.Text
		}
		if err = s.repo.PutPublication(ctx, tx, out); err != nil {
			return err
		}
		return s.repo.AdvanceRevisionEpoch(ctx, tx)
	})
	if err != nil {
		return domain.Publication{}, err
	}
	return out, nil
}

package service

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
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
// Only an authenticated verifier or the persisted host owner (through kb-admin)
// may invoke it; there is no agent route.
type LessonService struct {
	tx         Transactor
	repo       LessonRepository
	principals VerifierPrincipals
	owners     HostOwnerRepository
}

func NewLessonService(tx Transactor, r LessonRepository, p VerifierPrincipals, o HostOwnerRepository) *LessonService {
	return &LessonService{tx: tx, repo: r, principals: p, owners: o}
}

// LessonPreview is the read-only outcome a confirmed publication would
// produce. Digest binds the input, lesson head and every support state.
type LessonPreview struct {
	Digest, ExpectedHead, State string
	Reasons                     []domain.ReasonCode
	SupportRevisions            []string
}

// LessonPublishInput confirms a previewed lesson by its exact digest.
type LessonPublishInput struct {
	domain.LessonInput
	ExpectedDigest string
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
	states     []domain.RevisionState
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
	// Every revision in the support closure carries conditions the lesson
	// depends on, so each is checked exactly once regardless of input order.
	visited := map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
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
		r.states = append(r.states, state)
		switch {
		case state.Status == domain.ClaimDisputed && state.Valid:
			r.pending = addReason(r.pending, domain.ReasonDisputedSupport)
		case state.Status != domain.ClaimVerified || !state.Valid:
			r.rejected = addReason(r.rejected, domain.ReasonSupportNotVerified)
		}
		if explicitApplicability(in.Applicability) {
			r.rejected = addReason(r.rejected, preservesConditions(in.Applicability, c.Applicability, cross)...)
		}
		r.restricted = append(r.restricted, c.ClaimID, c.Revision, c.Applicability.RepositoryRevision, c.Applicability.DirtyDigest)
		r.restricted = append(r.restricted, c.EntityIDs...)
		for _, ref := range c.Evidence {
			r.restricted = append(r.restricted, ref.ArtifactID, ref.Locator, ref.SourceRevision)
		}
		r.closure = append(r.closure, id)
		for _, dep := range c.DependsOn {
			if err = visit(dep); err != nil {
				return err
			}
		}
		return nil
	}
	for _, id := range in.SupportRevisions {
		if err := visit(id); err != nil {
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
		if slices.ContainsFunc(r.restricted, func(token string) bool { return mentions(shared, token) }) {
			r.pending = addReason(r.pending, domain.ReasonRestrictedSourceContent)
		}
	}
	return r, nil
}

// shortIdentifier is the length below which a substring match would flag
// unrelated words, so shorter identifiers are matched only as whole tokens.
const shortIdentifier = 4

func identifierRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-'
}

// mentions reports whether shared text contains a restricted identifier.
// Long identifiers match anywhere; short ones match at token boundaries so a
// standalone private ID is held while words that contain it are not.
func mentions(shared, token string) bool {
	if token == "" {
		return false
	}
	if len(token) >= shortIdentifier {
		return strings.Contains(shared, token)
	}
	for i := 0; ; {
		j := strings.Index(shared[i:], token)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(token)
		before, _ := utf8.DecodeLastRuneInString(shared[:start])
		after, _ := utf8.DecodeRuneInString(shared[end:])
		if (start == 0 || !identifierRune(before)) && (end == len(shared) || !identifierRune(after)) {
			return true
		}
		i = start + 1
	}
}

// lessonSurface is every lesson field another project could read.
func lessonSurface(in domain.LessonInput) string {
	parts := append([]string{in.Text, in.Applicability.Environment, in.Applicability.RepositoryRevision}, in.Counterexamples...)
	for k, v := range in.Applicability.Versions {
		parts = append(parts, k, v)
	}
	return strings.Join(parts, "\n")
}

// lessonAuthority re-reads the caller's authority for project under the lock
// and returns the actor recorded as the lesson's submitter.
type lessonAuthority func(context.Context, Tx, string) (string, error)

type lessonEvaluation struct {
	actor, head, digest string
	review              lessonReview
}

func (e lessonEvaluation) outcome() (string, []domain.ReasonCode) {
	switch {
	case len(e.review.rejected) > 0:
		return domain.PublicationRejected, append(slices.Clone(e.review.rejected), e.review.pending...)
	case len(e.review.pending) > 0:
		return domain.PublicationPending, e.review.pending
	}
	return domain.PublicationPublished, nil
}

func (s *LessonService) evaluate(ctx context.Context, tx Tx, authority lessonAuthority, in domain.LessonInput) (lessonEvaluation, error) {
	var ev lessonEvaluation
	actor, err := authority(ctx, tx, in.ProjectID)
	if err != nil {
		return ev, err
	}
	head, err := s.repo.Head(ctx, tx, in.LessonID)
	if err != nil {
		return ev, err
	}
	if head != in.ExpectedHead {
		return ev, domain.ErrConflict
	}
	if head != "" {
		previous, e := s.repo.LoadRevision(ctx, tx, head)
		if e != nil {
			return ev, e
		}
		if previous.Claim.Kind != domain.KindLesson || previous.Claim.Scope != (domain.Scope{ProjectID: in.ProjectID}) {
			return ev, domain.ErrForbidden
		}
	}
	review, err := s.review(ctx, tx, in)
	if err != nil {
		return ev, err
	}
	ev = lessonEvaluation{actor: actor, head: head, review: review}
	ev.digest = captureID("lesson-preview", in, head, review.states, review.closure, review.rejected, review.pending)
	return ev, nil
}

func (s *LessonService) verifierLessonAuthority(p domain.Principal) lessonAuthority {
	return func(ctx context.Context, tx Tx, project string) (string, error) {
		return p.ClientID, verifierAuthority(ctx, tx, s.principals, p, project)
	}
}

func (s *LessonService) hostLessonAuthority() lessonAuthority {
	return func(ctx context.Context, tx Tx, _ string) (string, error) {
		if s.owners == nil {
			return "", domain.ErrUnavailable
		}
		owner, err := hostOwner(ctx, tx, s.owners)
		return owner.OwnerID, err
	}
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
	return s.publish(ctx, s.verifierLessonAuthority(p), in, "")
}

// PreviewLessonHost reports, without writing, the outcome a host-owner
// publication of in would produce and the digest that confirms it.
func (s *LessonService) PreviewLessonHost(ctx context.Context, in domain.LessonInput) (LessonPreview, error) {
	if err := validLessonInput(in); err != nil {
		return LessonPreview{}, err
	}
	var out LessonPreview
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		ev, err := s.evaluate(ctx, tx, s.hostLessonAuthority(), in)
		if err != nil {
			return err
		}
		out = LessonPreview{Digest: ev.digest, ExpectedHead: ev.head, SupportRevisions: ev.review.closure}
		out.State, out.Reasons = ev.outcome()
		return nil
	})
	if err != nil {
		return LessonPreview{}, err
	}
	return out, nil
}

// PublishLessonHost publishes as the persisted host owner only when the
// current evaluation still matches the previewed digest exactly.
func (s *LessonService) PublishLessonHost(ctx context.Context, in LessonPublishInput) (domain.Publication, error) {
	if err := validLessonInput(in.LessonInput); err != nil {
		return domain.Publication{}, err
	}
	if !validDigest(in.ExpectedDigest) {
		return domain.Publication{}, domain.ErrInvalidInput
	}
	return s.publish(ctx, s.hostLessonAuthority(), in.LessonInput, in.ExpectedDigest)
}

// publish requires expectedDigest to match when it is non-empty.
func (s *LessonService) publish(ctx context.Context, authority lessonAuthority, in domain.LessonInput, expectedDigest string) (domain.Publication, error) {
	var out domain.Publication
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		ev, err := s.evaluate(ctx, tx, authority, in)
		if err != nil {
			return err
		}
		if expectedDigest != "" && ev.digest != expectedDigest {
			return domain.ErrStale
		}
		out = domain.Publication{SharingScope: in.SharingScope}
		if out.State, out.Reasons = ev.outcome(); out.State != domain.PublicationPublished {
			return nil
		}
		review := ev.review
		supports := slices.Sorted(slices.Values(in.SupportRevisions))
		lesson := domain.ClaimRevision{ClaimID: in.LessonID, Revision: newRevision(), Text: domain.LessonText(in.Text, in.Counterexamples), Kind: domain.KindLesson, Scope: domain.Scope{ProjectID: in.ProjectID}, DependsOn: supports, Applicability: in.Applicability, Status: domain.ClaimVerified, SubmittedBy: ev.actor}
		if err = s.repo.PutLesson(ctx, tx, lesson, in.ExpectedHead); err != nil {
			return err
		}
		v := domain.Verification{ID: captureID("lesson", lesson.Revision), ClaimRevision: lesson.Revision, PolicyRevision: domain.EvidencePolicyVersion, EvidenceDigest: captureID(review.closure), Outcome: domain.VerificationSupported, Method: domain.MethodLessonPolicy, Origin: domain.Origin{ClientID: ev.actor, Trust: domain.TrustSubmitted}, CheckedAt: time.Now().UTC()}
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

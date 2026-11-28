package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
)

// DecisionRespondAction is the only owner-challenge action this service redeems.
const DecisionRespondAction = "decision.respond"

const maxDecisionBatch = 100

// DecisionRepository persists decisions, owner judgments, reply receipts, the
// Buzz poll cursor and recovery challenges. Implementations accept the caller's
// transaction and never open their own.
type DecisionRepository interface {
	PutDecision(context.Context, Tx, domain.Decision) error
	Decision(context.Context, Tx, string) (domain.Decision, error)
	UpdateDecision(context.Context, Tx, domain.Decision) error
	ListDecisions(ctx context.Context, tx Tx, undeliveredOnly bool, limit int) ([]domain.Decision, error)
	ReplyReceipt(context.Context, Tx, string) (domain.DecisionReplyReceipt, error)
	PutReplyReceipt(context.Context, Tx, domain.DecisionReplyReceipt) error
	PutJudgment(context.Context, Tx, domain.OwnerJudgment) error
	ListJudgments(ctx context.Context, tx Tx, signer string, sinceAt, afterAt int64, afterID string, limit int) ([]domain.JudgmentRecord, error)
	Cursor(context.Context, Tx) (string, error)
	PutCursor(context.Context, Tx, string) error
	PutChallenge(context.Context, Tx, domain.OwnerChallenge) error
	Challenge(context.Context, Tx, string) (domain.OwnerChallenge, error)
	UpdateChallenge(context.Context, Tx, domain.OwnerChallenge) error
	PutGrant(context.Context, Tx, domain.ActionGrant) error
	GrantByToken(context.Context, Tx, string) (domain.ActionGrant, error)
	UpdateGrant(context.Context, Tx, domain.ActionGrant) error
}

// DecisionRevisions reads candidate revision state and current heads.
type DecisionRevisions interface {
	LoadRevision(context.Context, Tx, string) (domain.RevisionState, error)
	Head(context.Context, Tx, string) (string, error)
}

// DecisionArtifacts reads the current evidence artifact bindings.
type DecisionArtifacts interface {
	Artifact(context.Context, Tx, string) (domain.Artifact, string, error)
}

// BuzzGateway delivers decision posts and returns locally verified replies.
// Send must be idempotent by operation key.
type BuzzGateway interface {
	Send(ctx context.Context, operationKey, threadID, body string) (domain.MessageReceipt, error)
	Poll(ctx context.Context, cursor string) ([]domain.SignedMessage, string, error)
}

// OwnerSignatureVerifier checks a BIP-340 signature over a 32-byte digest.
type OwnerSignatureVerifier interface {
	VerifyDigest(publicKey string, digest [32]byte, signature []byte) error
}

// IngestResult counts replies by outcome. Deferred replies failed transiently;
// the poll cursor is held so they are retried on the next sync.
type IngestResult struct{ Resolved, Rejected, Ignored, Deferred int }

type DecisionService struct {
	tx        Transactor
	repo      DecisionRepository
	revisions DecisionRevisions
	artifacts DecisionArtifacts
	owners    HostOwnerRepository
	buzz      BuzzGateway
	verifier  OwnerSignatureVerifier
	now       func() time.Time
}

// NewDecisionService composes owner decisions. buzz may be nil, in which case
// decisions stay pending until the recovery path or a later delivery resolves them.
func NewDecisionService(tx Transactor, repo DecisionRepository, revisions DecisionRevisions, artifacts DecisionArtifacts, owners HostOwnerRepository, buzz BuzzGateway, verifier OwnerSignatureVerifier, now func() time.Time) *DecisionService {
	if now == nil {
		now = time.Now
	}
	return &DecisionService{tx, repo, revisions, artifacts, owners, buzz, verifier, now}
}

func (s *DecisionService) clock() time.Time { return s.now().UTC() }

func authorizeDecisionRequest(p domain.Principal, project string) error {
	switch p.Role {
	case domain.RoleOwner:
		if p.OwnerID != "" {
			return nil
		}
	case domain.RoleVerifier:
		if slices.Contains(p.ProjectIDs, project) {
			return nil
		}
	}
	return domain.ErrForbidden
}

// currentEvidence binds the candidate revision, its status, and the exact
// generation of every evidence artifact. Any change yields ErrStale.
func (s *DecisionService) currentEvidence(ctx context.Context, tx Tx, revision string) (domain.RevisionState, string, error) {
	state, err := s.revisions.LoadRevision(ctx, tx, revision)
	if err != nil {
		return state, "", err
	}
	head, err := s.revisions.Head(ctx, tx, state.Claim.ClaimID)
	if err != nil {
		return state, "", err
	}
	if head != revision || state.Status != domain.ClaimCandidate {
		return state, "", domain.ErrStale
	}
	if len(state.Claim.Evidence) == 0 || len(state.Claim.Evidence) > 50 {
		return state, "", domain.ErrIncomplete
	}
	type binding struct{ ID, Digest, Generation, State string }
	bindings := make([]binding, 0, len(state.Claim.Evidence))
	for _, ref := range state.Claim.Evidence {
		a, _, e := s.artifacts.Artifact(ctx, tx, ref.ArtifactID)
		if e != nil {
			return state, "", e
		}
		if a.Digest != ref.Digest {
			return state, "", domain.ErrStale
		}
		bindings = append(bindings, binding{a.ID, a.Digest, a.Generation, string(a.State)})
	}
	return state, captureID("kb-decision-evidence/v1", state.Claim, state.Status, state.Valid, bindings), nil
}

// Prepare returns an unsaved decision bound to the current evidence digest.
func (s *DecisionService) Prepare(ctx context.Context, p domain.Principal, candidateRevision, requestedAction string) (domain.Decision, error) {
	if domain.ValidateIDs(candidateRevision, requestedAction) != nil || len(requestedAction) > 64 {
		return domain.Decision{}, domain.ErrInvalidInput
	}
	var out domain.Decision
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		state, digest, err := s.currentEvidence(ctx, tx, candidateRevision)
		if err != nil {
			return err
		}
		if err = authorizeDecisionRequest(p, state.Claim.Scope.ProjectID); err != nil {
			return err
		}
		out = domain.Decision{CandidateRevision: candidateRevision, EvidenceDigest: digest, RequestedAction: requestedAction, ProjectID: state.Claim.Scope.ProjectID}
		return nil
	})
	if err != nil {
		return domain.Decision{}, err
	}
	return out, nil
}

// Request records a pending owner decision and attempts Buzz delivery. A Buzz
// outage never fails the request; the decision stays pending for Deliver or
// owner recovery.
func (s *DecisionService) Request(ctx context.Context, p domain.Principal, d domain.Decision) (domain.Decision, error) {
	if domain.ValidateIDs(d.CandidateRevision, d.RequestedAction) != nil || len(d.RequestedAction) > 64 || !validDigest(d.EvidenceDigest) {
		return domain.Decision{}, domain.ErrInvalidInput
	}
	var out domain.Decision
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		owner, err := hostOwner(ctx, tx, s.owners)
		if err != nil {
			return err
		}
		state, digest, err := s.currentEvidence(ctx, tx, d.CandidateRevision)
		if err != nil {
			return err
		}
		if err = authorizeDecisionRequest(p, state.Claim.Scope.ProjectID); err != nil {
			return err
		}
		if digest != d.EvidenceDigest {
			return domain.ErrStale
		}
		id, found, err := s.currentGeneration(ctx, tx, captureID("kb-decision/v1", owner.OwnerID, d.CandidateRevision, d.EvidenceDigest, d.RequestedAction))
		if err != nil || found != nil {
			if found != nil {
				out = *found
			}
			return err
		}
		now := s.clock()
		out = domain.Decision{ID: id, CandidateRevision: d.CandidateRevision, EvidenceDigest: d.EvidenceDigest, RequestedAction: d.RequestedAction, OwnerID: owner.OwnerID, ExpiresAt: now.Add(domain.DefaultDecisionTTL), State: domain.DecisionPending, ProjectID: state.Claim.Scope.ProjectID, RequestedBy: p.ClientID, CreatedAt: now}
		return s.repo.PutDecision(ctx, tx, out)
	})
	if err != nil {
		return domain.Decision{}, err
	}
	if out.State == domain.DecisionPending && out.ThreadID == "" && s.buzz != nil {
		if delivered, e := s.deliver(ctx, out); e == nil {
			out = delivered
		}
	}
	return out, nil
}

// maxDecisionGenerations bounds the renewal chain walk. Each generation lives
// for DefaultDecisionTTL, so this covers months of repeated requests.
const maxDecisionGenerations = 128

// currentGeneration follows the deterministic renewal chain starting at base.
// A live or resolved generation is returned as-is (requests are idempotent).
// An expired generation, including one still marked pending past ExpiresAt,
// is marked expired and the walk continues to its successor; the first unused
// ID in the chain is returned for a fresh request.
func (s *DecisionService) currentGeneration(ctx context.Context, tx Tx, base string) (string, *domain.Decision, error) {
	id := base
	for range maxDecisionGenerations {
		existing, err := s.repo.Decision(ctx, tx, id)
		if errors.Is(err, domain.ErrIncomplete) {
			return id, nil, nil
		}
		if err != nil {
			return "", nil, err
		}
		if existing.State == domain.DecisionPending && !s.clock().Before(existing.ExpiresAt) {
			existing.State = domain.DecisionExpired
			if err = s.repo.UpdateDecision(ctx, tx, existing); err != nil {
				return "", nil, err
			}
		}
		if existing.State != domain.DecisionExpired {
			return "", &existing, nil
		}
		id = captureID("kb-decision-renewal/v1", existing.ID)
	}
	return "", nil, domain.ErrConflict
}

func decisionBody(d domain.Decision) string {
	// Identifiers only: claim text and evidence content never leave the graph.
	return strings.Join([]string{
		"KB owner decision " + d.ID,
		"Project: " + d.ProjectID,
		"Requested action: " + d.RequestedAction,
		"Candidate revision: " + d.CandidateRevision,
		"Evidence digest: " + d.EvidenceDigest,
		"Expires: " + d.ExpiresAt.UTC().Format(time.RFC3339),
		"",
		"Inspect on the service host: kb-admin decisions. Reply in this thread with exactly one line:",
		"approve " + d.ID + " " + d.CandidateRevision + " " + d.EvidenceDigest,
		"reject " + d.ID + " " + d.CandidateRevision + " " + d.EvidenceDigest,
	}, "\n")
}

// deliver sends one decision post. The gateway reconciles by operation key, so
// a lost acknowledgement never duplicates the post.
func (s *DecisionService) deliver(ctx context.Context, d domain.Decision) (domain.Decision, error) {
	receipt, err := s.buzz.Send(ctx, "kb-decision-"+d.ID, "", decisionBody(d))
	if err != nil {
		return d, err
	}
	if domain.ValidateIDs(receipt.EventID, receipt.ChannelID) != nil {
		return d, domain.ErrUnknownOutcome
	}
	var out domain.Decision
	err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		current, e := s.repo.Decision(ctx, tx, d.ID)
		if e != nil {
			return e
		}
		if current.ThreadID == "" {
			current.ThreadID = receipt.EventID
			current.ChannelID = receipt.ChannelID
			if e = s.repo.UpdateDecision(ctx, tx, current); e != nil {
				return e
			}
		}
		out = current
		return nil
	})
	return out, err
}

// Deliver retries undelivered pending decisions. Failures leave them pending.
func (s *DecisionService) Deliver(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > maxDecisionBatch {
		return 0, domain.ErrInvalidInput
	}
	if s.buzz == nil {
		return 0, domain.ErrUnavailable
	}
	var queue []domain.Decision
	if err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		var e error
		queue, e = s.repo.ListDecisions(ctx, tx, true, limit)
		return e
	}); err != nil {
		return 0, err
	}
	delivered := 0
	var firstErr error
	for _, d := range queue {
		if !s.clock().Before(d.ExpiresAt) {
			continue
		}
		if _, err := s.deliver(ctx, d); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		delivered++
	}
	return delivered, firstErr
}

// Pending lists pending decisions for host-owner inspection and recovery.
func (s *DecisionService) Pending(ctx context.Context, limit int) ([]domain.Decision, error) {
	if limit < 1 || limit > maxDecisionBatch {
		return nil, domain.ErrInvalidInput
	}
	var out []domain.Decision
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		if _, e := hostOwner(ctx, tx, s.owners); e != nil {
			return e
		}
		var e error
		out, e = s.repo.ListDecisions(ctx, tx, false, limit)
		return e
	})
	return out, err
}

// JudgmentsHost lists owner judgments oldest first for review on the host:
// what each signing key approved or rejected, and when. Signer narrows it to
// one public key, such as the retired key that owner-rotate printed; Since to
// judgments recorded on or after a UTC day. CurrentKey marks judgments signed
// by the key enrolled now, so a review after a theft can tell them apart.
func (s *DecisionService) JudgmentsHost(ctx context.Context, q domain.JudgmentQuery) (domain.JudgmentPage, error) {
	if q.Limit == 0 {
		q.Limit = 100
	}
	q.Signer = strings.ToLower(q.Signer)
	if q.Limit < 1 || q.Limit > domain.MaxJudgmentPage || (q.Signer != "" && !validDigest(q.Signer)) || (q.Since != "" && !domain.ValidAccessDay(q.Since)) {
		return domain.JudgmentPage{}, domain.ErrInvalidInput
	}
	sinceAt := int64(0)
	if q.Since != "" {
		day, _ := time.Parse(time.DateOnly, q.Since)
		sinceAt = day.UnixNano()
	}
	afterAt, afterID, err := parseJudgmentCursor(q.After)
	if err != nil {
		return domain.JudgmentPage{}, err
	}
	var rows []domain.JudgmentRecord
	var current string
	err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		owner, e := s.owners.Owner(ctx, tx)
		if e != nil {
			return e
		}
		if owner.ID == "" || owner.PublicKey == "" {
			return domain.ErrForbidden
		}
		current = owner.PublicKey
		// One more than the page shows whether there is a next page.
		rows, e = s.repo.ListJudgments(ctx, tx, q.Signer, sinceAt, afterAt, afterID, q.Limit+1)
		return e
	})
	if err != nil {
		return domain.JudgmentPage{}, err
	}
	page := domain.JudgmentPage{Judgments: []domain.JudgmentRecord{}}
	for _, j := range rows {
		if len(page.Judgments) == q.Limit {
			last := page.Judgments[len(page.Judgments)-1]
			page.Next = strconv.FormatInt(last.RecordedAt.UnixNano(), 10) + "." + last.ID
			break
		}
		j.CurrentKey = strings.ToLower(j.SignerID) == current
		page.Judgments = append(page.Judgments, j)
	}
	return page, nil
}

// parseJudgmentCursor reads a Next cursor: the last judgment's recorded time
// in Unix nanoseconds, a dot, and its ID.
func parseJudgmentCursor(after string) (int64, string, error) {
	if after == "" {
		return -1, "", nil
	}
	at, id, ok := strings.Cut(after, ".")
	n, err := strconv.ParseInt(at, 10, 64)
	if !ok || err != nil || !validDigest(id) {
		return 0, "", domain.ErrInvalidInput
	}
	return n, id, nil
}

type replyChannel int

const (
	replySignedDigest replyChannel = iota
	replyBuzz
	replyRecovery
)

func (c replyChannel) String() string {
	return [...]string{"api", "buzz", "recovery"}[c]
}

type replyBinding struct {
	channel replyChannel
	message *domain.SignedMessage
}

// Respond resolves a decision from an owner reply signed (BIP-340) over
// domain.DecisionReplyDigest. Caller credentials carry no authority here.
func (s *DecisionService) Respond(ctx context.Context, reply domain.DecisionReply) (domain.Decision, error) {
	return s.resolve(ctx, reply, replyBinding{channel: replySignedDigest})
}

func parseReplyLine(content string) (action, id, revision, digest string, ok bool) {
	line := strings.TrimSpace(content)
	if strings.ContainsAny(line, "\r\n") || len(line) > 512 {
		return "", "", "", "", false
	}
	fields := strings.Fields(line)
	if len(fields) != 4 {
		return "", "", "", "", false
	}
	return strings.ToLower(fields[0]), fields[1], fields[2], fields[3], true
}

// Ingest polls Buzz for replies. Only messages from the enrolled owner that
// parse as a decision line are considered; others are ignored without effect.
func (s *DecisionService) Ingest(ctx context.Context) (IngestResult, error) {
	var result IngestResult
	if s.buzz == nil {
		return result, domain.ErrUnavailable
	}
	var cursor string
	var owner OwnerIdentity
	if err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		var e error
		if owner, e = s.owners.Owner(ctx, tx); e != nil {
			return e
		}
		if owner.PublicKey == "" {
			return domain.ErrForbidden
		}
		cursor, e = s.repo.Cursor(ctx, tx)
		return e
	}); err != nil {
		return result, err
	}
	messages, next, err := s.buzz.Poll(ctx, cursor)
	if err != nil {
		return result, err
	}
	var transient error
	for i := range messages {
		msg := messages[i]
		action, id, revision, digest, ok := parseReplyLine(msg.Content)
		if msg.PubKey != owner.PublicKey || !ok {
			result.Ignored++
			continue
		}
		reply := domain.DecisionReply{DecisionID: id, CandidateRevision: revision, EvidenceDigest: digest, Action: action, EventID: msg.EventID, SignerID: msg.PubKey, Signature: msg.Signature}
		if _, e := s.resolve(ctx, reply, replyBinding{channel: replyBuzz, message: &msg}); e != nil {
			if !permanentReplyRejection(e) {
				// Keep resolving the rest of the batch (each reply is
				// idempotent by event ID) but hold the cursor so this reply
				// is polled again.
				result.Deferred++
				if transient == nil {
					transient = e
				}
				continue
			}
			result.Rejected++
			continue
		}
		result.Resolved++
	}
	if transient != nil {
		return result, transient
	}
	if next != "" && next != cursor {
		if err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error { return s.repo.PutCursor(ctx, tx, next) }); err != nil {
			return result, err
		}
	}
	return result, nil
}

// permanentReplyRejection reports whether resolving a reply failed for a
// reason that cannot change on retry (validation, authority, binding, state).
// Anything else (graph, transaction or context failure) is transient.
func permanentReplyRejection(err error) bool {
	for _, permanent := range []error{domain.ErrInvalidInput, domain.ErrUnauthorized, domain.ErrForbidden, domain.ErrConflict, domain.ErrStale, domain.ErrIncomplete} {
		if errors.Is(err, permanent) {
			return true
		}
	}
	return false
}

func (s *DecisionService) resolve(ctx context.Context, reply domain.DecisionReply, b replyBinding) (domain.Decision, error) {
	if domain.ValidateIDs(reply.DecisionID, reply.CandidateRevision, reply.EvidenceDigest, reply.Action, reply.EventID, reply.SignerID) != nil || len(reply.EventID) > 128 || len(reply.Signature) == 0 || len(reply.Signature) > 512 {
		return domain.Decision{}, domain.ErrInvalidInput
	}
	var out domain.Decision
	var terminal error
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		var e error
		out, terminal, e = s.resolveTx(ctx, tx, reply, b)
		return e
	})
	if err != nil {
		return domain.Decision{}, err
	}
	if terminal != nil {
		return out, terminal
	}
	return out, nil
}

// resolveTx returns (decision, terminal, err). A terminal error is reported to
// the caller after its state change (expired/stale) commits; err aborts.
func (s *DecisionService) resolveTx(ctx context.Context, tx Tx, reply domain.DecisionReply, b replyBinding) (domain.Decision, error, error) {
	replyDigest := captureID("kb-decision-reply-receipt/v1", reply.DecisionID, reply.CandidateRevision, reply.EvidenceDigest, reply.Action, reply.EventID, strings.ToLower(reply.SignerID), b.channel.String())
	if prior, err := s.repo.ReplyReceipt(ctx, tx, reply.EventID); err == nil {
		if prior.ReplyDigest != replyDigest {
			return domain.Decision{}, nil, domain.ErrConflict
		}
		d, e := s.repo.Decision(ctx, tx, prior.DecisionID)
		return d, nil, e
	} else if !errors.Is(err, domain.ErrIncomplete) {
		return domain.Decision{}, nil, err
	}
	owner, err := s.owners.Owner(ctx, tx)
	if err != nil {
		return domain.Decision{}, nil, err
	}
	if owner.ID == "" || owner.PublicKey == "" || strings.ToLower(reply.SignerID) != owner.PublicKey {
		return domain.Decision{}, nil, domain.ErrForbidden
	}
	switch b.channel {
	case replySignedDigest:
		if s.verifier == nil || s.verifier.VerifyDigest(owner.PublicKey, domain.DecisionReplyDigest(reply), reply.Signature) != nil {
			return domain.Decision{}, nil, domain.ErrUnauthorized
		}
	case replyBuzz:
		// The gateway verified event ID and signature locally; bind the author too.
		if b.message == nil || b.message.PubKey != owner.PublicKey || b.message.EventID != reply.EventID {
			return domain.Decision{}, nil, domain.ErrUnauthorized
		}
	case replyRecovery:
		// Authority came from a redeemed one-use owner grant in this transaction.
	}
	d, err := s.repo.Decision(ctx, tx, reply.DecisionID)
	if err != nil {
		return domain.Decision{}, nil, err
	}
	if b.channel == replyBuzz && (d.ThreadID == "" || b.message.ChannelID != d.ChannelID || b.message.ThreadRootID != d.ThreadID) {
		return domain.Decision{}, nil, domain.ErrForbidden
	}
	if d.State != domain.DecisionPending {
		return domain.Decision{}, nil, domain.ErrConflict
	}
	if reply.CandidateRevision != d.CandidateRevision || reply.EvidenceDigest != d.EvidenceDigest {
		return domain.Decision{}, nil, domain.ErrStale
	}
	state := map[string]domain.DecisionState{domain.DecisionApprove: domain.DecisionAccepted, domain.DecisionReject: domain.DecisionRejected}[reply.Action]
	if state == "" {
		return domain.Decision{}, nil, domain.ErrInvalidInput
	}
	now := s.clock()
	if !now.Before(d.ExpiresAt) {
		d.State = domain.DecisionExpired
		return d, domain.ErrStale, s.repo.UpdateDecision(ctx, tx, d)
	}
	// Recheck the exact evidence inside the resolving transaction: an approval
	// of one revision never approves a later one.
	_, digest, err := s.currentEvidence(ctx, tx, d.CandidateRevision)
	if err != nil && !errors.Is(err, domain.ErrStale) {
		return domain.Decision{}, nil, err
	}
	if err != nil || digest != d.EvidenceDigest {
		d.State = domain.DecisionStale
		return d, domain.ErrStale, s.repo.UpdateDecision(ctx, tx, d)
	}
	judgment := domain.OwnerJudgment{ID: captureID("kb-owner-judgment/v1", d.ID, reply.EventID), DecisionID: d.ID, CandidateRevision: d.CandidateRevision, EvidenceDigest: d.EvidenceDigest, Action: reply.Action, OwnerID: owner.ID, SignerID: owner.PublicKey, EventID: reply.EventID, Channel: b.channel.String(), RecordedAt: now}
	if err = s.repo.PutJudgment(ctx, tx, judgment); err != nil {
		return domain.Decision{}, nil, err
	}
	d.State = state
	d.ResolvedByEvent = reply.EventID
	if err = s.repo.UpdateDecision(ctx, tx, d); err != nil {
		return domain.Decision{}, nil, err
	}
	if err = s.repo.PutReplyReceipt(ctx, tx, domain.DecisionReplyReceipt{EventID: reply.EventID, DecisionID: d.ID, ReplyDigest: replyDigest, State: state}); err != nil {
		return domain.Decision{}, nil, err
	}
	return d, nil, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", domain.ErrUnavailable
	}
	return hex.EncodeToString(b), nil
}

// CreateOwnerChallenge starts host-side owner recovery for one exact action.
// The challenge carries no bearer and authorizes nothing by itself.
func (s *DecisionService) CreateOwnerChallenge(ctx context.Context, requestedAction, digest string) (domain.OwnerChallenge, error) {
	if requestedAction != DecisionRespondAction || !validDigest(digest) {
		return domain.OwnerChallenge{}, domain.ErrInvalidInput
	}
	id, err := randomHex(16)
	if err != nil {
		return domain.OwnerChallenge{}, err
	}
	nonce, err := randomHex(32)
	if err != nil {
		return domain.OwnerChallenge{}, err
	}
	c := domain.OwnerChallenge{ID: id, Nonce: nonce, RequestedAction: requestedAction, ActionDigest: digest, ExpiresAt: s.clock().Add(domain.DefaultOwnerChallengeTTL)}
	err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		if _, e := hostOwner(ctx, tx, s.owners); e != nil {
			return e
		}
		return s.repo.PutChallenge(ctx, tx, c)
	})
	if err != nil {
		return domain.OwnerChallenge{}, err
	}
	return c, nil
}

// ApproveOwnerChallenge verifies the enrolled owner's signature over the
// challenge and mints a one-use grant. Only the token digest is persisted.
func (s *DecisionService) ApproveOwnerChallenge(ctx context.Context, challengeID string, signature []byte) (domain.ActionGrant, string, error) {
	if domain.ValidateIDs(challengeID) != nil || len(signature) == 0 || len(signature) > 512 {
		return domain.ActionGrant{}, "", domain.ErrInvalidInput
	}
	token, err := randomHex(32)
	if err != nil {
		return domain.ActionGrant{}, "", err
	}
	var grant domain.ActionGrant
	err = s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		owner, e := s.owners.Owner(ctx, tx)
		if e != nil {
			return e
		}
		if owner.ID == "" || owner.PublicKey == "" {
			return domain.ErrForbidden
		}
		c, e := s.repo.Challenge(ctx, tx, challengeID)
		if e != nil {
			return e
		}
		if c.ApprovedAt != nil {
			return domain.ErrConflict
		}
		now := s.clock()
		if !now.Before(c.ExpiresAt) {
			return domain.ErrStale
		}
		if s.verifier == nil || s.verifier.VerifyDigest(owner.PublicKey, c.SigningDigest(), signature) != nil {
			return domain.ErrForbidden
		}
		c.ApprovedAt = &now
		if e = s.repo.UpdateChallenge(ctx, tx, c); e != nil {
			return e
		}
		grant = domain.ActionGrant{ID: captureID("kb-action-grant/v1", c.ID), ChallengeID: c.ID, RequestedAction: c.RequestedAction, ActionDigest: c.ActionDigest, TokenDigest: TokenDigest(token), ExpiresAt: c.ExpiresAt}
		return s.repo.PutGrant(ctx, tx, grant)
	})
	if err != nil {
		return domain.ActionGrant{}, "", err
	}
	return grant, token, nil
}

// CreateDecisionChallenge binds a recovery challenge to one action on the
// exact revision and evidence digest of a pending decision.
func (s *DecisionService) CreateDecisionChallenge(ctx context.Context, decisionID, action string) (domain.OwnerChallenge, error) {
	if domain.ValidateIDs(decisionID) != nil || (action != domain.DecisionApprove && action != domain.DecisionReject) {
		return domain.OwnerChallenge{}, domain.ErrInvalidInput
	}
	var d domain.Decision
	if err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		var e error
		d, e = s.repo.Decision(ctx, tx, decisionID)
		return e
	}); err != nil {
		return domain.OwnerChallenge{}, err
	}
	if d.State != domain.DecisionPending {
		return domain.OwnerChallenge{}, domain.ErrConflict
	}
	return s.CreateOwnerChallenge(ctx, DecisionRespondAction, domain.DecisionActionDigest(d.ID, action, d.CandidateRevision, d.EvidenceDigest))
}

// RespondWithGrant redeems a one-use owner grant for exactly one decision
// action, then resolves it through the same checks as any owner reply.
func (s *DecisionService) RespondWithGrant(ctx context.Context, token, decisionID, action string) (domain.Decision, error) {
	if domain.ValidateIDs(token, decisionID, action) != nil || len(token) > 128 {
		return domain.Decision{}, domain.ErrInvalidInput
	}
	var out domain.Decision
	var terminal error
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		grant, e := s.repo.GrantByToken(ctx, tx, TokenDigest(token))
		if errors.Is(e, domain.ErrIncomplete) {
			return domain.ErrUnauthorized
		}
		if e != nil {
			return e
		}
		if grant.ConsumedAt != nil {
			return domain.ErrConflict
		}
		now := s.clock()
		if !now.Before(grant.ExpiresAt) {
			return domain.ErrStale
		}
		d, e := s.repo.Decision(ctx, tx, decisionID)
		if e != nil {
			return e
		}
		if grant.RequestedAction != DecisionRespondAction || grant.ActionDigest != domain.DecisionActionDigest(d.ID, action, d.CandidateRevision, d.EvidenceDigest) {
			return domain.ErrForbidden
		}
		owner, e := s.owners.Owner(ctx, tx)
		if e != nil {
			return e
		}
		grant.ConsumedAt = &now
		if e = s.repo.UpdateGrant(ctx, tx, grant); e != nil {
			return e
		}
		reply := domain.DecisionReply{DecisionID: d.ID, CandidateRevision: d.CandidateRevision, EvidenceDigest: d.EvidenceDigest, Action: action, EventID: "grant-" + grant.ID[:32], SignerID: owner.PublicKey, Signature: []byte("owner-grant")}
		out, terminal, e = s.resolveTx(ctx, tx, reply, replyBinding{channel: replyRecovery})
		return e
	})
	if err != nil {
		return domain.Decision{}, err
	}
	if terminal != nil {
		return out, terminal
	}
	return out, nil
}

func (s *DecisionService) hostPrincipal(ctx context.Context) (domain.Principal, error) {
	var p domain.Principal
	err := s.tx.Within(ctx, func(ctx context.Context, tx Tx) error {
		var e error
		p, e = hostOwner(ctx, tx, s.owners)
		return e
	})
	return p, err
}

// PrepareHost previews a decision request on behalf of the persisted host
// owner. It is read-only and returns the evidence digest to confirm.
func (s *DecisionService) PrepareHost(ctx context.Context, candidateRevision, requestedAction string) (domain.Decision, error) {
	p, err := s.hostPrincipal(ctx)
	if err != nil {
		return domain.Decision{}, err
	}
	return s.Prepare(ctx, p, candidateRevision, requestedAction)
}

// RequestHost records a decision on behalf of the persisted host owner. The
// draft's evidence digest must still match the current evidence.
func (s *DecisionService) RequestHost(ctx context.Context, d domain.Decision) (domain.Decision, error) {
	p, err := s.hostPrincipal(ctx)
	if err != nil {
		return domain.Decision{}, err
	}
	return s.Request(ctx, p, d)
}

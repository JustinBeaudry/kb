package service

import (
	"context"
	"encoding/hex"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	decisionOwnerKey = "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"
	decisionAgentKey = "c6047f9441ed7d6d3045406e95c07cd85c778e4b8cef3ca7abac09b95c709ee5"
	decisionChannel  = "6f1c7c1e-1d5b-4c55-9d43-0c1f0b6a2f10"
)

// fakeSigner is the gateway-boundary signature fake: a signature is valid only
// for the exact key and digest it names. Real BIP-340 is covered in gateway tests.
func fakeSign(key string, digest [32]byte) []byte {
	return []byte("sig:" + key + ":" + hex.EncodeToString(digest[:]))
}

type fakeVerifier struct{}

func (fakeVerifier) VerifyDigest(key string, digest [32]byte, sig []byte) error {
	if string(sig) != string(fakeSign(key, digest)) {
		return domain.ErrUnauthorized
	}
	return nil
}

type decisionRepoFake struct {
	decisions  map[string]domain.Decision
	receipts   map[string]domain.DecisionReplyReceipt
	judgments  []domain.OwnerJudgment
	challenges map[string]domain.OwnerChallenge
	grants     map[string]domain.ActionGrant
	cursor     string
	// judgmentErr simulates a transient graph failure while recording a judgment.
	judgmentErr error
}

func newDecisionRepoFake() *decisionRepoFake {
	return &decisionRepoFake{decisions: map[string]domain.Decision{}, receipts: map[string]domain.DecisionReplyReceipt{}, challenges: map[string]domain.OwnerChallenge{}, grants: map[string]domain.ActionGrant{}}
}
func (f *decisionRepoFake) PutDecision(_ context.Context, _ Tx, d domain.Decision) error {
	if _, ok := f.decisions[d.ID]; ok {
		return domain.ErrConflict
	}
	f.decisions[d.ID] = d
	return nil
}
func (f *decisionRepoFake) Decision(_ context.Context, _ Tx, id string) (domain.Decision, error) {
	d, ok := f.decisions[id]
	if !ok {
		return d, domain.ErrIncomplete
	}
	return d, nil
}
func (f *decisionRepoFake) UpdateDecision(_ context.Context, _ Tx, d domain.Decision) error {
	if _, ok := f.decisions[d.ID]; !ok {
		return domain.ErrIncomplete
	}
	f.decisions[d.ID] = d
	return nil
}
func (f *decisionRepoFake) ListDecisions(_ context.Context, _ Tx, undeliveredOnly bool, limit int) ([]domain.Decision, error) {
	var out []domain.Decision
	for _, d := range f.decisions {
		if d.State == domain.DecisionPending && (!undeliveredOnly || d.ThreadID == "") {
			out = append(out, d)
		}
	}
	slices.SortFunc(out, func(a, b domain.Decision) int { return strings.Compare(a.ID, b.ID) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (f *decisionRepoFake) ReplyReceipt(_ context.Context, _ Tx, id string) (domain.DecisionReplyReceipt, error) {
	r, ok := f.receipts[id]
	if !ok {
		return r, domain.ErrIncomplete
	}
	return r, nil
}
func (f *decisionRepoFake) PutReplyReceipt(_ context.Context, _ Tx, r domain.DecisionReplyReceipt) error {
	f.receipts[r.EventID] = r
	return nil
}
func (f *decisionRepoFake) PutJudgment(_ context.Context, _ Tx, j domain.OwnerJudgment) error {
	if f.judgmentErr != nil {
		return f.judgmentErr
	}
	f.judgments = append(f.judgments, j)
	return nil
}
func (f *decisionRepoFake) Cursor(context.Context, Tx) (string, error) { return f.cursor, nil }
func (f *decisionRepoFake) PutCursor(_ context.Context, _ Tx, c string) error {
	f.cursor = c
	return nil
}
func (f *decisionRepoFake) PutChallenge(_ context.Context, _ Tx, c domain.OwnerChallenge) error {
	f.challenges[c.ID] = c
	return nil
}
func (f *decisionRepoFake) Challenge(_ context.Context, _ Tx, id string) (domain.OwnerChallenge, error) {
	c, ok := f.challenges[id]
	if !ok {
		return c, domain.ErrIncomplete
	}
	return c, nil
}
func (f *decisionRepoFake) UpdateChallenge(_ context.Context, _ Tx, c domain.OwnerChallenge) error {
	f.challenges[c.ID] = c
	return nil
}
func (f *decisionRepoFake) PutGrant(_ context.Context, _ Tx, g domain.ActionGrant) error {
	f.grants[g.TokenDigest] = g
	return nil
}
func (f *decisionRepoFake) GrantByToken(_ context.Context, _ Tx, digest string) (domain.ActionGrant, error) {
	g, ok := f.grants[digest]
	if !ok {
		return g, domain.ErrIncomplete
	}
	return g, nil
}
func (f *decisionRepoFake) UpdateGrant(_ context.Context, _ Tx, g domain.ActionGrant) error {
	f.grants[g.TokenDigest] = g
	return nil
}

type decisionGraphFake struct {
	states    map[string]domain.RevisionState
	heads     map[string]string
	artifacts map[string]domain.Artifact
	owner     domain.OwnerIdentity
}

func (f *decisionGraphFake) LoadRevision(_ context.Context, _ Tx, id string) (domain.RevisionState, error) {
	s, ok := f.states[id]
	if !ok {
		return s, domain.ErrIncomplete
	}
	return s, nil
}
func (f *decisionGraphFake) Head(_ context.Context, _ Tx, id string) (string, error) {
	return f.heads[id], nil
}
func (f *decisionGraphFake) Artifact(_ context.Context, _ Tx, id string) (domain.Artifact, string, error) {
	a, ok := f.artifacts[id]
	if !ok {
		return a, "", domain.ErrIncomplete
	}
	return a, "collector", nil
}
func (f *decisionGraphFake) Owner(context.Context, Tx) (OwnerIdentity, error) { return f.owner, nil }

// buzzFake is a gateway-boundary relay: it stores posts, can be taken down,
// and serves owner replies queued by the test.
type buzzFake struct {
	down    bool
	posts   []domain.MessageReceipt
	bodies  []string
	inbox   []domain.SignedMessage
	polled  []string
	nextSeq int
}

func (b *buzzFake) Send(_ context.Context, key, thread, body string) (domain.MessageReceipt, error) {
	if b.down {
		return domain.MessageReceipt{}, domain.ErrUnavailable
	}
	for _, p := range b.posts {
		if p.OperationKey == key {
			p.Reconciled = true
			return p, nil
		}
	}
	b.nextSeq++
	id := strings.Repeat(string(rune('a'+b.nextSeq)), 64)
	r := domain.MessageReceipt{OperationKey: key, EventID: id, ThreadID: id, ChannelID: decisionChannel}
	if thread != "" {
		r.ThreadID = thread
	}
	b.posts = append(b.posts, r)
	b.bodies = append(b.bodies, body)
	return r, nil
}
func (b *buzzFake) Poll(_ context.Context, cursor string) ([]domain.SignedMessage, string, error) {
	b.polled = append(b.polled, cursor)
	if b.down {
		return nil, "", domain.ErrUnavailable
	}
	out := b.inbox
	b.inbox = nil
	return out, "cursor-" + time.Now().Format("150405.000000000"), nil
}

type decisionHarness struct {
	svc   *DecisionService
	repo  *decisionRepoFake
	graph *decisionGraphFake
	buzz  *buzzFake
	now   time.Time
}

func newDecisionHarness(t *testing.T) *decisionHarness {
	t.Helper()
	h := &decisionHarness{repo: newDecisionRepoFake(), buzz: &buzzFake{}, now: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	candidate := domain.ClaimRevision{ClaimID: "claim", Revision: "rev-1", Kind: "finding", Text: "private claim text", Scope: domain.Scope{ProjectID: "demo", TaskID: "task"}, Evidence: []domain.EvidenceRef{{ArtifactID: "artifact", Digest: TokenDigest("bytes"), Locator: "file"}}, Status: domain.ClaimCandidate}
	h.graph = &decisionGraphFake{states: map[string]domain.RevisionState{"rev-1": {Claim: candidate, Status: domain.ClaimCandidate}}, heads: map[string]string{"claim": "rev-1"}, artifacts: map[string]domain.Artifact{"artifact": {ID: "artifact", Digest: TokenDigest("bytes"), Generation: "g1", State: domain.ArtifactReady}}, owner: domain.OwnerIdentity{ID: "owner", PublicKey: decisionOwnerKey}}
	h.svc = NewDecisionService(directTx{}, h.repo, h.graph, h.graph, h.graph, h.buzz, fakeVerifier{}, func() time.Time { return h.now })
	return h
}

var verifierPrincipal = domain.Principal{ClientID: "verifier", Role: domain.RoleVerifier, ProjectIDs: []string{"demo"}}

func (h *decisionHarness) request(t *testing.T) domain.Decision {
	t.Helper()
	draft, err := h.svc.Prepare(t.Context(), verifierPrincipal, "rev-1", "publish")
	require.NoError(t, err)
	d, err := h.svc.Request(t.Context(), verifierPrincipal, draft)
	require.NoError(t, err)
	return d
}
func signedReply(d domain.Decision, action, event, key string) domain.DecisionReply {
	r := domain.DecisionReply{DecisionID: d.ID, CandidateRevision: d.CandidateRevision, EvidenceDigest: d.EvidenceDigest, Action: action, EventID: event, SignerID: key}
	r.Signature = fakeSign(key, domain.DecisionReplyDigest(r))
	return r
}
func (h *decisionHarness) ownerMessage(d domain.Decision, content, event string) domain.SignedMessage {
	return domain.SignedMessage{EventID: event, PubKey: decisionOwnerKey, ChannelID: d.ChannelID, ThreadRootID: d.ThreadID, ParentID: d.ThreadID, Content: content, Kind: 9, CreatedAt: h.now, Signature: []byte("verified-by-gateway")}
}

func TestDecisionReplyValidation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mutate    func(*decisionHarness, domain.Decision) domain.DecisionReply
		wantErr   error
		wantState domain.DecisionState
	}{
		{"owner approval accepted", func(_ *decisionHarness, d domain.Decision) domain.DecisionReply {
			return signedReply(d, "approve", "evt-ok", decisionOwnerKey)
		}, nil, domain.DecisionAccepted},
		{"owner rejection recorded", func(_ *decisionHarness, d domain.Decision) domain.DecisionReply {
			return signedReply(d, "reject", "evt-no", decisionOwnerKey)
		}, nil, domain.DecisionRejected},
		{"wrong key", func(_ *decisionHarness, d domain.Decision) domain.DecisionReply {
			return signedReply(d, "approve", "evt-agent", decisionAgentKey)
		}, domain.ErrForbidden, domain.DecisionPending},
		{"bad signature", func(_ *decisionHarness, d domain.Decision) domain.DecisionReply {
			r := signedReply(d, "approve", "evt-bad", decisionOwnerKey)
			r.Signature = fakeSign(decisionOwnerKey, [32]byte{1})
			return r
		}, domain.ErrUnauthorized, domain.DecisionPending},
		{"signature bound to another action", func(_ *decisionHarness, d domain.Decision) domain.DecisionReply {
			r := signedReply(d, "reject", "evt-swap", decisionOwnerKey)
			r.Action = "approve"
			return r
		}, domain.ErrUnauthorized, domain.DecisionPending},
		{"unknown action", func(_ *decisionHarness, d domain.Decision) domain.DecisionReply {
			return signedReply(d, "publish-everything", "evt-act", decisionOwnerKey)
		}, domain.ErrInvalidInput, domain.DecisionPending},
		{"expired", func(h *decisionHarness, d domain.Decision) domain.DecisionReply {
			h.now = d.ExpiresAt.Add(time.Second)
			return signedReply(d, "approve", "evt-late", decisionOwnerKey)
		}, domain.ErrStale, domain.DecisionExpired},
		{"changed candidate revision", func(h *decisionHarness, d domain.Decision) domain.DecisionReply {
			h.graph.heads["claim"] = "rev-2"
			return signedReply(d, "approve", "evt-rev", decisionOwnerKey)
		}, domain.ErrStale, domain.DecisionStale},
		{"changed evidence", func(h *decisionHarness, d domain.Decision) domain.DecisionReply {
			a := h.graph.artifacts["artifact"]
			a.Generation = "g2"
			h.graph.artifacts["artifact"] = a
			return signedReply(d, "approve", "evt-ev", decisionOwnerKey)
		}, domain.ErrStale, domain.DecisionStale},
		{"reply names a different revision", func(_ *decisionHarness, d domain.Decision) domain.DecisionReply {
			d.CandidateRevision = "rev-later"
			return signedReply(d, "approve", "evt-other", decisionOwnerKey)
		}, domain.ErrStale, domain.DecisionPending},
		{"reply names a different evidence digest", func(_ *decisionHarness, d domain.Decision) domain.DecisionReply {
			d.EvidenceDigest = TokenDigest("other")
			return signedReply(d, "approve", "evt-dig", decisionOwnerKey)
		}, domain.ErrStale, domain.DecisionPending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newDecisionHarness(t)
			d := h.request(t)
			reply := tc.mutate(h, d)

			got, err := h.svc.Respond(t.Context(), reply)

			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.wantState, got.State)
			}
			assert.Equal(t, tc.wantState, h.repo.decisions[d.ID].State)
			if tc.wantState == domain.DecisionAccepted || tc.wantState == domain.DecisionRejected {
				require.Len(t, h.repo.judgments, 1)
				assert.Equal(t, "api", h.repo.judgments[0].Channel)
			} else {
				assert.Empty(t, h.repo.judgments)
			}
		})
	}

	t.Run("duplicate event returns exact prior receipt without new effect", func(t *testing.T) {
		h := newDecisionHarness(t)
		d := h.request(t)
		reply := signedReply(d, "approve", "evt-dup", decisionOwnerKey)
		first, err := h.svc.Respond(t.Context(), reply)
		require.NoError(t, err)

		again, err := h.svc.Respond(t.Context(), reply)

		require.NoError(t, err)
		assert.Equal(t, first, again)
		assert.Len(t, h.repo.judgments, 1)
	})
	t.Run("replay of a different event after resolution is rejected", func(t *testing.T) {
		h := newDecisionHarness(t)
		d := h.request(t)
		_, err := h.svc.Respond(t.Context(), signedReply(d, "approve", "evt-1", decisionOwnerKey))
		require.NoError(t, err)

		_, err = h.svc.Respond(t.Context(), signedReply(d, "reject", "evt-2", decisionOwnerKey))

		assert.ErrorIs(t, err, domain.ErrConflict)
		assert.Equal(t, domain.DecisionAccepted, h.repo.decisions[d.ID].State)
		assert.Len(t, h.repo.judgments, 1)
	})
	t.Run("event id reused with altered content conflicts", func(t *testing.T) {
		h := newDecisionHarness(t)
		d := h.request(t)
		_, err := h.svc.Respond(t.Context(), signedReply(d, "approve", "evt-x", decisionOwnerKey))
		require.NoError(t, err)

		_, err = h.svc.Respond(t.Context(), signedReply(d, "reject", "evt-x", decisionOwnerKey))

		assert.ErrorIs(t, err, domain.ErrConflict)
	})
	t.Run("buzz reply in wrong thread or channel has no effect", func(t *testing.T) {
		h := newDecisionHarness(t)
		d := h.request(t)
		line := "approve " + d.ID + " " + d.CandidateRevision + " " + d.EvidenceDigest
		wrongThread := h.ownerMessage(d, line, "evt-thread")
		wrongThread.ThreadRootID = strings.Repeat("9", 64)
		wrongChannel := h.ownerMessage(d, line, "evt-channel")
		wrongChannel.ChannelID = "00000000-0000-0000-0000-000000000000"
		impostor := h.ownerMessage(d, line, "evt-impostor")
		impostor.PubKey = decisionAgentKey
		h.buzz.inbox = []domain.SignedMessage{wrongThread, wrongChannel, impostor}

		result, err := h.svc.Ingest(t.Context())

		require.NoError(t, err)
		assert.Equal(t, 0, result.Resolved)
		assert.Equal(t, 2, result.Rejected)
		assert.Equal(t, 1, result.Ignored)
		assert.Equal(t, domain.DecisionPending, h.repo.decisions[d.ID].State)
	})
	t.Run("buzz owner reply in the decision thread resolves once", func(t *testing.T) {
		h := newDecisionHarness(t)
		d := h.request(t)
		msg := h.ownerMessage(d, "approve "+d.ID+" "+d.CandidateRevision+" "+d.EvidenceDigest, "evt-buzz")
		h.buzz.inbox = []domain.SignedMessage{msg, msg}

		result, err := h.svc.Ingest(t.Context())

		require.NoError(t, err)
		assert.Equal(t, 2, result.Resolved)
		assert.Equal(t, domain.DecisionAccepted, h.repo.decisions[d.ID].State)
		require.Len(t, h.repo.judgments, 1)
		assert.Equal(t, "buzz", h.repo.judgments[0].Channel)
		assert.Equal(t, "evt-buzz", h.repo.judgments[0].EventID)
		assert.NotEmpty(t, h.repo.cursor)
	})
}

func TestBuzzOutagePreservesPendingDecision(t *testing.T) {
	h := newDecisionHarness(t)
	h.buzz.down = true

	decision := h.request(t)
	_, ingestErr := h.svc.Ingest(t.Context())
	delivered, deliverErr := h.svc.Deliver(t.Context(), 10)

	assert.Equal(t, "pending", string(decision.State))
	assert.Empty(t, decision.ThreadID)
	assert.ErrorIs(t, ingestErr, domain.ErrUnavailable)
	assert.ErrorIs(t, deliverErr, domain.ErrUnavailable)
	assert.Zero(t, delivered)
	assert.Equal(t, domain.DecisionPending, h.repo.decisions[decision.ID].State)
	pending, err := h.svc.Pending(t.Context(), 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)

	h.buzz.down = false
	delivered, err = h.svc.Deliver(t.Context(), 10)
	require.NoError(t, err)
	assert.Equal(t, 1, delivered)
	stored := h.repo.decisions[decision.ID]
	assert.Equal(t, decisionChannel, stored.ChannelID)
	assert.NotEmpty(t, stored.ThreadID)
	require.Len(t, h.buzz.bodies, 1)
	assert.NotContains(t, h.buzz.bodies[0], "private claim text")
	assert.Contains(t, h.buzz.bodies[0], "approve "+decision.ID+" rev-1 "+decision.EvidenceDigest)
}

func TestDecisionRequestBindsCurrentEvidenceAndAuthority(t *testing.T) {
	h := newDecisionHarness(t)
	draft, err := h.svc.Prepare(t.Context(), verifierPrincipal, "rev-1", "publish")
	require.NoError(t, err)
	for _, tc := range []struct {
		name    string
		p       domain.Principal
		mutate  func(*domain.Decision)
		wantErr error
	}{
		{"agent cannot request owner decisions", domain.Principal{ClientID: "agent", Role: domain.RoleAgent, ProjectIDs: []string{"demo"}}, func(*domain.Decision) {}, domain.ErrForbidden},
		{"verifier outside project", domain.Principal{ClientID: "v", Role: domain.RoleVerifier, ProjectIDs: []string{"other"}}, func(*domain.Decision) {}, domain.ErrForbidden},
		{"stale evidence digest", verifierPrincipal, func(d *domain.Decision) { d.EvidenceDigest = TokenDigest("old") }, domain.ErrStale},
		{"missing action", verifierPrincipal, func(d *domain.Decision) { d.RequestedAction = "" }, domain.ErrInvalidInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := draft
			tc.mutate(&d)

			_, err := h.svc.Request(t.Context(), tc.p, d)

			assert.ErrorIs(t, err, tc.wantErr)
		})
	}
	t.Run("repeat request is idempotent and expires in 24h", func(t *testing.T) {
		first, err := h.svc.Request(t.Context(), verifierPrincipal, draft)
		require.NoError(t, err)

		second, err := h.svc.Request(t.Context(), verifierPrincipal, draft)

		require.NoError(t, err)
		assert.Equal(t, first.ID, second.ID)
		assert.Equal(t, h.now.Add(24*time.Hour), first.ExpiresAt)
		assert.Len(t, h.buzz.posts, 1)
	})
}

func TestOwnerChallengeCannotElevateAgentToken(t *testing.T) {
	setup := func(t *testing.T) (*decisionHarness, domain.Decision, domain.OwnerChallenge) {
		h := newDecisionHarness(t)
		h.buzz.down = true
		d := h.request(t)
		digest := domain.DecisionActionDigest(d.ID, "approve", d.CandidateRevision, d.EvidenceDigest)
		c, err := h.svc.CreateOwnerChallenge(t.Context(), DecisionRespondAction, digest)
		require.NoError(t, err)
		return h, d, c
	}
	t.Run("agent-signed challenge yields no grant", func(t *testing.T) {
		h, _, c := setup(t)

		_, token, err := h.svc.ApproveOwnerChallenge(t.Context(), c.ID, fakeSign(decisionAgentKey, c.SigningDigest()))

		assert.ErrorIs(t, err, domain.ErrForbidden)
		assert.Empty(t, token)
		assert.Empty(t, h.repo.grants)
	})
	t.Run("agent-signed decision reply cannot act as owner", func(t *testing.T) {
		h, d, _ := setup(t)
		reply := signedReply(d, "approve", "evt", decisionAgentKey)

		_, err := h.svc.Respond(t.Context(), reply)

		assert.ErrorIs(t, err, domain.ErrForbidden)
	})
	t.Run("grant is scoped to one action digest and one use", func(t *testing.T) {
		h, d, c := setup(t)
		grant, token, err := h.svc.ApproveOwnerChallenge(t.Context(), c.ID, fakeSign(decisionOwnerKey, c.SigningDigest()))
		require.NoError(t, err)
		assert.NotContains(t, grant.TokenDigest, token)

		_, wrongAction := h.svc.RespondWithGrant(t.Context(), token, d.ID, "reject")
		assert.ErrorIs(t, wrongAction, domain.ErrForbidden)
		assert.Equal(t, domain.DecisionPending, h.repo.decisions[d.ID].State)

		got, err := h.svc.RespondWithGrant(t.Context(), token, d.ID, "approve")
		require.NoError(t, err)
		assert.Equal(t, domain.DecisionAccepted, got.State)
		assert.Equal(t, "recovery", h.repo.judgments[0].Channel)

		_, reused := h.svc.RespondWithGrant(t.Context(), token, d.ID, "approve")
		assert.ErrorIs(t, reused, domain.ErrConflict)
		_, reapproved, err := h.svc.ApproveOwnerChallenge(t.Context(), c.ID, fakeSign(decisionOwnerKey, c.SigningDigest()))
		assert.ErrorIs(t, err, domain.ErrConflict)
		assert.Empty(t, reapproved)
	})
	t.Run("unknown token and expired challenge are refused", func(t *testing.T) {
		h, d, c := setup(t)
		_, err := h.svc.RespondWithGrant(t.Context(), "not-a-grant", d.ID, "approve")
		assert.ErrorIs(t, err, domain.ErrUnauthorized)
		h.now = c.ExpiresAt.Add(time.Second)

		_, _, err = h.svc.ApproveOwnerChallenge(t.Context(), c.ID, fakeSign(decisionOwnerKey, c.SigningDigest()))

		assert.ErrorIs(t, err, domain.ErrStale)
	})
	t.Run("challenge input is validated", func(t *testing.T) {
		h := newDecisionHarness(t)
		_, err := h.svc.CreateOwnerChallenge(t.Context(), "", TokenDigest("x"))
		assert.ErrorIs(t, err, domain.ErrInvalidInput)
		_, err = h.svc.CreateOwnerChallenge(t.Context(), DecisionRespondAction, "not-a-digest")
		assert.ErrorIs(t, err, domain.ErrInvalidInput)
	})
}

func TestDecisionChallengeBindsPendingDecision(t *testing.T) {
	h := newDecisionHarness(t)
	d := h.request(t)

	c, err := h.svc.CreateDecisionChallenge(t.Context(), d.ID, "approve")

	require.NoError(t, err)
	assert.Equal(t, domain.DecisionActionDigest(d.ID, "approve", d.CandidateRevision, d.EvidenceDigest), c.ActionDigest)
	_, err = h.svc.CreateDecisionChallenge(t.Context(), d.ID, "publish")
	assert.ErrorIs(t, err, domain.ErrInvalidInput)
	_, err = h.svc.CreateDecisionChallenge(t.Context(), "missing", "approve")
	assert.ErrorIs(t, err, domain.ErrIncomplete)
	_, token, err := h.svc.ApproveOwnerChallenge(t.Context(), c.ID, fakeSign(decisionOwnerKey, c.SigningDigest()))
	require.NoError(t, err)
	_, err = h.svc.RespondWithGrant(t.Context(), token, d.ID, "approve")
	require.NoError(t, err)
	_, err = h.svc.CreateDecisionChallenge(t.Context(), d.ID, "approve")
	assert.ErrorIs(t, err, domain.ErrConflict)
}

func TestAckLostDoesNotDuplicatePost(t *testing.T) {
	h := newDecisionHarness(t)
	d := h.request(t)
	// The relay accepted the post, but a crash lost the local acknowledgement.
	stored := h.repo.decisions[d.ID]
	stored.ThreadID, stored.ChannelID = "", ""
	h.repo.decisions[d.ID] = stored

	delivered, err := h.svc.Deliver(t.Context(), 10)

	require.NoError(t, err)
	assert.Equal(t, 1, delivered)
	deliveredMessages := len(h.buzz.posts)
	assert.Equal(t, 1, deliveredMessages)
	assert.Equal(t, h.buzz.posts[0].ThreadID, h.repo.decisions[d.ID].ThreadID)
}

func TestIngestDoesNotAdvanceCursorPastTransientFailure(t *testing.T) {
	h := newDecisionHarness(t)
	d := h.request(t)
	reply := h.ownerMessage(d, "approve "+d.ID+" "+d.CandidateRevision+" "+d.EvidenceDigest, "evt-transient")
	later := h.ownerMessage(d, "not a decision line", "evt-later")
	later.CreatedAt = h.now.Add(time.Minute)
	h.repo.cursor = "cursor-before"
	h.repo.judgmentErr = domain.ErrUnavailable
	h.buzz.inbox = []domain.SignedMessage{reply, later}

	result, err := h.svc.Ingest(t.Context())

	assert.ErrorIs(t, err, domain.ErrUnavailable)
	assert.Equal(t, 1, result.Deferred)
	assert.Equal(t, "cursor-before", h.repo.cursor, "cursor must not move past a reply that failed transiently")
	assert.Equal(t, domain.DecisionPending, h.repo.decisions[d.ID].State)

	h.repo.judgmentErr = nil
	h.buzz.inbox = []domain.SignedMessage{reply, later}
	result, err = h.svc.Ingest(t.Context())

	require.NoError(t, err)
	assert.Equal(t, 1, result.Resolved)
	assert.Equal(t, domain.DecisionAccepted, h.repo.decisions[d.ID].State)
	assert.NotEqual(t, "cursor-before", h.repo.cursor)
}

func TestIngestAdvancesCursorPastPermanentRejection(t *testing.T) {
	h := newDecisionHarness(t)
	d := h.request(t)
	wrongThread := h.ownerMessage(d, "approve "+d.ID+" "+d.CandidateRevision+" "+d.EvidenceDigest, "evt-thread")
	wrongThread.ThreadRootID = strings.Repeat("9", 64)
	h.repo.cursor = "cursor-before"
	h.buzz.inbox = []domain.SignedMessage{wrongThread}

	result, err := h.svc.Ingest(t.Context())

	require.NoError(t, err)
	assert.Equal(t, 1, result.Rejected)
	assert.NotEqual(t, "cursor-before", h.repo.cursor)
}

func TestDecisionRequestAfterExpiryStartsFreshGeneration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		expire func(*testing.T, *decisionHarness, domain.Decision)
	}{
		{"pending past expiry", func(_ *testing.T, h *decisionHarness, d domain.Decision) {
			h.now = d.ExpiresAt.Add(time.Second)
		}},
		{"undelivered pending past expiry", func(_ *testing.T, h *decisionHarness, d domain.Decision) {
			stored := h.repo.decisions[d.ID]
			stored.ThreadID, stored.ChannelID = "", ""
			h.repo.decisions[d.ID] = stored
			h.buzz.posts, h.buzz.bodies = nil, nil
			h.now = d.ExpiresAt.Add(time.Second)
		}},
		{"terminal expired", func(t *testing.T, h *decisionHarness, d domain.Decision) {
			h.now = d.ExpiresAt.Add(time.Second)
			_, err := h.svc.Respond(t.Context(), signedReply(d, "approve", "evt-late", decisionOwnerKey))
			require.ErrorIs(t, err, domain.ErrStale)
			require.Equal(t, domain.DecisionExpired, h.repo.decisions[d.ID].State)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newDecisionHarness(t)
			old := h.request(t)
			tc.expire(t, h, old)
			posted := len(h.buzz.posts)

			renewed := h.request(t)

			assert.NotEqual(t, old.ID, renewed.ID)
			assert.Equal(t, domain.DecisionPending, renewed.State)
			assert.Equal(t, h.now.Add(domain.DefaultDecisionTTL), renewed.ExpiresAt)
			assert.Equal(t, domain.DecisionExpired, h.repo.decisions[old.ID].State)
			require.Len(t, h.buzz.posts, posted+1, "only the fresh generation is posted")
			assert.Equal(t, "kb-decision-"+renewed.ID, h.buzz.posts[posted].OperationKey)
			again := h.request(t)
			assert.Equal(t, renewed.ID, again.ID, "the fresh generation is itself idempotent")
			assert.Len(t, h.buzz.posts, posted+1)
		})
	}
	t.Run("resolved decision is returned, not renewed", func(t *testing.T) {
		h := newDecisionHarness(t)
		d := h.request(t)
		_, err := h.svc.Respond(t.Context(), signedReply(d, "approve", "evt-ok", decisionOwnerKey))
		require.NoError(t, err)
		h.now = d.ExpiresAt.Add(time.Hour)

		again := h.request(t)

		assert.Equal(t, d.ID, again.ID)
		assert.Equal(t, domain.DecisionAccepted, again.State)
	})
}

func TestHostDecisionRequestUsesPersistedOwner(t *testing.T) {
	h := newDecisionHarness(t)

	draft, err := h.svc.PrepareHost(t.Context(), "rev-1", "publish")
	require.NoError(t, err)
	d, err := h.svc.RequestHost(t.Context(), draft)

	require.NoError(t, err)
	assert.Equal(t, "owner", d.OwnerID)
	assert.Equal(t, domain.DecisionPending, d.State)
	assert.NotEmpty(t, d.ThreadID)
	stale := draft
	stale.EvidenceDigest = TokenDigest("old")
	_, err = h.svc.RequestHost(t.Context(), stale)
	assert.ErrorIs(t, err, domain.ErrStale)
	h.graph.owner = domain.OwnerIdentity{}
	_, err = h.svc.PrepareHost(t.Context(), "rev-1", "publish")
	assert.ErrorIs(t, err, domain.ErrForbidden)
	_, err = h.svc.RequestHost(t.Context(), draft)
	assert.ErrorIs(t, err, domain.ErrForbidden)
}

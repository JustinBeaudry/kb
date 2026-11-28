package repository_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/platform/graph"
	"github.com/JustinBeaudry/kb/server/internal/repository"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func decisionKey(seed string) (*btcec.PrivateKey, string) {
	sum := sha256.Sum256([]byte("kb-synthetic-decision-key:" + seed))
	priv, _ := btcec.PrivKeyFromBytes(sum[:])
	return priv, hex.EncodeToString(schnorr.SerializePubKey(priv.PubKey()))
}

func schnorrSign(t *testing.T, priv *btcec.PrivateKey, digest [32]byte) []byte {
	t.Helper()
	sig, err := schnorr.Sign(priv, digest[:])
	require.NoError(t, err)
	return sig.Serialize()
}

func seedDecisionCandidate(t *testing.T, store *graph.Store, revision string) {
	t.Helper()
	artifact, err := json.Marshal(domain.Artifact{ID: "artifact-1", Digest: service.TokenDigest("bytes"), Generation: "g1", Scope: domain.Scope{ProjectID: "demo", TaskID: "task"}, State: domain.ArtifactReady})
	require.NoError(t, err)
	claim, err := json.Marshal(domain.ClaimRevision{ClaimID: "claim-1", Revision: revision, Kind: "finding", Text: "synthetic", Scope: domain.Scope{ProjectID: "demo", TaskID: "task"}, Evidence: []domain.EvidenceRef{{ArtifactID: "artifact-1", Digest: service.TokenDigest("bytes"), Locator: "file"}}, Status: domain.ClaimCandidate})
	require.NoError(t, err)
	require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
		native, e := store.Unwrap(tx)
		if e != nil {
			return e
		}
		if _, e = native.Run(ctx, `MERGE (a:Artifact {id:'artifact-1'}) SET a.data=$artifact,a.clientID='collector',a.state='ready',a.projectID='demo' MERGE (h:Claim {id:'claim-1'}) WITH h OPTIONAL MATCH (h)-[old:HEAD]->() DELETE old WITH h CREATE (c:ClaimRevision {id:$revision,claimID:'claim-1',data:$claim,status:'candidate',valid:false,projectID:'demo'}) CREATE (h)-[:HEAD]->(c)`, map[string]any{"artifact": string(artifact), "claim": string(claim), "revision": revision}); e != nil {
			return e
		}
		return nil
	}))
}

func TestDecisionRoundTripOnRealGraph(t *testing.T) {
	store, _, _ := jobsGraph(t)
	ownerPriv, ownerPub := decisionKey("owner")
	agentPriv, _ := decisionKey("agent")
	owners := repository.NewIdentityRepository(store)
	identity := service.NewIdentityService(store, owners, gateway.NostrIdentity{})
	_, err := identity.Setup(t.Context(), domain.OwnerIdentity{ID: "owner", PublicKey: ownerPub}, service.EnrollInput{ClientID: "verifier", Role: domain.RoleAgent, ProjectIDs: []string{"demo"}, Capabilities: []string{"status:read"}})
	require.NoError(t, err)
	seedDecisionCandidate(t, store, "rev-1")
	repo := repository.NewDecisionRepository(store)
	decisions := service.NewDecisionService(store, repo, repository.NewVerificationRepository(store), repository.NewEvidenceRepository(store), owners, nil, gateway.NostrVerifier{}, nil)
	verifier := domain.Principal{ClientID: "verifier", Role: domain.RoleVerifier, ProjectIDs: []string{"demo"}}

	draft, err := decisions.Prepare(t.Context(), verifier, "rev-1", "publish")
	require.NoError(t, err)
	d, err := decisions.Request(t.Context(), verifier, draft)
	require.NoError(t, err)
	assert.Equal(t, domain.DecisionPending, d.State)
	pending, err := decisions.Pending(t.Context(), 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)

	t.Run("signed owner reply resolves once and records judgment separately", func(t *testing.T) {
		reply := domain.DecisionReply{DecisionID: d.ID, CandidateRevision: d.CandidateRevision, EvidenceDigest: d.EvidenceDigest, Action: "approve", EventID: "nonce-1", SignerID: ownerPub}
		agent := reply
		agent.Signature = schnorrSign(t, agentPriv, domain.DecisionReplyDigest(reply))
		_, err := decisions.Respond(t.Context(), agent)
		assert.ErrorIs(t, err, domain.ErrUnauthorized)
		reply.Signature = schnorrSign(t, ownerPriv, domain.DecisionReplyDigest(reply))

		first, err := decisions.Respond(t.Context(), reply)
		require.NoError(t, err)
		again, err := decisions.Respond(t.Context(), reply)
		require.NoError(t, err)

		assert.Equal(t, domain.DecisionAccepted, first.State)
		assert.Equal(t, first, again)
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			native, e := store.Unwrap(tx)
			require.NoError(t, e)
			res, e := native.Run(ctx, `MATCH (j:OwnerJudgment)-[:JUDGES]->(:Decision {id:$id}) OPTIONAL MATCH (v:Verification {claimRevision:'rev-1'}) RETURN count(DISTINCT j), count(v)`, map[string]any{"id": d.ID})
			require.NoError(t, e)
			rec, e := res.Single(ctx)
			require.NoError(t, e)
			assert.Equal(t, int64(1), rec.Values[0])
			assert.Equal(t, int64(0), rec.Values[1], "owner judgment must not create a Verification")
			return nil
		}))
	})

	t.Run("evidence change before recovery approval marks decision stale", func(t *testing.T) {
		seedDecisionCandidate(t, store, "rev-2")
		draft, err := decisions.Prepare(t.Context(), verifier, "rev-2", "publish")
		require.NoError(t, err)
		d2, err := decisions.Request(t.Context(), verifier, draft)
		require.NoError(t, err)
		challenge, err := decisions.CreateOwnerChallenge(t.Context(), service.DecisionRespondAction, domain.DecisionActionDigest(d2.ID, "approve", d2.CandidateRevision, d2.EvidenceDigest))
		require.NoError(t, err)
		_, token, err := decisions.ApproveOwnerChallenge(t.Context(), challenge.ID, schnorrSign(t, ownerPriv, challenge.SigningDigest()))
		require.NoError(t, err)
		artifact, err := json.Marshal(domain.Artifact{ID: "artifact-1", Digest: service.TokenDigest("bytes"), Generation: "g2", Scope: domain.Scope{ProjectID: "demo", TaskID: "task"}, State: domain.ArtifactReady})
		require.NoError(t, err)
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			native, e := store.Unwrap(tx)
			require.NoError(t, e)
			_, e = native.Run(ctx, `MATCH (a:Artifact {id:'artifact-1'}) SET a.data=$data`, map[string]any{"data": string(artifact)})
			return e
		}))

		_, err = decisions.RespondWithGrant(t.Context(), token, d2.ID, "approve")

		assert.ErrorIs(t, err, domain.ErrStale)
		pending, err := decisions.Pending(t.Context(), 10)
		require.NoError(t, err)
		assert.Empty(t, pending)
		_, err = decisions.RespondWithGrant(t.Context(), token, d2.ID, "approve")
		assert.ErrorIs(t, err, domain.ErrConflict)
	})

	t.Run("cursor persists", func(t *testing.T) {
		require.NoError(t, store.Within(t.Context(), func(ctx context.Context, tx domain.Tx) error {
			c, e := repo.Cursor(ctx, tx)
			require.NoError(t, e)
			assert.Empty(t, c)
			require.NoError(t, repo.PutCursor(ctx, tx, "123"))
			c, e = repo.Cursor(ctx, tx)
			assert.Equal(t, "123", c)
			return e
		}))
	})

	t.Run("judgments list what each key signed, after the key is replaced", func(t *testing.T) {
		page, err := decisions.JudgmentsHost(t.Context(), domain.JudgmentQuery{})
		require.NoError(t, err)
		require.Len(t, page.Judgments, 1)
		j := page.Judgments[0]
		assert.Equal(t, ownerPub, j.SignerID)
		assert.True(t, j.CurrentKey)
		assert.Equal(t, "approve", j.Action)
		assert.Equal(t, "demo", j.ProjectID)
		assert.Equal(t, "publish", j.RequestedAction)
		assert.Equal(t, d.ID, j.DecisionID)
		assert.Equal(t, "rev-1", j.CandidateRevision)
		assert.False(t, j.RecordedAt.IsZero())

		_, newPub := decisionKey("owner-2")
		_, err = identity.ReplaceOwnerKeyHost(t.Context(), newPub)
		require.NoError(t, err)

		retired, err := decisions.JudgmentsHost(t.Context(), domain.JudgmentQuery{Signer: ownerPub})
		require.NoError(t, err)
		require.Len(t, retired.Judgments, 1)
		assert.False(t, retired.Judgments[0].CurrentKey, "a replaced key's judgments are marked as no longer current")
		none, err := decisions.JudgmentsHost(t.Context(), domain.JudgmentQuery{Signer: newPub})
		require.NoError(t, err)
		assert.Empty(t, none.Judgments)
	})
}

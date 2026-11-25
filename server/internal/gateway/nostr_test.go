package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syntheticKey derives a deterministic test-only key; no real identity.
func syntheticKey(t *testing.T, seed string) (*btcec.PrivateKey, string) {
	t.Helper()
	sum := sha256.Sum256([]byte("kb-synthetic-test-key:" + seed))
	priv, _ := btcec.PrivKeyFromBytes(sum[:])
	return priv, hex.EncodeToString(schnorr.SerializePubKey(priv.PubKey()))
}

func signEvent(t *testing.T, priv *btcec.PrivateKey, e NostrEvent) NostrEvent {
	t.Helper()
	e.PubKey = hex.EncodeToString(schnorr.SerializePubKey(priv.PubKey()))
	e.ID = NostrEventID(e)
	id, err := hex.DecodeString(e.ID)
	require.NoError(t, err)
	sig, err := schnorr.Sign(priv, id)
	require.NoError(t, err)
	e.Sig = hex.EncodeToString(sig.Serialize())
	return e
}

func TestNostrEventIDUsesNIP01Escaping(t *testing.T) {
	for _, tc := range []struct{ name, content, want string }{
		{"quote and backslash", "a\"b\\c", `"a\"b\\c"`},
		{"short escapes", "\n\r\t\b\f", `"\n\r\t\b\f"`},
		{"other control", "\x01\x1f", `"\u0001\u001f"`},
		{"html and separators stay raw", "<&> é", "\"<&> é\""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			nip01String(&b, tc.content)

			assert.Equal(t, tc.want, b.String())
		})
	}
}

func TestVerifyNostrEvent(t *testing.T) {
	priv, pub := syntheticKey(t, "owner")
	_, other := syntheticKey(t, "other")
	valid := signEvent(t, priv, NostrEvent{CreatedAt: 1_787_000_000, Kind: 9, Tags: [][]string{{"h", "c"}, {"e", "ab", "", "root"}}, Content: "approve \"x\"\n<é>"})
	require.NoError(t, VerifyNostrEvent(valid))
	for _, tc := range []struct {
		name   string
		mutate func(*NostrEvent)
	}{
		{"tampered content", func(e *NostrEvent) { e.Content += "!" }},
		{"tampered tag", func(e *NostrEvent) { e.Tags[0][1] = "other" }},
		{"swapped author", func(e *NostrEvent) { e.PubKey = other }},
		{"recomputed id without resigning", func(e *NostrEvent) { e.Content = "reject"; e.ID = NostrEventID(*e) }},
		{"bad signature", func(e *NostrEvent) { e.Sig = e.Sig[:126] + "00" }},
		{"uppercase id", func(e *NostrEvent) { e.ID = "A" + e.ID[1:] }},
		{"short signature", func(e *NostrEvent) { e.Sig = "00" }},
		{"invalid utf8", func(e *NostrEvent) { e.Content = "\xff" }},
		{"zero time", func(e *NostrEvent) { e.CreatedAt = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := valid
			e.Tags = [][]string{append([]string(nil), valid.Tags[0]...), valid.Tags[1]}
			tc.mutate(&e)

			assert.Error(t, VerifyNostrEvent(e))
		})
	}
	assert.Equal(t, pub, valid.PubKey)
}

func TestNostrVerifierChecksOwnerDigestSignature(t *testing.T) {
	priv, pub := syntheticKey(t, "owner")
	_, other := syntheticKey(t, "agent")
	digest := domain.DecisionReplyDigest(domain.DecisionReply{DecisionID: "d", Action: "approve"})
	sig, err := schnorr.Sign(priv, digest[:])
	require.NoError(t, err)

	assert.NoError(t, NostrVerifier{}.VerifyDigest(pub, digest, sig.Serialize()))
	assert.ErrorIs(t, NostrVerifier{}.VerifyDigest(other, digest, sig.Serialize()), domain.ErrUnauthorized)
	assert.ErrorIs(t, NostrVerifier{}.VerifyDigest(pub, [32]byte{1}, sig.Serialize()), domain.ErrUnauthorized)
	assert.ErrorIs(t, NostrVerifier{}.VerifyDigest("zz", digest, sig.Serialize()), domain.ErrUnauthorized)
	assert.ErrorIs(t, NostrVerifier{}.VerifyDigest(pub, digest, []byte{1}), domain.ErrUnauthorized)
}

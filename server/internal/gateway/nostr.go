package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

// NostrEvent is the NIP-01 wire shape returned by the Buzz CLI. It never
// leaves the gateway; callers receive domain.SignedMessage.
type NostrEvent struct {
	ID        string     `json:"id"`
	PubKey    string     `json:"pubkey"`
	CreatedAt int64      `json:"created_at"`
	Kind      int        `json:"kind"`
	Tags      [][]string `json:"tags"`
	Content   string     `json:"content"`
	Sig       string     `json:"sig"`
}

// nip01String escapes exactly as NIP-01 requires: quote, backslash and the
// short control escapes; other control bytes as \u00XX; everything else raw.
func nip01String(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 {
				b.WriteString(`\u00`)
				b.WriteString(hex.EncodeToString([]byte{byte(r)}))
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}

// NostrEventID computes the NIP-01 event ID from the event's signed fields.
func NostrEventID(e NostrEvent) string {
	var b strings.Builder
	b.WriteString(`[0,`)
	nip01String(&b, e.PubKey)
	b.WriteByte(',')
	b.WriteString(strconv.FormatInt(e.CreatedAt, 10))
	b.WriteByte(',')
	b.WriteString(strconv.Itoa(e.Kind))
	b.WriteString(`,[`)
	for i, tag := range e.Tags {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('[')
		for j, v := range tag {
			if j > 0 {
				b.WriteByte(',')
			}
			nip01String(&b, v)
		}
		b.WriteByte(']')
	}
	b.WriteString(`],`)
	nip01String(&b, e.Content)
	b.WriteByte(']')
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func lowerHex(s string, n int) ([]byte, bool) {
	if len(s) != 2*n || s != strings.ToLower(s) {
		return nil, false
	}
	raw, err := hex.DecodeString(s)
	return raw, err == nil
}

// VerifyNostrEvent checks event identity and the BIP-340 signature locally.
// Relay or CLI claims are never trusted.
func VerifyNostrEvent(e NostrEvent) error {
	if !utf8.ValidString(e.Content) || e.CreatedAt <= 0 || e.Kind < 0 {
		return domain.ErrInvalidInput
	}
	if _, ok := lowerHex(e.ID, 32); !ok || NostrEventID(e) != e.ID {
		return domain.ErrUnauthorized
	}
	sig, ok := lowerHex(e.Sig, 64)
	if !ok {
		return domain.ErrUnauthorized
	}
	id, _ := hex.DecodeString(e.ID)
	var digest [32]byte
	copy(digest[:], id)
	return verifySchnorr(e.PubKey, digest, sig)
}

func verifySchnorr(publicKey string, digest [32]byte, signature []byte) error {
	raw, ok := lowerHex(publicKey, 32)
	if !ok || len(signature) != schnorr.SignatureSize {
		return domain.ErrUnauthorized
	}
	key, err := schnorr.ParsePubKey(raw)
	if err != nil {
		return domain.ErrUnauthorized
	}
	sig, err := schnorr.ParseSignature(signature)
	if err != nil || !sig.Verify(digest[:], key) {
		return domain.ErrUnauthorized
	}
	return nil
}

// NostrVerifier verifies owner BIP-340 signatures over canonical digests.
type NostrVerifier struct{}

func (NostrVerifier) VerifyDigest(publicKey string, digest [32]byte, signature []byte) error {
	return verifySchnorr(publicKey, digest, signature)
}

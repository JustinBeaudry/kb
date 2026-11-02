package gateway

import (
	"strings"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNostrPublicKeyCanonicalization(t *testing.T) {
	const key = "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"
	for _, input := range []string{key, strings.ToUpper(key)} {
		t.Run(input[:8], func(t *testing.T) {
			canonical, err := (NostrIdentity{}).NormalizePublicKey(input)
			require.NoError(t, err)
			assert.Equal(t, key, canonical)
		})
	}
}
func TestNostrPublicKeyRejectsWrongEncodingAndInvalidCurvePoints(t *testing.T) {
	for _, tc := range []struct{ name, input string }{{"empty", ""}, {"base64", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}, {"nonhex", strings.Repeat("z", 64)}, {"zero", strings.Repeat("0", 64)}, {"off curve", strings.Repeat("0", 63) + "5"}, {"outside field", strings.Repeat("f", 64)}} {
		t.Run(tc.name, func(t *testing.T) {
			canonical, err := (NostrIdentity{}).NormalizePublicKey(tc.input)
			assert.ErrorIs(t, err, domain.ErrInvalidInput)
			assert.Empty(t, canonical)
		})
	}
}

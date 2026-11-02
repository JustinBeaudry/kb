package domain

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestValidateIDsRejectsMissingOpaqueIdentity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ids     []string
		invalid bool
	}{
		{"opaque allowed", []string{"non-uuid:owner/1", "revision:unparsed"}, false},
		{"empty identity", []string{"project", ""}, true},
		{"blank identity", []string{" \t"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateIDs(tc.ids...)
			if tc.invalid {
				assert.ErrorIs(t, err, ErrInvalidInput)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

package gateway

import (
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChildArgumentsForVerificationCommands(t *testing.T) {
	cases := []struct {
		name    string
		action  string
		preview bool
		want    []string
		err     error
	}{
		{"check reads stdin", "verify-check", false, []string{"verify-check", "--input", "-"}, nil},
		{"check has no preview", "verify-check", true, nil, domain.ErrInvalidInput},
		{"lesson preview", "lesson-publish", true, []string{"lesson-publish", "--input", "-", "--preview"}, nil},
		{"lesson publish", "lesson-publish", false, []string{"lesson-publish", "--input", "-"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args, err := childArguments(domain.HostAdminRequest{Version: 1, Action: tc.action, Options: domain.HostAdminOptions{Preview: tc.preview}})

			if tc.err != nil {
				assert.ErrorIs(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, args)
		})
	}
}

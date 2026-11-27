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

func TestChildArgumentsForCredentialCommands(t *testing.T) {
	args, err := childArguments(domain.HostAdminRequest{Version: 1, Action: "rotate", Options: domain.HostAdminOptions{ClientID: "agent", TTL: "2160h"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"rotate", "--client-id", "agent", "--ttl", "2160h", "--credential-fd", "3"}, args)
	args, err = childArguments(domain.HostAdminRequest{Version: 1, Action: "enroll", Options: domain.HostAdminOptions{ProjectID: "p", TTL: "24h"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"enroll", "--project", "p", "--ttl", "24h", "--credential-fd", "3"}, args)
	for _, action := range []string{"setup", "enroll", "rotate"} {
		assert.True(t, domain.HostAdminIssuesCredential(action), action)
	}
	for _, action := range []string{"revoke", "list", "verify"} {
		assert.False(t, domain.HostAdminIssuesCredential(action), action)
	}
}

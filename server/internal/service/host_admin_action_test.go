package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHostAdminActionAllowlist(t *testing.T) {
	cases := []struct {
		action string
		want   bool
	}{
		{"verify", true},
		{"verify-check", true},
		{"lesson-publish", true},
		{"check", false},
		{"publish", false},
		{"recover", false},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			assert.Equal(t, tc.want, hostAdminAction(tc.action))
		})
	}
}

package service

import (
	"context"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

type policyFixture struct {
	current domain.Principal
	policy  domain.Policy
	err     error
}

func (f *policyFixture) Within(ctx context.Context, fn func(context.Context, Tx) error) error {
	return fn(ctx, nil)
}
func (f *policyFixture) Snapshot(context.Context, Tx, domain.Principal, domain.Scope) (domain.RetrievalSnapshot, error) {
	return domain.RetrievalSnapshot{Principal: f.current}, f.err
}
func (f *policyFixture) Policy(context.Context, Tx, string) (domain.Policy, error) {
	return f.policy, nil
}
func TestPolicyUsesCurrentAuthoritativeGrants(t *testing.T) {
	p := domain.Principal{ClientID: "collector", ProjectIDs: []string{"demo"}, Capabilities: []string{"policy:read"}}
	for _, tc := range []struct {
		name      string
		current   domain.Principal
		policy    domain.Policy
		err, want error
	}{
		{name: "authorized", current: p, policy: domain.Policy{Revision: "p1"}},
		{name: "project grant removed", current: domain.Principal{ClientID: p.ClientID, ProjectIDs: []string{"other"}, Capabilities: p.Capabilities}, want: domain.ErrForbidden},
		{name: "capability removed", current: domain.Principal{ClientID: p.ClientID, ProjectIDs: p.ProjectIDs}, want: domain.ErrForbidden},
		{name: "revoked", current: p, err: domain.ErrUnauthorized, want: domain.ErrUnauthorized},
		{name: "missing policy", current: p, want: domain.ErrForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &policyFixture{tc.current, tc.policy, tc.err}
			got, err := NewPolicyService(f, f, f).Get(t.Context(), p, "demo")
			if tc.want != nil {
				assert.ErrorIs(t, err, tc.want)
				assert.Empty(t, got.Revision)
			} else {
				require.NoError(t, err)
				assert.Equal(t, "p1", got.Revision)
			}
		})
	}
}
func TestReadOnlyPolicyEnrollment(t *testing.T) {
	for _, role := range []domain.Role{domain.RoleCollector, domain.RoleAgent, domain.RoleVerifier} {
		t.Run(string(role), func(t *testing.T) {
			assert.NoError(t, validateEnrollment(EnrollInput{Role: role, ProjectIDs: []string{"demo"}, Capabilities: []string{"policy:read"}}))
			assert.ErrorIs(t, validateEnrollment(EnrollInput{Role: role, ProjectIDs: []string{"demo"}, Capabilities: []string{"policy:write"}}), domain.ErrInvalidInput)
		})
	}
}

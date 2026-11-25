package service_test

import (
	"context"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type statusGrants struct{ principal domain.Principal }

func (s statusGrants) Snapshot(context.Context, service.Tx, domain.Principal, domain.Scope) (service.RetrievalSnapshot, error) {
	return service.RetrievalSnapshot{Principal: s.principal}, nil
}

func TestStatusReportsDreamingToOwnerOnly(t *testing.T) {
	owner := domain.Principal{ClientID: "owner", Role: domain.RoleOwner, ProjectIDs: []string{"project"}, Capabilities: []string{"status:read"}}
	agent := domain.Principal{ClientID: "agent", Role: domain.RoleAgent, ProjectIDs: []string{"project"}, Capabilities: []string{"status:read"}}
	for _, tc := range []struct {
		name      string
		principal domain.Principal
		cap       int64
		reasons   []string
		want      string
	}{
		{"agent sees no dreaming state", agent, 1000, nil, ""},
		{"no cap is unconfigured", owner, 0, []string{"model_budget_unconfigured"}, "unconfigured"},
		{"missing route is pending", owner, 1000, []string{"model_route_unavailable"}, "pending"},
		{"exhausted budget", owner, 1000, []string{"model_budget_exhausted"}, "budget_exhausted"},
		{"healthy", owner, 1000, nil, "available"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeDreamRepo()
			repo.pendingJobs = len(tc.reasons)
			repo.pendingCodes = tc.reasons
			tx := &lockTx{}
			budget := service.NewModelBudgetService(tx, newFakeBudgetRepo(), tc.cap, nil)
			s := service.NewStatusService(tx, statusGrants{tc.principal}).WithDreaming(repo, budget)

			status, err := s.Status(context.Background(), tc.principal)

			require.NoError(t, err)
			if tc.want == "" {
				assert.Nil(t, status.Dreaming)
				return
			}
			require.NotNil(t, status.Dreaming)
			assert.Equal(t, tc.want, status.Dreaming.State)
			assert.Equal(t, len(tc.reasons), status.Dreaming.PendingJobs)
			assert.Equal(t, tc.cap, status.Dreaming.DailyCapMicroUSD)
		})
	}
}

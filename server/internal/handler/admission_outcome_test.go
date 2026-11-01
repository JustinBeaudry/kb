package handler

import (
	"context"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/platform"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"
)

type outcomeMaintenanceStore struct {
	state   domain.MaintenanceState
	permits []domain.OperationPermit
}

func (s *outcomeMaintenanceStore) Load(context.Context) (domain.MaintenanceState, []domain.OperationPermit, error) {
	return s.state, s.permits, nil
}
func (s *outcomeMaintenanceStore) Save(_ context.Context, state domain.MaintenanceState, permits []domain.OperationPermit) error {
	s.state = state
	s.permits = permits
	return nil
}

type panicIdentity struct{ fixtureIdentity }

func (panicIdentity) Authenticate(context.Context, string) (domain.Principal, error) {
	panic("private panic")
}
func TestAuthenticatedUnknownRouteReleasesButRecoveredPanicRetainsPermit(t *testing.T) {
	for _, panics := range []bool{false, true} {
		ctx := t.Context()
		store := &outcomeMaintenanceStore{state: domain.MaintenanceState{Phase: "bootstrap", Fence: domain.MaintenanceFence{OperationID: "bootstrap", Generation: 1}}}
		maintenance, err := service.NewMaintenanceService(ctx, store)
		require.NoError(t, err)
		require.NoError(t, maintenance.SetPhase(ctx, store.state.Fence, "recovering"))
		require.NoError(t, maintenance.MarkRecoveryComplete(ctx, store.state.Fence))
		require.NoError(t, maintenance.Resume(ctx, store.state.Fence))
		var identity IdentityService = fixtureIdentity{}
		if panics {
			identity = panicIdentity{}
		}
		server, err := NewIdentityServer(identity, slog.New(slog.NewJSONHandler(io.Discard, nil)))
		require.NoError(t, err)
		handler := platform.AdmittedHandler(maintenance, "api-one", server)
		req := httptest.NewRequest("GET", "/unknown", nil)
		req.Header.Set("Authorization", "Bearer fixture-secret")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		assert.Equal(t, 503, response.Code)
		assert.NotContains(t, response.Body.String(), "private")
		fence, err := maintenance.Begin(ctx, "backup")
		require.NoError(t, err)
		wait, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
		err = maintenance.WaitDrained(wait, fence)
		cancel()
		if panics {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
		}
	}
}

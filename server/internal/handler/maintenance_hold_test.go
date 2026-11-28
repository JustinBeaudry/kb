package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/handler"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const holdSecret = "test-control-secret-0123456789012345"

type holdStore struct {
	state   domain.MaintenanceState
	permits []domain.OperationPermit
}

func (s *holdStore) Load(context.Context) (domain.MaintenanceState, []domain.OperationPermit, error) {
	return s.state, s.permits, nil
}
func (s *holdStore) Save(_ context.Context, state domain.MaintenanceState, p []domain.OperationPermit) error {
	s.state, s.permits = state, append([]domain.OperationPermit(nil), p...)
	return nil
}

func openHoldSupervisor(t *testing.T) *service.MaintenanceService {
	t.Helper()
	ctx := context.Background()
	m, err := service.NewMaintenanceService(ctx, &holdStore{state: domain.MaintenanceState{Phase: "bootstrap", Fence: domain.MaintenanceFence{OperationID: "bootstrap", Generation: 1}}})
	require.NoError(t, err)
	st, err := m.State(ctx)
	require.NoError(t, err)
	require.NoError(t, m.SetPhase(ctx, st.Fence, "recovering"))
	require.NoError(t, m.MarkRecoveryComplete(ctx, st.Fence))
	require.NoError(t, m.Resume(ctx, st.Fence))
	return m
}

func TestMaintenanceHoldTravelsOverTheControlAPI(t *testing.T) {
	domain.SetHoldOverrun(func(string) {})
	t.Cleanup(func() { domain.SetHoldOverrun(nil) })
	m := openHoldSupervisor(t)
	h, err := handler.NewMaintenanceHandler(m, holdSecret)
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	client, err := gateway.NewMaintenanceHTTPClient(srv.URL, holdSecret, "worker-one")
	require.NoError(t, err)
	held, release := domain.WithPermitHold(context.Background(), "worker")
	defer release(nil)
	p, err := client.Admit(held, "worker-one", "worker")
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(15*time.Minute), p.HoldUntil, 5*time.Second)
	plain, err := client.Admit(context.Background(), "worker-one", "worker")
	require.NoError(t, err)
	assert.True(t, plain.HoldUntil.IsZero())
	require.NoError(t, client.Finish(context.Background(), p))
	require.NoError(t, client.Finish(context.Background(), plain))
	st, err := m.State(context.Background())
	require.NoError(t, err)
	assert.Zero(t, st.Active)
}

func TestMaintenanceControlAPIRefusesAnOversizedHold(t *testing.T) {
	h, err := handler.NewMaintenanceHandler(openHoldSupervisor(t), holdSecret)
	require.NoError(t, err)
	for _, hold := range []string{"3601", "-1"} {
		r := httptest.NewRequest("POST", "/v1/admit", strings.NewReader(`{"instance_id":"api-one","kind":"api","hold_seconds":`+hold+`}`))
		r.Header.Set("Authorization", "Bearer "+holdSecret)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		assert.Equal(t, http.StatusBadRequest, w.Code, hold)
	}
}

// A supervisor from before holds decodes admission strictly and refuses the
// field. The holder then asks again without it and runs unheld.
func TestMaintenanceHolderFallsBackWhenTheSupervisorPredatesHolds(t *testing.T) {
	domain.SetHoldOverrun(func(string) {})
	t.Cleanup(func() { domain.SetHoldOverrun(nil) })
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var input struct {
			InstanceID string `json:"instance_id"`
			Kind       string `json:"kind"`
		}
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		w.Header().Set("Content-Type", "application/json")
		if d.Decode(&input) != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":"invalid_input"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(domain.OperationPermit{ID: "permit", InstanceID: input.InstanceID, Kind: input.Kind, Generation: 1})
	}))
	t.Cleanup(srv.Close)
	client, err := gateway.NewMaintenanceHTTPClient(srv.URL, holdSecret, "api-one")
	require.NoError(t, err)
	held, release := domain.WithPermitHold(context.Background(), "api")
	defer release(nil)
	p, err := client.Admit(held, "api-one", "api")
	require.NoError(t, err)
	assert.Equal(t, "permit", p.ID)
	assert.True(t, p.HoldUntil.IsZero())
	assert.Equal(t, 2, requests)
}

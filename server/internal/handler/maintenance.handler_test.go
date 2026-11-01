package handler_test

import (
	"context"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/handler"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"strings"
	"testing"
)

type admission struct{}

func (admission) Admit(_ context.Context, instance, kind string) (domain.OperationPermit, error) {
	return domain.OperationPermit{ID: "permit", InstanceID: instance, Kind: kind, Generation: 1}, nil
}
func (admission) Finish(context.Context, domain.OperationPermit) error { return nil }
func (admission) State(context.Context) (domain.MaintenanceState, error) {
	return domain.MaintenanceState{Phase: "open"}, nil
}
func TestMaintenanceServiceRoleCannotMintHostAuthority(t *testing.T) {
	h, err := handler.NewMaintenanceHandler(admission{}, "test-control-secret-0123456789012345")
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	client, err := gateway.NewMaintenanceHTTPClient(srv.URL, "test-control-secret-0123456789012345", "api-one")
	require.NoError(t, err)
	p, err := client.Admit(context.Background(), "api-one", "api")
	require.NoError(t, err)
	require.Equal(t, "permit", p.ID)
	_, err = client.Admit(context.Background(), "api-one", "host-admin:setup")
	require.ErrorIs(t, err, domain.ErrForbidden)
	_, err = client.Admit(context.Background(), "api-one", "startup-recovery")
	require.ErrorIs(t, err, domain.ErrForbidden)
	wrong, err := gateway.NewMaintenanceHTTPClient(srv.URL, "wrong-secret-012345678901234567890", "api-one")
	require.NoError(t, err)
	_, err = wrong.State(context.Background())
	require.ErrorIs(t, err, domain.ErrUnauthorized)
	require.NoError(t, client.Finish(context.Background(), p))
}
func TestMaintenanceServiceRoleCannotFinishHostPermit(t *testing.T) {
	h, err := handler.NewMaintenanceHandler(admission{}, "test-control-secret-0123456789012345")
	require.NoError(t, err)
	r := httptest.NewRequest("POST", "/v1/finish", strings.NewReader(`{"id":"host","instance_id":"host","kind":"host-admin:setup","generation":1}`))
	r.Header.Set("Authorization", "Bearer test-control-secret-0123456789012345")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	require.Equal(t, 403, w.Code)
}

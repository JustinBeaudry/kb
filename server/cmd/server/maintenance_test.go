package main

import (
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/handler"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func maintenanceFixture(t *testing.T, open bool) *service.MaintenanceService {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0700))
	store, err := gateway.NewMaintenanceStateStore(filepath.Join(dir, "state.json"), true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	control, err := service.NewMaintenanceService(t.Context(), store)
	require.NoError(t, err)
	if open {
		state, err := control.State(t.Context())
		require.NoError(t, err)
		require.NoError(t, control.SetPhase(t.Context(), state.Fence, "recovering"))
		require.NoError(t, control.MarkRecoveryComplete(t.Context(), state.Fence))
		require.NoError(t, control.Resume(t.Context(), state.Fence))
	}
	secret := strings.Repeat("fixture-only", 4)
	h, err := handler.NewMaintenanceHandler(control, secret)
	require.NoError(t, err)
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	path := filepath.Join(dir, "secret")
	require.NoError(t, os.WriteFile(path, []byte(secret), 0600))
	t.Setenv("KB_MAINTENANCE_ENDPOINT", server.URL)
	t.Setenv("KB_MAINTENANCE_SECRET_FILE", path)
	t.Setenv("KB_INSTANCE_ID", "fixture-process")
	return control
}

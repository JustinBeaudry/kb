package repository_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/require"
)

// The compiled operator client and fixed child use the same production socket,
// peer checks, recovery admission, inherited handshake and completion protocol.
func identityHostFixture(t *testing.T, graphURI, graphSecret string) (string, *service.MaintenanceService) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "kb-identity-host-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	dir, err = filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0700))
	binary := filepath.Join(dir, "kb-admin")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "../../cmd/kb-admin")
	output, err := build.CombinedOutput()
	require.NoError(t, err, string(output))
	stateStore, err := gateway.NewMaintenanceStateStore(filepath.Join(dir, "maintenance.json"), true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stateStore.Close()) })
	maintenance, err := service.NewMaintenanceService(t.Context(), stateStore)
	require.NoError(t, err)
	executor, err := gateway.NewHostAdminProcessExecutor(binary, maintenance)
	require.NoError(t, err)
	host := service.NewHostAdminService(maintenance, executor, "identity-host")
	socket := filepath.Join(dir, "admin.sock")
	server, err := gateway.NewUnixHostAdminServer(socket, os.Geteuid(), host)
	require.NoError(t, err)
	for key, value := range map[string]string{
		"KB_GRAPH_URI": graphURI, "KB_GRAPH_DATABASE": "neo4j", "KB_GRAPH_USER": "neo4j",
		"KB_GRAPH_SECRET": graphSecret, "KB_GRAPH_SECRET_FILE": "",
		"KB_LISTEN": "127.0.0.1:8080", "KB_TLS_CERT": "", "KB_TLS_KEY": "", "KB_INGRESS_TRANSPORT": "direct",
		"KB_HOST_ADMIN_SOCKET": socket, "KB_HOST_EXECUTION_FD": "",
	} {
		t.Setenv(key, value)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	t.Cleanup(func() { cancel(); _ = server.Close(); require.NoError(t, <-done) })
	return binary, maintenance
}

func assertIdentityHostDrained(t *testing.T, maintenance *service.MaintenanceService) {
	t.Helper()
	state, err := maintenance.State(t.Context())
	require.NoError(t, err)
	require.Equal(t, "bootstrap", state.Phase, "host setup cannot open ordinary admission")
	require.Zero(t, state.Active, "child must explicitly complete after joined cleanup")
	require.Zero(t, state.Unknown, "known rejected and completed setup must settle")
	require.NoError(t, maintenance.WaitDrained(t.Context(), state.Fence))
}

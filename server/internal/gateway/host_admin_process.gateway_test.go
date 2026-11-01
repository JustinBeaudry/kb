package gateway_test

import (
	"context"
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type permitValidator struct{ deny bool }

func (v permitValidator) ValidatePermit(context.Context, domain.OperationPermit) error {
	if v.deny {
		return domain.ErrMaintenance
	}
	return nil
}
func buildHostChild(t *testing.T) string {
	t.Helper()
	if fixture := os.Getenv("KB_TEST_HOST_CHILD"); fixture != "" {
		return fixture
	}
	binary := filepath.Join(t.TempDir(), "kb-admin")
	cmd := exec.CommandContext(context.Background(), "go", "build", "-o", binary, "./testdata/hostchild")
	data, err := cmd.CombinedOutput()
	require.NoError(t, err, string(data))
	return binary
}
func TestHostProcessRequiresBoundActionAndExplicitCompletion(t *testing.T) {
	binary := buildHostChild(t)
	for _, test := range []struct {
		action    string
		completed bool
	}{{"journal-check", true}, {"policy", false}, {"list", false}, {"revoke", false}} {
		t.Run(test.action, func(t *testing.T) {
			executor, err := gateway.NewHostAdminProcessExecutor(binary, permitValidator{})
			require.NoError(t, err)
			r, err := executor.Execute(context.Background(), domain.HostAdminRequest{Version: 1, RequestID: "one", Action: test.action}, domain.OperationPermit{ID: "p"}, domain.HostAdminFiles{})
			assert.Equal(t, test.completed, r.Completed)
			if test.completed {
				require.NoError(t, err)
				assert.JSONEq(t, `{"ok":true}`, string(r.ResultJSON))
			} else {
				require.Error(t, err)
			}
		})
	}
}
func TestHostProcessTransfersCredentialOnlyByInheritedDescriptor(t *testing.T) {
	binary := buildHostChild(t)
	executor, err := gateway.NewHostAdminProcessExecutor(binary, permitValidator{})
	require.NoError(t, err)
	f, err := os.CreateTemp(t.TempDir(), "sink")
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	r, err := executor.Execute(context.Background(), domain.HostAdminRequest{Version: 1, RequestID: "one", Action: "enroll"}, domain.OperationPermit{ID: "p"}, domain.HostAdminFiles{CredentialSink: f})
	require.NoError(t, err)
	assert.True(t, r.Completed)
	assert.NotContains(t, string(r.ResultJSON), "child-bearer")
	_, err = f.Seek(0, 0)
	require.NoError(t, err)
	data := make([]byte, 64)
	n, err := f.Read(data)
	require.NoError(t, err)
	assert.Equal(t, "child-bearer-never-stdout", string(data[:n]))
}
func TestHostProcessDeniesChangedPermitBeforeExternalWork(t *testing.T) {
	executor, err := gateway.NewHostAdminProcessExecutor(buildHostChild(t), permitValidator{deny: true})
	require.NoError(t, err)
	r, err := executor.Execute(context.Background(), domain.HostAdminRequest{Version: 1, RequestID: "one", Action: "journal-check"}, domain.OperationPermit{}, domain.HostAdminFiles{})
	require.Error(t, err)
	assert.False(t, r.Completed)
	assert.Empty(t, r.ResultJSON)
}

func TestHostProcessKnownFailureCompletionDoesNotCreateUnknown(t *testing.T) {
	executor, err := gateway.NewHostAdminProcessExecutor(buildHostChild(t), permitValidator{})
	require.NoError(t, err)
	r, err := executor.Execute(context.Background(), domain.HostAdminRequest{Version: 1, RequestID: "one", Action: "journal-init"}, domain.OperationPermit{}, domain.HostAdminFiles{})
	require.ErrorIs(t, err, domain.ErrConflict)
	assert.True(t, r.Completed)
	assert.Equal(t, "conflict", r.Code)
}
func TestHostServiceRealExecutorFinishesProvenPreStartFailures(t *testing.T) {
	for _, kind := range []string{"oversized-option", "missing-executable", "child-crash"} {
		t.Run(kind, func(t *testing.T) {
			binary := buildHostChild(t)
			if kind == "missing-executable" {
				contents, err := os.ReadFile(binary)
				require.NoError(t, err)
				binary = filepath.Join(t.TempDir(), "ephemeral-admin")
				require.NoError(t, os.WriteFile(binary, contents, 0700))
			}
			dir, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			require.NoError(t, os.Chmod(dir, 0700))
			store, err := gateway.NewMaintenanceStateStore(filepath.Join(dir, "maintenance.json"), true)
			require.NoError(t, err)
			t.Cleanup(func() { _ = store.Close() })
			maintenance, err := service.NewMaintenanceService(context.Background(), store)
			require.NoError(t, err)
			state, err := maintenance.State(context.Background())
			require.NoError(t, err)
			require.NoError(t, maintenance.SetPhase(context.Background(), state.Fence, "recovering"))
			require.NoError(t, maintenance.MarkRecoveryComplete(context.Background(), state.Fence))
			require.NoError(t, maintenance.Resume(context.Background(), state.Fence))
			executor, err := gateway.NewHostAdminProcessExecutor(binary, maintenance)
			require.NoError(t, err)
			request := domain.HostAdminRequest{Version: 1, RequestID: "prestart", Action: "list"}
			wantError := domain.ErrInvalidInput
			wantUnknown := 0
			switch kind {
			case "oversized-option":
				request.Options.After = strings.Repeat("a", 4097)
			case "missing-executable":
				require.NoError(t, os.Remove(binary))
				request.Action = "journal-check"
				wantError = domain.ErrUnavailable
			case "child-crash":
				request.Action = "policy"
				wantError = domain.ErrUnknownOutcome
				wantUnknown = 1
			}
			host := service.NewHostAdminService(maintenance, executor, "host-test")
			_, err = host.Run(context.Background(), request, nil)
			require.ErrorIs(t, err, wantError)
			state, err = maintenance.State(context.Background())
			require.NoError(t, err)
			assert.Zero(t, state.Active)
			assert.Equal(t, wantUnknown, state.Unknown)
		})
	}
}

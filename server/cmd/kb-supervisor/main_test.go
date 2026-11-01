package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type operatorFake struct{ calls int }

func (f *operatorFake) Run(context.Context, string) (domain.BackupResult, error) {
	f.calls++
	return domain.BackupResult{State: "complete"}, nil
}
func (f *operatorFake) Restore(context.Context, string, string) (domain.BackupResult, error) {
	f.calls++
	return domain.BackupResult{State: "complete"}, nil
}
func (f *operatorFake) Recover(context.Context, string) error { f.calls++; return nil }
func (f *operatorFake) Pause(context.Context, string) error   { f.calls++; return nil }
func TestSupervisorOperatorUsesProtectedLocalSocket(t *testing.T) {
	temporary, e := os.MkdirTemp("", "kb-operator-")
	require.NoError(t, e)
	t.Cleanup(func() { _ = os.RemoveAll(temporary) })
	root, e := filepath.EvalSymlinks(temporary)
	require.NoError(t, e)
	dir := filepath.Join(root, "control")
	require.NoError(t, os.Mkdir(dir, 0700))
	path := filepath.Join(dir, "backup.sock")
	listener, e := gateway.ListenProtectedUnix(path, os.Geteuid())
	require.NoError(t, e)
	t.Cleanup(func() { _ = listener.Close() })
	fake := &operatorFake{}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		_ = serveOperator(ctx, listener, fake, func(context.Context) (operatorStatus, error) { return operatorStatus{}, nil })
	}()
	var output bytes.Buffer
	require.NoError(t, callOperator(t.Context(), path, operatorRequest{Version: 1, Action: "backup", ID: "backup-1"}, &output))
	var response operatorResponse
	require.NoError(t, json.Unmarshal(output.Bytes(), &response))
	assert.Equal(t, "complete", response.Result.State)
	assert.Equal(t, 1, fake.calls)
}
func TestSupervisorOperatorRejectsUnknownAction(t *testing.T) {
	fake := &operatorFake{}
	result := dispatchOperator(t.Context(), fake, func(context.Context) (operatorStatus, error) { return operatorStatus{}, nil }, operatorRequest{Version: 1, Action: "shell", ID: "id"})
	assert.Equal(t, "invalid_input", result.Code)
	assert.Zero(t, fake.calls)
}
func TestBackupConfigRejectsMixedSecretSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(path, []byte("private"), 0600))
	t.Setenv("KB_BACKUP_ACCESS_KEY", "plain")
	t.Setenv("KB_BACKUP_ACCESS_KEY_FILE", path)
	_, e := backupSecret("KB_BACKUP_ACCESS_KEY")
	require.ErrorIs(t, e, domain.ErrInvalidInput)
	assert.NotContains(t, e.Error(), "private")
}

func TestSupervisorHelpNeedsNoConfigurationOrIO(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"serve", "--help"}, {"restore", "--help"}, {"help"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "must-not-exist")
			t.Setenv("KB_SUPERVISOR_ROOT", root)
			t.Setenv("KB_GRAPH_URI", "")
			var output bytes.Buffer
			require.NoError(t, run(t.Context(), args, &output))
			assert.Contains(t, output.String(), "pause --id")
			assert.Contains(t, output.String(), "restore --backup-id")
			assert.Contains(t, output.String(), "docs/runbooks/recovery.md")
			assert.NoDirExists(t, root)
		})
	}
}

var _ io.Writer = (*bytes.Buffer)(nil)

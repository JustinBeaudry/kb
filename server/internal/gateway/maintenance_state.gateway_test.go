package gateway_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/JustinBeaudry/kb/server/internal/gateway"
	"github.com/JustinBeaudry/kb/server/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMaintenanceFileStorePersistsUnknownAcrossRestart(t *testing.T) {
	ctx := context.Background()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0700))
	path := filepath.Join(dir, "state.json")
	_, err = gateway.NewMaintenanceStateStore(path, false)
	require.Error(t, err)
	store, err := gateway.NewMaintenanceStateStore(path, true)
	require.NoError(t, err)
	s, err := service.NewMaintenanceService(ctx, store)
	require.NoError(t, err)
	st, _ := s.State(ctx)
	require.NoError(t, s.SetPhase(ctx, st.Fence, "recovering"))
	require.NoError(t, s.MarkRecoveryComplete(ctx, st.Fence))
	require.NoError(t, s.Resume(ctx, st.Fence))
	_, err = s.Admit(ctx, "api", "upload")
	require.NoError(t, err)
	store, err = gateway.NewMaintenanceStateStore(path, false)
	require.NoError(t, err)
	s, err = service.NewMaintenanceService(ctx, store)
	require.NoError(t, err)
	st, _ = s.State(ctx)
	assert.Equal(t, 1, st.Unknown)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
}
func TestMaintenanceFileStoreRejectsCorruptionPermissionsAndSymlink(t *testing.T) {
	for _, kind := range []string{"symlink", "public", "corrupt", "missing"} {
		t.Run(kind, func(t *testing.T) {
			dir, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			require.NoError(t, os.Chmod(dir, 0700))
			path := filepath.Join(dir, "state.json")
			switch kind {
			case "symlink":
				require.NoError(t, os.WriteFile(filepath.Join(dir, "target"), []byte("{}"), 0600))
				require.NoError(t, os.Symlink("target", path))
			case "public":
				require.NoError(t, os.WriteFile(path, []byte("{}"), 0644))
			case "corrupt":
				require.NoError(t, os.WriteFile(path, []byte("{"), 0600))
			}
			_, err = gateway.NewMaintenanceStateStore(path, false)
			require.Error(t, err)
		})
	}
}
func TestMaintenanceFileStoreRejectsUnknownVersion(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0700))
	path := filepath.Join(dir, "state.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"version":99,"state":{},"permits":[]}`), 0600))
	_, err = gateway.NewMaintenanceStateStore(path, false)
	require.ErrorIs(t, err, domain.ErrUnavailable)
}

package gateway

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBackupPhasePersistenceNeverTreatsMissingHistoryAsFresh(t *testing.T) {
	root, e := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, e)
	dir := filepath.Join(root, "private")
	require.NoError(t, os.Mkdir(dir, 0700))
	path := filepath.Join(dir, "backup.json")
	_, err := NewBackupStateStore(path, false)
	require.Error(t, err)
	s, err := NewBackupStateStore(path, true)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	op := domain.BackupOperation{Version: 1, ID: "backup-1", Stage: "resuming", TerminalState: "recovered", StartedAt: time.Now().UTC()}
	require.NoError(t, s.SaveOperation(t.Context(), op))
	schedule := domain.BackupSchedule{AttemptID: "backup-1", Failures: 1}
	require.NoError(t, s.SaveSchedule(t.Context(), schedule))
	reopened, err := NewBackupStateStore(path, false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	got, err := reopened.LoadOperation(t.Context())
	require.NoError(t, err)
	assert.Equal(t, op, got)
	gotSchedule, err := reopened.LoadSchedule(t.Context())
	require.NoError(t, err)
	assert.Equal(t, schedule, gotSchedule)
}
func TestBackupPhaseStoreRejectsTamperedOrSymlinkState(t *testing.T) {
	for _, variant := range []string{"corrupt", "symlink"} {
		t.Run(variant, func(t *testing.T) {
			root, e := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, e)
			dir := filepath.Join(root, "private")
			require.NoError(t, os.Mkdir(dir, 0700))
			path := filepath.Join(dir, "backup.json")
			if variant == "corrupt" {
				require.NoError(t, os.WriteFile(path, []byte(`{"version":4}`), 0600))
			} else {
				require.NoError(t, os.Symlink("missing", path))
			}
			_, err := NewBackupStateStore(path, true)
			require.Error(t, err)
		})
	}
}

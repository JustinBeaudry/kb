package gateway

import (
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRestoreRetryAfterGatewayRestartRebuildsOnlyOwnedStage(t *testing.T) {
	first, store, cfg, m, data, system := archiveSetup(t)
	require.NoError(t, first.Write(t.Context(), m, data, system))
	target := filepath.Join(cfg.ScratchRoot, "restore-persisted-incarnation")
	_, err := first.Restore(t.Context(), m.BackupID, target)
	require.NoError(t, err)
	// Simulate a supervisor crash after staging publication: no CleanupRestore,
	// no active recovery permit, and the replacement gateway has no stage memory.
	require.NoError(t, os.WriteFile(filepath.Join(target, backupObjectName(m.Objects[0])), []byte("damaged-stage"), 0600))
	restarted, err := NewBackupArchive(store, store, cfg)
	require.NoError(t, err)
	restored, err := restarted.Restore(t.Context(), m.BackupID, target)
	require.NoError(t, err)
	assert.Equal(t, m.BackupID, restored.BackupID)
	body, err := restarted.OpenRestoreObject(t.Context(), m.Objects[0])
	require.NoError(t, err)
	content, err := io.ReadAll(body)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	assert.Equal(t, "pinned-evidence", string(content))
	assert.Equal(t, target, restarted.stage)
}
func TestRestoreRetryRefusesOutsideScratchOrSymlinkCustody(t *testing.T) {
	for _, variant := range []string{"outside", "outside-new", "symlink", "public-stage", "symlink-child"} {
		t.Run(variant, func(t *testing.T) {
			a, store, cfg, m, data, system := archiveSetup(t)
			require.NoError(t, a.Write(t.Context(), m, data, system))
			other := filepath.Join(t.TempDir(), "graph-generation")
			require.NoError(t, os.Mkdir(other, 0700))
			sentinel := filepath.Join(other, "must-remain")
			require.NoError(t, os.WriteFile(sentinel, []byte("graph-must-remain"), 0600))
			target := filepath.Join(cfg.ScratchRoot, "restore-custody")
			switch variant {
			case "outside":
				target = other
			case "outside-new":
				target = filepath.Join(other, "restore-not-scratch")
			case "symlink":
				require.NoError(t, os.Symlink(other, target))
			case "public-stage":
				require.NoError(t, os.Mkdir(target, 0755))
			case "symlink-child":
				require.NoError(t, os.Mkdir(target, 0700))
				require.NoError(t, os.Symlink(other, filepath.Join(target, "escape")))
			}
			restarted, err := NewBackupArchive(store, store, cfg)
			require.NoError(t, err)
			_, err = restarted.Restore(t.Context(), m.BackupID, target)
			require.Error(t, err)
			content, err := os.ReadFile(sentinel)
			require.NoError(t, err)
			assert.Equal(t, "graph-must-remain", string(content))
		})
	}
}

func TestRestoreStageCustodyRejectsDifferentOwnerWithoutPrivilegedFixtures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owned-file")
	require.NoError(t, os.WriteFile(path, []byte("private"), 0600))
	info, err := os.Lstat(path)
	require.NoError(t, err)
	require.NoError(t, validateRestoreStageInfo(info, uint32(os.Geteuid())))
	require.ErrorIs(t, validateRestoreStageInfo(info, uint32(os.Geteuid())+1), domain.ErrForbidden)
}
func TestRestoreRetryRefusesUnboundedExistingStage(t *testing.T) {
	a, store, cfg, m, data, system := archiveSetup(t)
	require.NoError(t, a.Write(t.Context(), m, data, system))
	target := filepath.Join(cfg.ScratchRoot, "restore-oversized")
	_, err := a.Restore(t.Context(), m.BackupID, target)
	require.NoError(t, err)
	require.NoError(t, os.Truncate(filepath.Join(target, "neo4j.dump"), cfg.MaxArchiveBytes+1))
	restarted, err := NewBackupArchive(store, store, cfg)
	require.NoError(t, err)
	_, err = restarted.Restore(t.Context(), m.BackupID, target)
	require.ErrorIs(t, err, domain.ErrBudget)
	assert.FileExists(t, filepath.Join(target, "neo4j.dump"))
}

package gateway

import (
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestBackupCapacityRefusesUnknownAndInsufficientSpace(t *testing.T) {
	dir := t.TempDir()
	free, total, e := BackupDiskSpace(dir)
	require.NoError(t, e)
	require.Positive(t, free)
	require.GreaterOrEqual(t, total, free)
	require.ErrorIs(t, checkBackupSpace(dir, int64(^uint64(0)>>1)), domain.ErrBudget)
	require.Error(t, checkBackupSpace(dir+"/absent", 1))
}

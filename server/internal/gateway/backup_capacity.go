package gateway

import (
	"github.com/JustinBeaudry/kb/server/internal/domain"
	"golang.org/x/sys/unix"
)

// BackupDiskSpace excludes reserved filesystem blocks from the backup budget.
func BackupDiskSpace(path string) (available, total uint64, err error) {
	var stat unix.Statfs_t
	if unix.Statfs(path, &stat) != nil || stat.Bsize <= 0 || stat.Blocks == 0 {
		return 0, 0, domain.ErrUnavailable
	}
	block := uint64(stat.Bsize)
	if uint64(stat.Bavail) > ^uint64(0)/block || uint64(stat.Blocks) > ^uint64(0)/block {
		return 0, 0, domain.ErrUnavailable
	}
	return uint64(stat.Bavail) * block, uint64(stat.Blocks) * block, nil
}
func checkBackupSpace(path string, required int64) error {
	if required < 0 {
		return domain.ErrBudget
	}
	available, _, err := BackupDiskSpace(path)
	if err != nil {
		return err
	}
	if available < uint64(required) {
		return domain.ErrBudget
	}
	return nil
}

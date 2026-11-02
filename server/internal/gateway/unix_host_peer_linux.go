//go:build linux

package gateway

import (
	"golang.org/x/sys/unix"
	"os"
)

func unixPeer(fd int) (int, int, error) {
	c, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, 0, err
	}
	return int(c.Uid), int(c.Pid), nil
}
func anonymousCredentialPipe(fd int, _ os.FileInfo) bool {
	var stat unix.Statfs_t
	return unix.Fstatfs(fd, &stat) == nil && uint64(stat.Type) == uint64(unix.PIPEFS_MAGIC)
}

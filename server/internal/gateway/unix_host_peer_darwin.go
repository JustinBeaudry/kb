//go:build darwin

package gateway

import (
	"golang.org/x/sys/unix"
	"os"
	"syscall"
)

func unixPeer(fd int) (int, int, error) {
	cred, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return 0, 0, err
	}
	pid, err := unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	return int(cred.Uid), pid, err
}
func anonymousCredentialPipe(_ int, info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 0
}

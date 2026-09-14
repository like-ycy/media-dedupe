//go:build darwin || linux || freebsd || netbsd || openbsd

package fsutil

import "syscall"

func extractSys(stat any) (inode uint64, device uint64) {
	st, ok := stat.(*syscall.Stat_t)
	if !ok || st == nil {
		return 0, 0
	}
	return uint64(st.Ino), uint64(st.Dev)
}

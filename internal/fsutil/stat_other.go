//go:build !darwin && !linux && !freebsd && !netbsd && !openbsd

package fsutil

func extractSys(stat any) (inode uint64, device uint64) {
	return 0, 0
}

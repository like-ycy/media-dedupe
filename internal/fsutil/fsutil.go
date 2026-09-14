package fsutil

import (
	"os"
	"path/filepath"
)

// ExpandPath expands leading ~ to home directory.
func ExpandPath(path string) string {
	if path == "~" || len(path) >= 2 && path[0] == '~' && (path[1] == '/' || path[1] == filepath.Separator) {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		if path == "~" {
			return home
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

// SafeStat returns size/mtime/inode/device for a path.
func SafeStat(path string) (size int64, mtimeNs int64, inode uint64, device uint64, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	stat := info.Sys()
	size = info.Size()
	mtimeNs = info.ModTime().UnixNano()
	inode, device = extractSys(stat)
	return size, mtimeNs, inode, device, nil
}

// EnsureDir creates dir and parents if needed.
func EnsureDir(path string) error {
	return os.MkdirAll(path, 0o755)
}

// FileExists reports whether path exists and is a regular file.
func FileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

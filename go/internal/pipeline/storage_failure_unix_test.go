//go:build unix

package pipeline

import "syscall"

// storageFailure is an error the platform classifies as a failing volume. Tests
// that assert "this aborts the scan" need one per platform: errno values mean
// nothing to the Windows classifier, and Win32 codes mean nothing to the unix one.
func storageFailure() error { return syscall.EIO }

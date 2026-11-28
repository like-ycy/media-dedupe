//go:build unix

package pipeline

import (
	"syscall"
	"testing"
)

// The unix classifier has to keep recognizing every errno it claims to.
func TestIsFatalIOErrorUnixErrnos(t *testing.T) {
	for _, err := range []error{
		syscall.EIO, syscall.ENODEV, syscall.ENOSPC, syscall.EDQUOT,
		syscall.EROFS, syscall.EMFILE, syscall.ENFILE, syscall.ENOMEM,
	} {
		if !isFatalIOError(err) {
			t.Fatalf("%v should abort the scan", err)
		}
	}
}

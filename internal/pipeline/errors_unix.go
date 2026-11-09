//go:build unix

package pipeline

import (
	"context"
	"errors"
	"syscall"
)

var (
	contextCanceled = context.Canceled
	contextDeadline = context.DeadlineExceeded
)

// fatalStorageError recognizes errno values that signal a failing volume or an
// exhausted system resource rather than a bad file.
func fatalStorageError(err error) bool {
	for _, target := range []error{
		syscall.EIO,
		syscall.ENODEV,
		syscall.ENOSPC,
		syscall.EDQUOT,
		syscall.EROFS,
		syscall.EMFILE,
		syscall.ENFILE,
		syscall.ENOMEM,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

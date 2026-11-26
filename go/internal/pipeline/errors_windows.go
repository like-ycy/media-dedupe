//go:build windows

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

// Win32 error codes that signal a failing volume or an exhausted system
// resource rather than a bad file. Values come from winerror.h; syscall does
// not expose them as named constants.
const (
	winErrorTooManyOpenFiles   syscall.Errno = 4
	winErrorNotEnoughMemory    syscall.Errno = 8
	winErrorWriteProtect       syscall.Errno = 19
	winErrorNotReady           syscall.Errno = 21
	winErrorCRC                syscall.Errno = 23
	winErrorHandleDiskFull     syscall.Errno = 39
	winErrorDiskFull           syscall.Errno = 112
	winErrorIODevice           syscall.Errno = 1117
	winErrorDeviceNotConnected syscall.Errno = 1167
)

// fatalStorageError recognizes Win32 errors that mean the storage layer is
// unhealthy.
func fatalStorageError(err error) bool {
	for _, target := range []error{
		winErrorIODevice,
		winErrorDiskFull,
		winErrorHandleDiskFull,
		winErrorCRC,
		winErrorWriteProtect,
		winErrorNotEnoughMemory,
		winErrorTooManyOpenFiles,
		winErrorDeviceNotConnected,
		winErrorNotReady,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

//go:build windows

package pipeline

import "testing"

// Same contract on Windows, where the codes come from winerror.h instead.
func TestIsFatalIOErrorWin32Codes(t *testing.T) {
	for _, err := range []error{
		winErrorIODevice, winErrorDiskFull, winErrorHandleDiskFull, winErrorCRC,
		winErrorWriteProtect, winErrorNotEnoughMemory, winErrorTooManyOpenFiles,
		winErrorDeviceNotConnected, winErrorNotReady,
	} {
		if !isFatalIOError(err) {
			t.Fatalf("%v should abort the scan", err)
		}
	}
}

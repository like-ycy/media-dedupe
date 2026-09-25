//go:build windows

package ops

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const (
	foDelete           = 0x0003
	fnofSilent         = 0x0004
	fnofNoConfirmation = 0x0010
	fnofAllowUndo      = 0x0040
	fnofNoErrorUI      = 0x0400
)

// shFileOpStructW matches SHFILEOPSTRUCTW on both 386 and amd64 via Go's natural alignment.
type shFileOpStructW struct {
	hwnd                  uintptr
	wFunc                 uint32
	pFrom                 *uint16
	pTo                   *uint16
	fFlags                uint16
	fAnyOperationsAborted int32
	hNameMappings         uintptr
	lpszProgressTitle     *uint16
}

var (
	shell32              = syscall.NewLazyDLL("shell32.dll")
	procSHFileOperationW = shell32.NewProc("SHFileOperationW")
)

// trashPath moves one absolute path into the Recycle Bin via shell32.
// No subprocess is spawned, so GUI builds never flash a console window.
func trashPath(path string) error {
	buf, err := doubleNullUTF16(path)
	if err != nil {
		return err
	}
	op := shFileOpStructW{
		wFunc: foDelete,
		pFrom: &buf[0],
		// Recycle (not permanent) + stay silent; failures surface as errors here.
		fFlags: fnofAllowUndo | fnofNoConfirmation | fnofSilent | fnofNoErrorUI,
	}
	// SHFileOperationW returns 0 on success; non-zero is a DE_* code, not GetLastError.
	r1, _, _ := procSHFileOperationW.Call(uintptr(unsafe.Pointer(&op)))
	if r1 != 0 {
		return fmt.Errorf("recycle failed: SHFileOperationW code %d", r1)
	}
	if op.fAnyOperationsAborted != 0 {
		return fmt.Errorf("recycle aborted: %s", path)
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("recycle failed: file still exists: %s", path)
	}
	return nil
}

// doubleNullUTF16 builds a SHFileOperation pFrom buffer: path + NUL + extra NUL.
func doubleNullUTF16(path string) ([]uint16, error) {
	u, err := syscall.UTF16FromString(path)
	if err != nil {
		return nil, err
	}
	// UTF16FromString already appends one NUL; SHFileOperation wants double-NUL terminated lists.
	return append(u, 0), nil
}

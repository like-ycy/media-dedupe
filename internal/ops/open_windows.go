//go:build windows

package ops

import (
	"fmt"
	"syscall"
	"unsafe"
)

const swShownormal = 1

var procShellExecuteW = shell32.NewProc("ShellExecuteW")

// openPath asks the shell to open path exactly like a double-click, so file
// associations (e.g. Windows 11 Photos) and Unicode paths both work.
// rundll32 url.dll,FileProtocolHandler mangles non-ASCII paths and bypasses
// some app associations, so it is not used here.
func openPath(path string) error {
	verb, err := syscall.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	ret, _, callErr := procShellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		0,
		0,
		uintptr(swShownormal),
	)
	if ret > 32 {
		return nil
	}
	if errno, ok := callErr.(syscall.Errno); ok && errno != 0 {
		return fmt.Errorf("shell open failed: %w", errno)
	}
	return fmt.Errorf("shell open failed: code %d", ret)
}

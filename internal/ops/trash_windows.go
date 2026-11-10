//go:build windows

package ops

import (
	"fmt"
	"runtime"
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

func init() {
	trashPathsBatch = recycleBatch
	// syscall.UTF16FromString encodes with WTF-8 awareness: unpaired surrogates
	// survive instead of turning into U+FFFD. Windows file names may contain
	// them, and the difference is the difference between deleting the intended
	// file and deleting a namesake. It also rejects embedded NUL itself.
	encodePathUTF16 = func(path string) ([]uint16, error) {
		u, err := syscall.UTF16FromString(path)
		if err != nil {
			return nil, err
		}
		// UTF16FromString appends one NUL; the caller adds the second.
		return u[:len(u)-1], nil
	}
}

// trashPath moves one absolute path into the Recycle Bin via shell32.
// No subprocess is spawned, so GUI builds never flash a console window.
func trashPath(path string) error {
	if errs := recycleBatch([]string{path}); errs[path] != nil {
		return errs[path]
	}
	return nil
}

// recycleBatch recycles every path with a single SHFileOperationW call.
// One shell round trip per batch instead of per file is what makes bulk delete
// fast: each individual call costs tens of milliseconds of shell setup, which
// used to add up to tens of seconds for a few hundred files.
//
// SHFileOperationW reports success for the call as a whole, so every path is
// verified afterwards; if the batch fails, only the paths still in place are
// retried individually so per-item error messages stay exact.
func recycleBatch(paths []string) map[string]error {
	failed := make(map[string]error, len(paths))
	if len(paths) == 0 {
		return failed
	}
	buf, err := doubleNullListUTF16(paths)
	if err != nil {
		for _, p := range paths {
			failed[p] = err
		}
		return failed
	}
	op := shFileOpStructW{
		wFunc: foDelete,
		pFrom: &buf[0],
		// Recycle (not permanent) + stay silent; failures surface as errors here.
		fFlags: fnofAllowUndo | fnofNoConfirmation | fnofSilent | fnofNoErrorUI,
	}
	r1, _, _ := procSHFileOperationW.Call(uintptr(unsafe.Pointer(&op)))
	runtime.KeepAlive(buf)
	runtime.KeepAlive(&op)
	if r1 != 0 || op.fAnyOperationsAborted != 0 {
		// A batch error does not mean nothing happened: the shell often skips a
		// locked file and still removes the rest. Retry only the survivors so a
		// single bad entry cannot drag the whole batch back to 500 calls.
		for _, p := range paths {
			ok, statErr := gone(p)
			if statErr != nil {
				failed[p] = statErr
				continue
			}
			if ok {
				continue
			}
			if err := recycleOne(p); err != nil {
				failed[p] = err
			}
		}
		return failed
	}
	// The shell reported success; confirm each file is really gone so the
	// caller still gets an accurate per-path result.
	for _, p := range paths {
		ok, statErr := gone(p)
		if statErr != nil {
			// Cannot confirm either way: report the stat error rather than
			// claiming a success we did not verify.
			failed[p] = statErr
			continue
		}
		if !ok {
			failed[p] = fmt.Errorf("recycle failed: file still exists: %s", p)
		}
	}
	return failed
}

func recycleOne(path string) error {
	buf, err := doubleNullListUTF16([]string{path})
	if err != nil {
		return err
	}
	op := shFileOpStructW{
		wFunc:  foDelete,
		pFrom:  &buf[0],
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
	ok, statErr := gone(path)
	if statErr != nil {
		return statErr
	}
	if !ok {
		return fmt.Errorf("recycle failed: file still exists: %s", path)
	}
	return nil
}

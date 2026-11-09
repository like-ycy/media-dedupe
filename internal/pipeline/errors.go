package pipeline

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"sync"
)

// failureTracker records files that must be skipped and the first
// environment-level failure.
//
// File-level problems (missing, unreadable, corrupt or unsupported media) only
// skip that file. Storage-level problems (disk I/O errors, no space left, too
// many open files) abort the whole scan so a dying disk is never reported as a
// handful of unrelated bad files.
type failureTracker struct {
	mu    sync.Mutex
	paths map[string]struct{}
	fatal error
}

func newFailureTracker() *failureTracker {
	return &failureTracker{paths: make(map[string]struct{})}
}

func (t *failureTracker) mark(path string) {
	t.mu.Lock()
	t.paths[path] = struct{}{}
	t.mu.Unlock()
}

func (t *failureTracker) failed(path string) bool {
	t.mu.Lock()
	_, ok := t.paths[path]
	t.mu.Unlock()
	return ok
}

func (t *failureTracker) setFatal(err error) {
	if err == nil {
		return
	}
	t.mu.Lock()
	if t.fatal == nil {
		t.fatal = err
	}
	t.mu.Unlock()
}

func (t *failureTracker) fatalErr() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.fatal
}

func (t *failureTracker) failedPaths() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, 0, len(t.paths))
	for path := range t.paths {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// scanHooks centralizes per-file and environment failure handling for a scan.
type scanHooks struct {
	record   func(path, stage, message string)
	failures *failureTracker
	cancel   func()
}

// file skips a file whose own data could not be read. A storage-level error is
// promoted to a fatal scan failure instead.
func (h *scanHooks) file(path, stage string, err error) {
	if err == nil {
		return
	}
	if isFatalIOError(err) {
		h.fatal(path, stage, err)
		return
	}
	h.failures.mark(path)
	h.record(path, stage, err.Error())
}

// fatal stops the scan: the environment (disk, cache, program state) is broken.
func (h *scanHooks) fatal(path, stage string, err error) {
	if err == nil {
		return
	}
	h.record(path, stage, err.Error())
	h.failures.setFatal(fmt.Errorf("%s: %s: %w", stage, path, err))
	if h.cancel != nil {
		h.cancel()
	}
}

// aux records a failure of an auxiliary artifact (thumbnail/preview cache)
// without disqualifying the source file. Storage-level errors abort the scan.
func (h *scanHooks) aux(path, stage string, err error) {
	if err == nil {
		return
	}
	if isFatalIOError(err) {
		h.fatal(path, stage, err)
		return
	}
	h.record(path, stage, err.Error())
}

func (h *scanHooks) failed(path string) bool {
	return h.failures.failed(path)
}

// isFatalIOError reports whether err means the storage layer itself is
// unhealthy. Missing files and permission problems are ordinary per-file
// failures, not disk failures.
func isFatalIOError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
		return false
	}
	if errors.Is(err, contextCanceled) || errors.Is(err, contextDeadline) {
		return false
	}
	return fatalStorageError(err)
}

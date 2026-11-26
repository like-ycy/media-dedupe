package pipeline

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"sync"

	"media-dedupe/internal/model"
	"media-dedupe/internal/video"
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

// cacheWriteError marks a failure to persist a derived artifact (hash,
// metadata, preview bookkeeping). It says nothing about the source file: the
// disk, the cache database or the program itself is at fault, so the file must
// not be skipped or evicted.
type cacheWriteError struct{ err error }

func (e *cacheWriteError) Error() string { return "cache write: " + e.err.Error() }

func (e *cacheWriteError) Unwrap() error { return e.err }

// scanHooks centralizes per-file and environment failure handling for a scan.
type scanHooks struct {
	record   func(path, stage, kind, message string)
	failures *failureTracker
	cancel   func()
	// confirmed holds the paths whose byte identity the exact pass already
	// proved. Later stages only add similarity information, so their failures
	// must not delete a duplicate that SHA-256 has settled.
	confirmed map[string]struct{}
}

// markConfirmed records the paths of exact duplicate groups. Call it once the
// exact pass is done and before any approximate stage starts.
func (h *scanHooks) markConfirmed(groups []model.ReportGroup) {
	if h.confirmed == nil {
		h.confirmed = make(map[string]struct{})
	}
	for _, group := range groups {
		for _, item := range group.Items {
			h.confirmed[item.Path] = struct{}{}
		}
	}
}

// file skips a file whose own data could not be read. A storage-level error is
// promoted to a fatal scan failure instead. A file that already has a confirmed
// duplicate stays in the report: the failure only costs its similarity
// comparison, so it is kept as a warning.
func (h *scanHooks) file(path, stage string, err error) {
	if err == nil {
		return
	}
	if isFatalIOError(err) {
		h.fatal(path, stage, err)
		return
	}
	if _, ok := h.confirmed[path]; ok {
		h.record(path, stage, model.ErrorAux, err.Error())
		return
	}
	h.failures.mark(path)
	h.record(path, stage, model.ErrorSkipped, err.Error())
}

// read handles an error from a step that both reads the file and stores
// derived data: source failures skip the file, cache failures abort the scan.
func (h *scanHooks) read(path, stage string, err error) {
	var cacheErr *cacheWriteError
	if errors.As(err, &cacheErr) {
		h.fatal(path, stage, cacheErr.err)
		return
	}
	h.file(path, stage, err)
}

// fatal stops the scan: the environment (disk, cache, program state) is broken.
func (h *scanHooks) fatal(path, stage string, err error) {
	if err == nil {
		return
	}
	h.record(path, stage, model.ErrorFatal, err.Error())
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
	h.record(path, stage, model.ErrorAux, err.Error())
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
	// ffmpeg/ffprobe report disk problems in their diagnostics, not as errno.
	if errors.Is(err, video.ErrStorageIO) {
		return true
	}
	return fatalStorageError(err)
}

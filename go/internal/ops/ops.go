package ops

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

type FailedItem struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

type Request struct {
	Mode     string   `json:"mode"` // recycle | permanent
	Paths    []string `json:"paths"`
	GroupIDs []int64  `json:"groupIds"`
}

type Result struct {
	Succeeded []string     `json:"succeeded"`
	Failed    []FailedItem `json:"failed"`
	Mode      string       `json:"mode"`
}

// Neither platform documents a cap on how many paths one recycle call may
// carry: Windows takes a double-NUL-terminated list (SHFILEOPSTRUCTW.pFrom) and
// macOS takes one AppleScript statement per path. What actually bounds a batch
// here is therefore not an OS limit but two engineering limits:
//
//   - trashBatchBytes keeps one platform call's path payload small. Budgeting by
//     UTF-8 bytes is a safe upper bound for Windows too, because a path's UTF-16
//     unit count can never exceed its UTF-8 byte count, so 64 KiB here means at
//     most a 128 KiB pFrom buffer. macOS limits one process invocation to
//     ARG_MAX (1048576 bytes, verified with getconf), and a 64 KiB payload plus
//     AppleScript syntax stays far below it.
//   - trashBatchLimit caps the worst case: if a whole batch fails, the paths
//     still present are retried one by one, so this is roughly how many extra
//     per-file calls one failing batch can cost. At 256 the worst case stays in
//     the same order of magnitude as the old per-file loop, so a bad batch can
//     never turn into a much slower run than before.
const (
	trashBatchBytes = 64 << 10
	trashBatchLimit = 256
)

// trashPathsBatch is the platform's bulk recycle hook. Windows hands the whole
// list to a single SHFileOperationW call; platforms that leave it nil fall back
// to one trashPath call per file. It returns only the failed paths.
var trashPathsBatch func(paths []string) map[string]error

// batchPaths splits paths into platform calls that respect both the byte budget
// and the item cap, preserving order.
func batchPaths(paths []string) [][]string {
	var out [][]string
	start, bytes := 0, 0
	for i, p := range paths {
		size := len(p) + 1 // +1 for the terminator SHFileOperation needs
		if i > start && (bytes+size > trashBatchBytes || i-start >= trashBatchLimit) {
			out = append(out, paths[start:i])
			start, bytes = i, 0
		}
		bytes += size
	}
	if start < len(paths) {
		out = append(out, paths[start:])
	}
	return out
}

// encodePathUTF16 turns one path into the UTF-16 units the OS expects. It is a
// variable because Windows needs a different encoder: syscall.UTF16FromString
// preserves unpaired surrogates (WTF-8), which Windows file names may legally
// contain, while utf16.Encode would replace them with U+FFFD and we would then
// act on a different name. The default below is only ever used on platforms
// that hand the buffer to nothing.
var encodePathUTF16 = func(path string) ([]uint16, error) {
	return utf16.Encode([]rune(path)), nil
}

// doubleNullListUTF16 lays out pFrom for one SHFileOperation call: every path is
// NUL terminated and the list ends with a second NUL. It lives here rather than
// in the Windows file so the layout can be unit-tested on any platform — a
// malformed buffer is the one way this code could make the shell read past its
// end, which would be a real crash rather than an error code.
func doubleNullListUTF16(paths []string) ([]uint16, error) {
	if len(paths) == 0 {
		// An empty list still has to be double-NUL terminated, otherwise a
		// caller that passes it anyway hands the shell a buffer ending in a
		// single NUL and it keeps reading.
		return []uint16{0, 0}, nil
	}
	buf := make([]uint16, 0, len(paths)*64+2)
	for _, path := range paths {
		if strings.ContainsRune(path, 0) {
			return nil, fmt.Errorf("path contains NUL: %q", path)
		}
		u, err := encodePathUTF16(path)
		if err != nil {
			return nil, fmt.Errorf("encode path %q: %w", path, err)
		}
		buf = append(buf, u...)
		buf = append(buf, 0)
	}
	return append(buf, 0), nil
}

// gone reports whether path is no longer there. A stat error that is not
// ErrNotExist (permission denied, dead network handle, I/O error) cannot be
// read as "deleted": doing so would report a file we failed to remove as a
// success, and the caller would then mark its group as handled.
func gone(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return false, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	return false, err
}

// dedupeKey collapses the cosmetic differences between two spellings of one
// directory entry: empty segments and "." components. It deliberately keeps
// ".." — filepath.Clean would resolve it, but "link/.." lands in the link's
// target parent rather than the link's own parent, so collapsing it merges two
// paths that name different files, and these paths are about to be deleted.
//
// Note what is *not* being compared: inodes. Two hard links share an inode but
// are separate directory entries, and deleting one leaves the other, so merging
// them would leave a file the caller asked to remove. The key is the entry, not
// the contents.
func dedupeKey(p string) string {
	vol := filepath.VolumeName(p)
	rest := p[len(vol):]
	isSep := func(r rune) bool { return r < utf8.RuneSelf && os.IsPathSeparator(byte(r)) }

	var b strings.Builder
	b.WriteString(vol)
	for _, seg := range strings.FieldsFunc(rest, isSep) {
		if seg == "." {
			continue
		}
		b.WriteString(string(filepath.Separator))
		b.WriteString(seg)
	}
	if b.Len() == len(vol) {
		// Nothing but separators and "." — this is the volume root itself.
		b.WriteString(string(filepath.Separator))
	}
	return b.String()
}

// absolutePath makes p absolute without the lexical Clean that filepath.Abs
// performs. Clean collapses "link/.." to ".", but the kernel resolves "link"
// first and only then applies "..", so the two can name different files — and
// this path is about to be deleted. Anything already absolute is passed through
// untouched for the same reason; the OS resolves it, we do not.
func absolutePath(p string) (string, error) {
	if filepath.IsAbs(p) {
		return p, nil
	}
	// Windows has two shapes that are neither absolute nor plain relative, and
	// gluing either onto the working directory invents a path:
	//   "\media\a.jpg"  — root of the current drive
	//   "C:media\a.jpg" — relative to that drive's own current directory
	if vol := filepath.VolumeName(p); vol != "" {
		// Drive-relative: only the OS knows that drive's current directory, so
		// let it resolve. This is the one place a lexical Clean can creep in,
		// and only for input that was never canonical to begin with.
		return filepath.Abs(p)
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if len(p) > 0 && os.IsPathSeparator(p[0]) {
		return filepath.VolumeName(wd) + p, nil
	}
	return wd + string(filepath.Separator) + p, nil
}

// deleteSlot keeps the caller's path next to the absolute form we act on, so
// results stay in request order even though deletion happens in batches.
type deleteSlot struct {
	orig string
	abs  string
	err  string
}

// DeleteFiles deletes paths with recycle (default) or permanent mode.
// Recycle never falls back silently to permanent.
func DeleteFiles(req Request) Result {
	mode := req.Mode
	if mode == "" {
		mode = "recycle"
	}
	res := Result{Mode: mode, Succeeded: []string{}, Failed: []FailedItem{}}
	if mode != "recycle" && mode != "permanent" {
		res.Failed = append(res.Failed, FailedItem{Path: "", Message: "invalid delete mode: " + mode})
		return res
	}

	// Validate everything up front so a bad entry never reaches the OS as part
	// of a batch, where one failure can hide the rest.
	slots := precheck(req.Paths)
	targets := make([]string, 0, len(slots))
	seen := make(map[string]bool, len(slots))
	for i := range slots {
		s := &slots[i]
		if s.err != "" {
			continue
		}
		// A repeated directory entry can only fail once the first copy is gone,
		// and a failing entry can poison a whole batch, so duplicates are
		// dropped here. See dedupeKey for why this is not filepath.Clean and
		// not an inode comparison.
		key := dedupeKey(s.abs)
		if seen[key] {
			s.err = "duplicate path"
			continue
		}
		seen[key] = true
		targets = append(targets, s.abs)
	}

	failed := make(map[string]error, 0)
	if mode == "permanent" {
		for _, p := range targets {
			if err := removePermanent(p); err != nil {
				failed[p] = err
			}
		}
	} else {
		failed = recyclePaths(targets)
	}

	for _, s := range slots {
		if s.err != "" {
			res.Failed = append(res.Failed, FailedItem{Path: s.orig, Message: s.err})
			continue
		}
		if err, ok := failed[s.abs]; ok && err != nil {
			res.Failed = append(res.Failed, FailedItem{Path: s.orig, Message: err.Error()})
			continue
		}
		res.Succeeded = append(res.Succeeded, s.orig)
	}
	return res
}

// precheck resolves and validates paths without touching the filesystem state.
// Resolution is deliberately non-canonicalising (see absolutePath): whatever the
// kernel resolves the string to is what gets deleted.
func precheck(paths []string) []deleteSlot {
	slots := make([]deleteSlot, 0, len(paths))
	for _, raw := range paths {
		s := deleteSlot{orig: strings.TrimSpace(raw)}
		if s.orig == "" {
			s.err = "empty path"
			slots = append(slots, s)
			continue
		}
		abs, err := absolutePath(s.orig)
		if err != nil {
			s.err = err.Error()
			slots = append(slots, s)
			continue
		}
		s.abs = abs
		if _, err := os.Lstat(abs); err != nil {
			s.err = err.Error()
		} else if isProtected(abs) {
			s.err = "protected system path"
		}
		slots = append(slots, s)
	}
	return slots
}

// recyclePaths moves targets to the trash in batches, using the platform bulk
// hook when there is one. Paths are grouped by volume first: a recycle bin is
// per-volume, so mixing volumes in one shell call risks losing per-item
// accuracy for no gain.
func recyclePaths(targets []string) map[string]error {
	failed := make(map[string]error, 0)
	if len(targets) == 0 {
		return failed
	}
	if trashPathsBatch == nil {
		for _, p := range targets {
			if err := trashPath(p); err != nil {
				failed[p] = err
			}
		}
		return failed
	}
	buckets := make(map[string][]string, 2)
	volumes := make([]string, 0, 2)
	for _, p := range targets {
		vol := strings.ToLower(filepath.VolumeName(p))
		if _, ok := buckets[vol]; !ok {
			volumes = append(volumes, vol)
		}
		buckets[vol] = append(buckets[vol], p)
	}
	for _, vol := range volumes {
		for _, batch := range batchPaths(buckets[vol]) {
			for p, err := range trashPathsBatch(batch) {
				if err != nil {
					failed[p] = err
				}
			}
		}
	}
	return failed
}

func removePermanent(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return os.RemoveAll(path)
	}
	return os.Remove(path)
}

func isProtected(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return true
	}
	abs = filepath.Clean(abs)
	if abs == "/" || abs == `C:\` || abs == `C:` {
		return true
	}
	lower := strings.ToLower(abs)
	protected := []string{
		`c:\windows`,
		`c:\program files`,
		`c:\program files (x86)`,
		"/system",
		"/library",
		"/usr",
		"/bin",
		"/sbin",
		"/etc",
		"/proc",
	}
	for _, p := range protected {
		if strings.HasPrefix(lower, p+string(filepath.Separator)) || lower == p {
			return true
		}
	}
	return false
}

// OpenPath opens path with the system default application.
func OpenPath(path string) error {
	if _, err := os.Lstat(path); err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	return openPath(abs)
}

// RevealInFolder reveals path in Finder / Explorer.
func RevealInFolder(path string) error {
	if _, err := os.Lstat(path); err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", "-R", abs)
	case "windows":
		cmd = exec.Command("explorer", "/select,", abs)
	default:
		cmd = exec.Command("xdg-open", filepath.Dir(abs))
	}
	prepareCmd(cmd)
	return cmd.Start()
}

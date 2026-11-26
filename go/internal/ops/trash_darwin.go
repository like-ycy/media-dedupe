//go:build darwin

package ops

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

func init() { trashPathsBatch = recycleBatch }

func trashPath(path string) error {
	// An exit status of 0 does not mean the file is gone: a single delete is
	// wrapped in try/end try, so Finder's "can't delete, it's locked" is caught
	// by the script and osascript still exits cleanly. Anything that treats
	// that as success would report a file as deleted while it sits on disk.
	if err := deleteWithFinder([]string{path}); err == nil {
		ok, statErr := gone(path)
		if statErr != nil {
			return statErr
		}
		if ok {
			return nil
		}
	}
	// Finder refused or the script failed; a plain rename works on files Finder
	// keeps hold of, and reports a real error when it does not.
	return renameToTrash(path)
}

// recycleBatch deletes the whole batch with one osascript run. Spawning a
// process per file is what made bulk delete slow; Finder still handles every
// path, but the process startup cost is paid once per batch.
//
// Finder reports nothing per item, so each path is verified afterwards and any
// survivor is retried directly.
func recycleBatch(paths []string) map[string]error {
	failed := make(map[string]error, len(paths))
	if len(paths) == 0 {
		return failed
	}
	if err := deleteWithFinder(paths); err != nil {
		// The script as a whole failed, so nothing can be assumed: some deletes
		// may still have landed. Retry each survivor on its own, so one bad
		// name cannot take the rest of the batch down with it. This costs a
		// process per survivor, which is why the escape below has to be exact.
		for _, p := range paths {
			ok, statErr := gone(p)
			if statErr != nil {
				failed[p] = statErr
				continue
			}
			if ok {
				continue
			}
			if err := trashPath(p); err != nil {
				failed[p] = err
			}
		}
		return failed
	}
	for _, p := range paths {
		ok, statErr := gone(p)
		if statErr != nil {
			failed[p] = statErr
			continue
		}
		if ok {
			continue
		}
		// Finder left it behind (locked, permissions): one direct rename is
		// cheaper than another osascript and covers the common case.
		if err := renameToTrash(p); err != nil {
			failed[p] = fmt.Errorf("finder skipped %s: %v", p, err)
		}
	}
	return failed
}

// deleteWithFinder sends every path to Finder inside one AppleScript, wrapping
// each delete in try so a single stubborn file cannot abort the batch.
func deleteWithFinder(paths []string) error {
	out, err := exec.Command("osascript", "-e", buildFinderScript(paths)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("finder delete failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// buildFinderScript is separated out so the escaping can be checked by really
// compiling the script (osacompile) instead of by eyeballing the string.
func buildFinderScript(paths []string) string {
	var b strings.Builder
	b.WriteString("tell application \"Finder\"\n")
	for _, p := range paths {
		b.WriteString("try\n")
		fmt.Fprintf(&b, "delete POSIX file \"%s\"\n", escapeAppleScript(p))
		b.WriteString("end try\n")
	}
	b.WriteString("end tell")
	return b.String()
}

// maxNameComponent is the longest single file name these filesystems accept
// (NAME_MAX, 255 bytes on both APFS and HFS+).
const maxNameComponent = 255

// renameToTrash is the fallback: move the file into ~/.Trash directly.
func renameToTrash(path string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("recycle failed: %v", err)
	}
	trashDir := filepath.Join(home, ".Trash")
	dst := filepath.Join(trashDir, filepath.Base(path))
	if _, statErr := os.Lstat(dst); statErr == nil {
		dst = filepath.Join(trashDir, collisionName(filepath.Base(path), os.Getpid(), time.Now().UnixNano()))
	}
	if err := os.Rename(path, dst); err != nil {
		return fmt.Errorf("recycle failed: %v", err)
	}
	return nil
}

// collisionName builds a trash name that avoids the existing entry while
// staying inside maxNameComponent. The obvious base + ".pid.nanotime" is up to
// 31 bytes of suffix, so a 240-byte name would exceed the limit and the rename
// would fail with ENAMETOOLONG, leaving the file exactly where it was.
func collisionName(base string, pid int, nanos int64) string {
	suffix := fmt.Sprintf(".%d.%d", pid, nanos)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	// Keep the extension only when there is real room for it; the name still
	// has to mean something to whoever empties the trash later.
	if len(ext) > maxNameComponent/2 {
		ext, stem = "", base
	}
	room := maxNameComponent - len(suffix) - len(ext)
	if room < 1 {
		room = 1
	}
	return truncateBytes(stem, room) + ext + suffix
}

// truncateBytes cuts s to at most n bytes without splitting a UTF-8 rune.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n < 0 {
		n = 0
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func escapeAppleScript(s string) string {
	// Backslashes first: escaping the quote first would let that insertion's
	// own backslash be doubled again. A lone "\" in a file name is otherwise a
	// syntax error, and a syntax error kills the whole script — the per-item
	// try blocks are runtime protection and cannot save a failed compile.
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

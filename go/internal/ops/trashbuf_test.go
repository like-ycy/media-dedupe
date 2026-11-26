package ops

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"
)

func u16(s string) []uint16 { return utf16.Encode([]rune(s)) }

// TestDoubleNullListUTF16 pins the pFrom layout SHFileOperation expects: every
// path NUL terminated, then one extra NUL. Getting this wrong is the only way
// this package could hand the shell a buffer it reads past the end of.
func TestDoubleNullListUTF16(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		want  []uint16
	}{
		{
			name:  "single path",
			paths: []string{`C:\media\a.jpg`},
			want:  append(u16(`C:\media\a.jpg`), 0, 0),
		},
		{
			name:  "two paths",
			paths: []string{`C:\media\a.jpg`, `C:\media\b.jpg`},
			want:  append(append(append(u16(`C:\media\a.jpg`), 0), u16(`C:\media\b.jpg`)...), 0, 0),
		},
		{
			name:  "paths in different directories",
			paths: []string{`C:\a\1.jpg`, `D:\b\2.jpg`},
			want:  append(append(append(u16(`C:\a\1.jpg`), 0), u16(`D:\b\2.jpg`)...), 0, 0),
		},
		{
			name:  "non-ascii path",
			paths: []string{`C:\照片\假期.jpg`},
			want:  append(u16(`C:\照片\假期.jpg`), 0, 0),
		},
		{
			name:  "empty list still gets the double terminator",
			paths: nil,
			want:  []uint16{0, 0},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := doubleNullListUTF16(tc.paths)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("buffer mismatch\n got: %v\nwant: %v", got, tc.want)
			}
			// The list must be double-NUL terminated and contain no stray NUL
			// right after the final path.
			if len(got) < 2 || got[len(got)-1] != 0 || got[len(got)-2] != 0 {
				t.Fatalf("buffer is not double-NUL terminated: %v", got)
			}
		})
	}
}

func TestDoubleNullListUTF16RejectsEmbeddedNUL(t *testing.T) {
	if _, err := doubleNullListUTF16([]string{"C:\\a\x00b"}); err == nil {
		t.Fatal("expected an error for a path containing NUL")
	}
}

// TestDoubleNullListUTF16UsesPlatformEncoder proves the layout actually goes
// through the injected encoder. Windows swaps in syscall.UTF16FromString there
// to keep unpaired surrogates (WTF-8) intact; if the layout ever went back to
// calling utf16.Encode directly, Windows names containing a lone surrogate
// would silently turn into U+FFFD and we would delete the wrong file.
func TestDoubleNullListUTF16UsesPlatformEncoder(t *testing.T) {
	orig := encodePathUTF16
	defer func() { encodePathUTF16 = orig }()

	called := 0
	encodePathUTF16 = func(p string) ([]uint16, error) {
		called++
		if p == "boom" {
			return nil, errors.New("encoder refused")
		}
		return []uint16{0xFFFF}, nil
	}

	got, err := doubleNullListUTF16([]string{"a", "b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called != 2 {
		t.Fatalf("encoder called %d times, want 2", called)
	}
	if want := []uint16{0xFFFF, 0, 0xFFFF, 0, 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("layout is not path+NUL per entry: got %v want %v", got, want)
	}

	if _, err := doubleNullListUTF16([]string{"boom"}); err == nil {
		t.Fatal("an encoder error must propagate, not be swallowed")
	}
}

// TestGoneSeparatesMissingFromUnreadable is the guard against reporting a file
// we failed to delete as deleted: only a definite "does not exist" may count as
// success. The permission case is how this shows up in practice (a directory
// whose mode changed between the scan and the delete, or a dead network mount).
func TestGoneSeparatesMissingFromUnreadable(t *testing.T) {
	dir := t.TempDir()

	present := filepath.Join(dir, "here.jpg")
	if err := os.WriteFile(present, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := gone(present); ok || err != nil {
		t.Fatalf("existing file: got (%v, %v), want (false, nil)", ok, err)
	}

	if ok, err := gone(filepath.Join(dir, "gone.jpg")); !ok || err != nil {
		t.Fatalf("missing file: got (%v, %v), want (true, nil)", ok, err)
	}

	// A symlink whose target is missing still occupies its own name, so it is
	// not "gone" — deleting it is a real operation on a real directory entry.
	dangling := filepath.Join(dir, "dangling.jpg")
	if err := os.Symlink(filepath.Join(dir, "nowhere"), dangling); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if ok, err := gone(dangling); ok || err != nil {
		t.Fatalf("dangling symlink: got (%v, %v), want (false, nil)", ok, err)
	}

	if runtime.GOOS == "windows" {
		return // cannot drop read permission on a directory meaningfully
	}
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(locked, "inside.jpg")
	if err := os.WriteFile(inner, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(locked, 0o700)

	ok, err := gone(inner)
	if err == nil {
		t.Skip("permission denial not enforced here (running as root?)")
	}
	if ok {
		t.Fatal("an unreadable path must not be reported as deleted")
	}
}

// TestAbsolutePathDoesNotRewriteTheTarget is the fix for a delete hitting the
// wrong file. filepath.Clean collapses "link/.." lexically, but the kernel
// resolves "link" first and applies ".." to its real target, so the cleaned
// path can name a completely different file.
func TestAbsolutePathDoesNotRewriteTheTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinked directories and '..' semantics differ")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "base"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "other", "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	// The file that the raw path really names.
	realFile := filepath.Join(dir, "other", "x.jpg")
	if err := os.WriteFile(realFile, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "other", "sub"), filepath.Join(dir, "base", "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// Built by concatenation on purpose: filepath.Join would Clean the ".."
	// away and there would be nothing left to test.
	raw := dir + string(filepath.Separator) + "base/link/../x.jpg"
	cleaned := filepath.Clean(raw)
	if cleaned == raw {
		t.Fatalf("test is not meaningful: Clean left %q unchanged", raw)
	}
	// Sanity: the two forms really do name different files here.
	if _, err := os.Lstat(realFile); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := os.Lstat(cleaned); err == nil {
		t.Fatalf("test is not meaningful: %q also exists", cleaned)
	}

	got, err := absolutePath(raw)
	if err != nil {
		t.Fatalf("absolutePath: %v", err)
	}
	if got != raw {
		t.Fatalf("absolute path was rewritten\n  got: %q\n want: %q", got, raw)
	}
	// And it must still resolve to the file the caller meant.
	if _, err := os.Lstat(got); err != nil {
		t.Fatalf("resolved path does not name the intended file: %v", err)
	}
}

// TestDeleteFilesTakesTheIntendedFile is the consequence of the test above, at
// the level that actually matters: DeleteFiles must remove the file the raw
// path resolves to, and must leave the file the cleaned path would have named.
// Permanent mode keeps this out of the user's trash.
func TestDeleteFilesTakesTheIntendedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinked directories and '..' semantics differ")
	}
	dir := t.TempDir()
	decoyDir := filepath.Join(dir, "base")
	realDir := filepath.Join(dir, "other")
	for _, d := range []string{decoyDir, filepath.Join(realDir, "sub")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	decoy := filepath.Join(decoyDir, "x.jpg")
	real := filepath.Join(realDir, "x.jpg")
	for _, f := range []string{decoy, real} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(realDir, "sub"), filepath.Join(decoyDir, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	raw := dir + string(filepath.Separator) + "base/link/../x.jpg"
	res := DeleteFiles(Request{Mode: "permanent", Paths: []string{raw}})
	if len(res.Failed) != 0 || len(res.Succeeded) != 1 {
		t.Fatalf("delete reported %d ok / %d failed: %v", len(res.Succeeded), len(res.Failed), res.Failed)
	}
	if _, err := os.Lstat(real); err == nil {
		t.Fatal("the file the raw path names was not deleted")
	}
	if _, err := os.Lstat(decoy); err != nil {
		t.Fatalf("the file named by the lexically cleaned path was deleted instead: %v", err)
	}
}

// TestDeleteFilesDedupesByFileNotByString covers the other half: two paths that
// look different but name one file must be collapsed (a repeated entry can
// poison a Windows batch, and the second copy cannot succeed anyway), while two
// paths that look the "same" after Clean but name different files must both be
// deleted.
func TestDeleteFilesDedupesByFileNotByString(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinked directories and '..' semantics differ")
	}

	t.Run("same file listed twice", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "a.jpg"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		dot := dir + string(filepath.Separator) + "./a.jpg"
		plain := filepath.Join(dir, "a.jpg")
		res := DeleteFiles(Request{Mode: "permanent", Paths: []string{dot, plain}})
		if len(res.Succeeded) != 1 || len(res.Failed) != 1 {
			t.Fatalf("want 1 deleted + 1 duplicate, got %d ok / %d failed: %v", len(res.Succeeded), len(res.Failed), res.Failed)
		}
		if res.Failed[0].Message != "duplicate path" {
			t.Fatalf("unexpected failure message: %q", res.Failed[0].Message)
		}
	})

	t.Run("different files that Clean would merge", func(t *testing.T) {
		dir := t.TempDir()
		for _, d := range []string{filepath.Join(dir, "base"), filepath.Join(dir, "other", "sub")} {
			if err := os.MkdirAll(d, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		for _, f := range []string{filepath.Join(dir, "base", "x.jpg"), filepath.Join(dir, "other", "x.jpg")} {
			if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(filepath.Join(dir, "other", "sub"), filepath.Join(dir, "base", "link")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		viaLink := dir + string(filepath.Separator) + "base/link/../x.jpg"
		plain := filepath.Join(dir, "base", "x.jpg")
		res := DeleteFiles(Request{Mode: "permanent", Paths: []string{viaLink, plain}})
		if len(res.Succeeded) != 2 || len(res.Failed) != 0 {
			t.Fatalf("both files are distinct and must be deleted, got %d ok / %d failed: %v", len(res.Succeeded), len(res.Failed), res.Failed)
		}
	})

	// Hard links share an inode but are separate directory entries: removing one
	// leaves the other on disk. Dedupe by inode would silently skip the second
	// file the caller selected.
	t.Run("hard links are separate entries", func(t *testing.T) {
		dir := t.TempDir()
		first := filepath.Join(dir, "first.jpg")
		second := filepath.Join(dir, "second.jpg")
		if err := os.WriteFile(first, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(first, second); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}
		res := DeleteFiles(Request{Mode: "permanent", Paths: []string{first, second}})
		if len(res.Succeeded) != 2 || len(res.Failed) != 0 {
			t.Fatalf("both entries must be deleted, got %d ok / %d failed: %v", len(res.Succeeded), len(res.Failed), res.Failed)
		}
		if entries, err := os.ReadDir(dir); err != nil {
			t.Fatal(err)
		} else if len(entries) != 0 {
			t.Fatalf("%d entries left behind", len(entries))
		}
	})
}

// TestDedupeKey pins the line between "two spellings of one entry" and "two
// entries that look alike". Getting this wrong means either a skipped delete or
// a poisoned batch, both invisible until someone's library is half-cleaned.
func TestDedupeKey(t *testing.T) {
	sep := string(filepath.Separator)
	same := [][2]string{
		{"/a/b/x.jpg", "/a/b/x.jpg"},
		{"/a/b/x.jpg", "/a//b/x.jpg"},
		{"/a/b/x.jpg", "/a/./b/./x.jpg"},
		{"/a/b/", "/a/b"},
	}
	for _, pair := range same {
		if dedupeKey(pair[0]) != dedupeKey(pair[1]) {
			t.Errorf("%q and %q should be one entry: %q vs %q", pair[0], pair[1], dedupeKey(pair[0]), dedupeKey(pair[1]))
		}
	}
	different := [][2]string{
		{"/a/link/../x.jpg", "/a/x.jpg"},
		{"/a/x.jpg", "/a/y.jpg"},
		{"/a/b", "/a/b/c"},
	}
	for _, pair := range different {
		if dedupeKey(pair[0]) == dedupeKey(pair[1]) {
			t.Errorf("%q and %q must stay distinct, both keyed as %q", pair[0], pair[1], dedupeKey(pair[0]))
		}
	}
	if got := dedupeKey(sep); got != sep {
		t.Errorf("root keyed as %q", got)
	}
}

// TestAbsolutePathShapes covers the relative forms that are not simply "relative
// to the working directory". The Windows shapes cannot run here, so they are
// only compiled and reviewed; the check that they are not silently joined onto
// the working directory is the isRootRelative/drive logic itself.
func TestAbsolutePathShapes(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Run("relative joins the working directory", func(t *testing.T) {
		got, err := absolutePath("media/a.jpg")
		if err != nil {
			t.Fatal(err)
		}
		if want := wd + string(filepath.Separator) + "media/a.jpg"; got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})
	t.Run("dot relative keeps the dot", func(t *testing.T) {
		// The "." is left in place: it changes nothing for the OS, and removing
		// it is exactly the rewriting this function exists to avoid.
		got, err := absolutePath("./media/a.jpg")
		if err != nil {
			t.Fatal(err)
		}
		if want := wd + string(filepath.Separator) + "./media/a.jpg"; got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})
}

// TestBatchPaths checks the two limits that bound one platform call: the byte
// budget and the item cap that sets the worst-case retry cost.
func TestBatchPaths(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if got := batchPaths(nil); len(got) != 0 {
			t.Fatalf("expected no batches, got %v", got)
		}
	})

	t.Run("item cap", func(t *testing.T) {
		paths := make([]string, trashBatchLimit*2+10)
		for i := range paths {
			paths[i] = "C:\\m\\a.jpg" // short, so the byte budget never trips
		}
		batches := batchPaths(paths)
		if len(batches) != 3 {
			t.Fatalf("expected 3 batches, got %d", len(batches))
		}
		if len(batches[0]) != trashBatchLimit || len(batches[1]) != trashBatchLimit || len(batches[2]) != 10 {
			t.Fatalf("unexpected batch sizes: %d %d %d", len(batches[0]), len(batches[1]), len(batches[2]))
		}
	})

	t.Run("byte budget", func(t *testing.T) {
		paths := make([]string, 200)
		for i := range paths {
			paths[i] = strings.Repeat("a", 400) // 400-byte payload each
		}
		batches := batchPaths(paths)
		for i, b := range batches {
			total := 0
			for _, p := range b {
				total += len(p) + 1
			}
			if total > trashBatchBytes {
				t.Fatalf("batch %d carries %d bytes, over budget %d", i, total, trashBatchBytes)
			}
		}
		// 64 KiB budget / 401 bytes per path = 163 per batch, so 200 paths
		// need two batches.
		if len(batches) != 2 {
			t.Fatalf("expected 2 batches, got %d", len(batches))
		}
	})

	t.Run("order preserved and nothing lost", func(t *testing.T) {
		paths := make([]string, 600)
		for i := range paths {
			paths[i] = strings.Repeat("p", 50) + string(rune('A'+i%26))
		}
		var flat []string
		for _, b := range batchPaths(paths) {
			flat = append(flat, b...)
		}
		if !reflect.DeepEqual(flat, paths) {
			t.Fatal("batching changed the path order or dropped entries")
		}
	})
}

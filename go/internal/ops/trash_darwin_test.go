//go:build darwin

package ops

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestEscapeAppleScript pins the two characters that can break the script.
// A backslash is a syntax error inside an AppleScript string literal, and a
// syntax error kills the whole script: the "try" blocks around each delete are
// runtime protection and cannot rescue a failed compile. That would send an
// entire 500-file batch down the slow fallback path.
func TestEscapeAppleScript(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{`/plain/a.jpg`, `/plain/a.jpg`},
		{`/media/a"b.jpg`, `/media/a\"b.jpg`},
		{`/media/a\b.jpg`, `/media/a\\b.jpg`},
		// backslash immediately before a quote: both must be escaped, and the
		// backslash first, otherwise the inserted escape char gets doubled.
		{`/media/a\"b.jpg`, `/media/a\\\"b.jpg`},
		{`/media/照片.jpg`, `/media/照片.jpg`},
	}
	for _, tc := range tests {
		if got := escapeAppleScript(tc.in); got != tc.want {
			t.Errorf("escapeAppleScript(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestFinderScriptCompiles hands the generated script to the real AppleScript
// compiler. This is the check that matters: the old version escaped quotes but
// not backslashes, and a file named `a\b.jpg` made every path in the batch fail.
func TestFinderScriptCompiles(t *testing.T) {
	nasty := []string{
		`/tmp/plain.jpg`,
		`/tmp/quote"name.jpg`,
		`/tmp/back\slash.jpg`,
		`/tmp/both\"chars.jpg`,
		`/tmp/照片 空格.jpg`,
		`/tmp/单引号'name.jpg`,
	}
	script := buildFinderScript(nasty)
	out := filepath.Join(t.TempDir(), "probe.scpt")
	cmd := exec.Command("osacompile", "-e", script, "-o", out)
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("AppleScript failed to compile: %v\n%s\n--- script ---\n%s", err, combined, script)
	}

	// One script per path must also compile: that is the retry path taken when
	// the batch script fails, so it cannot have its own escaping hole.
	for _, p := range nasty {
		one := filepath.Join(t.TempDir(), "one.scpt")
		if combined, err := exec.Command("osacompile", "-e", buildFinderScript([]string{p}), "-o", one).CombinedOutput(); err != nil {
			t.Fatalf("single-path script for %q failed to compile: %v\n%s", p, err, combined)
		}
	}
}

// TestCollisionName checks the name built when the trash already holds a file
// of the same name. Appending ".pid.nanotime" (up to 31 bytes) to a long name
// overflows NAME_MAX, and then the rename fails with ENAMETOOLONG and the file
// simply stays where it was — which is exactly what this rename was avoiding.
func TestCollisionName(t *testing.T) {
	t.Run("short name keeps its stem and extension", func(t *testing.T) {
		got := collisionName("a.jpg", 42, 1234567890)
		if !strings.HasPrefix(got, "a") || !strings.HasSuffix(got, ".jpg.42.1234567890") {
			t.Fatalf("unexpected name: %q", got)
		}
	})

	t.Run("long ascii name stays within NAME_MAX", func(t *testing.T) {
		base := strings.Repeat("a", 240) + ".jpg"
		got := collisionName(base, 42, 1234567890)
		if len(got) > maxNameComponent {
			t.Fatalf("name is %d bytes, over the %d limit: %q", len(got), maxNameComponent, got)
		}
		if !strings.HasSuffix(got, ".42.1234567890") {
			t.Fatalf("name lost its uniqueness suffix: %q", got)
		}
		if !strings.HasSuffix(got, ".jpg.42.1234567890") {
			t.Fatalf("name lost its extension: %q", got)
		}
	})

	t.Run("long multibyte name stays valid utf-8", func(t *testing.T) {
		base := strings.Repeat("照", 90) + ".jpg" // 270 bytes, over the limit
		got := collisionName(base, 42, 1234567890)
		if len(got) > maxNameComponent {
			t.Fatalf("name is %d bytes, over the limit", len(got))
		}
		if !utf8.ValidString(got) {
			t.Fatalf("truncation split a rune: %q", got)
		}
	})

	t.Run("pathological name still terminates", func(t *testing.T) {
		// Every byte is a multibyte continuation byte's worth of name: the
		// suffix must win, the name must not come back empty of a suffix.
		got := collisionName(strings.Repeat("照", 200), 1, 2)
		if len(got) > maxNameComponent || len(got) == 0 {
			t.Fatalf("bad name length %d", len(got))
		}
		if !strings.HasSuffix(got, ".1.2") {
			t.Fatalf("lost suffix: %q", got)
		}
	})
}

// TestCollisionNameFitsFilesystem asks the real filesystem instead of trusting
// the 255 arithmetic: the name has to be accepted in a temp dir, otherwise the
// rename would fail with ENAMETOOLONG exactly as it did before the fix.
func TestCollisionNameFitsFilesystem(t *testing.T) {
	dir := t.TempDir()
	for _, base := range []string{
		"a.jpg",
		strings.Repeat("z", 240) + ".jpg",
		strings.Repeat("照", 90) + ".jpg",
		strings.Repeat("z", 255),
	} {
		name := collisionName(base, os.Getpid(), 1700000000000000000)
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("filesystem rejected %d-byte name for base of %d bytes: %v", len(name), len(base), err)
		}
		f.Close()
	}
}

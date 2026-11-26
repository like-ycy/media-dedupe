package video

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveToolPrefersOverride(t *testing.T) {
	dir := t.TempDir()
	name := "ffmpeg"
	if runtime.GOOS == "windows" {
		name = "ffmpeg.exe"
	}
	bin := filepath.Join(dir, name)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	ConfigureTools(dir, "")
	defer ConfigureTools("", "")

	got := resolveTool("ffmpeg", dir)
	if got != bin {
		t.Fatalf("resolveTool = %q, want %q", got, bin)
	}
	ff, _ := Tools()
	if ff != bin {
		t.Fatalf("Tools().ffmpeg = %q, want %q", ff, bin)
	}
}

func TestExpandPathHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home")
	}
	got := expandPath("~/ffmpeg-bin")
	want := filepath.Join(home, "ffmpeg-bin")
	if got != want {
		t.Fatalf("expandPath = %q, want %q", got, want)
	}
}

func TestCommonToolDirsNonEmpty(t *testing.T) {
	if len(commonToolDirs()) == 0 {
		t.Fatal("commonToolDirs should not be empty")
	}
}

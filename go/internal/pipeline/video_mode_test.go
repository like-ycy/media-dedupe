package pipeline

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"media-dedupe/internal/config"
	"media-dedupe/internal/progress"
	"media-dedupe/internal/video"
)

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSimpleVideoModeUsesContentFingerprintOnly guards the simple mode contract:
// byte-identical videos are grouped without ever invoking FFmpeg, and a file
// that differs only in its middle section is not grouped.
func TestSimpleVideoModeUsesContentFingerprintOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("counting wrapper uses sh")
	}
	ffmpeg, ffprobe := video.Tools()
	if ffmpeg == "" {
		t.Skip("FFmpeg is required for the invocation counter")
	}

	root := t.TempDir()
	size := config.QuickTierMinSize + 1<<20
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(root, "original.mp4")
	copied := filepath.Join(root, "copy.mp4")
	decoy := filepath.Join(root, "decoy.mp4")
	writeTestFile(t, original, data)
	writeTestFile(t, copied, data)
	// Same size, same head and tail, different middle.
	changed := append([]byte(nil), data...)
	copy(changed[size/2:], make([]byte, 4096))
	writeTestFile(t, decoy, changed)

	logPath := filepath.Join(t.TempDir(), "calls")
	t.Setenv("MEDIA_TEST_REAL_FFMPEG", ffmpeg)
	t.Setenv("MEDIA_TEST_FFMPEG_LOG", logPath)
	wrapper := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$MEDIA_TEST_FFMPEG_LOG\"\nexec \"$MEDIA_TEST_REAL_FFMPEG\" \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	video.ConfigureTools(wrapper, ffprobe)
	defer video.ConfigureTools("", "")

	opts := Options{
		Paths:            []string{root},
		CachePath:        filepath.Join(t.TempDir(), "cache.sqlite"),
		IncludeVideos:    true,
		VideoCompareMode: config.VideoModeSimple,
		FrameCount:       8,
		Workers:          2,
	}

	result, err := Scan(opts)
	if err != nil {
		t.Fatal(err)
	}
	if calls, readErr := os.ReadFile(logPath); readErr == nil && len(calls) > 0 {
		t.Fatalf("simple mode invoked FFmpeg: %s", calls)
	}
	if len(result.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(result.Groups))
	}
	group := result.Groups[0]
	if len(group.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(group.Items))
	}
	if group.Confidence != sampledFingerprintConfidence {
		t.Fatalf("confidence = %v, want %v", group.Confidence, sampledFingerprintConfidence)
	}
	for _, item := range group.Items {
		if item.Path == decoy {
			t.Fatalf("file with a different middle section was grouped")
		}
	}

	// Verification re-reads both files and promotes the claim to full identity.
	verifyOpts := opts
	verifyOpts.CachePath = filepath.Join(t.TempDir(), "verify.sqlite")
	verifyOpts.VerifySampledHash = true
	verified, err := Scan(verifyOpts)
	if err != nil {
		t.Fatal(err)
	}
	if len(verified.Groups) != 1 {
		t.Fatalf("verified groups = %d, want 1", len(verified.Groups))
	}
	if verified.Groups[0].Confidence != 1.0 {
		t.Fatalf("verified confidence = %v, want 1", verified.Groups[0].Confidence)
	}
}

// TestSimpleVideoModeSkipsFrameExtraction even when FFmpeg is configured and
// fine mode would have produced a video similarity pass.
func TestSimpleVideoModeSkipsFrameExtractionStage(t *testing.T) {
	root := t.TempDir()
	data := make([]byte, config.QuickTierMinSize+1024)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "only.mp4"), data)

	opts := Options{
		Paths:            []string{root},
		CachePath:        filepath.Join(t.TempDir(), "cache.sqlite"),
		FrameDir:         filepath.Join(t.TempDir(), "frames"),
		IncludeVideos:    true,
		VideoCompareMode: config.VideoModeSimple,
		Workers:          1,
	}
	var skipped bool
	opts.OnEvent = func(ev progress.Event) {
		if ev.Stage == progress.StageVideo && ev.Phase == "skipped" {
			skipped = true
		}
	}

	if _, err := Scan(opts); err != nil {
		t.Fatal(err)
	}
	if !skipped {
		t.Fatal("simple mode did not report a skipped video stage")
	}
	entries, err := os.ReadDir(opts.FrameDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) > 0 {
		t.Fatalf("simple mode wrote %d frame previews", len(entries))
	}
}

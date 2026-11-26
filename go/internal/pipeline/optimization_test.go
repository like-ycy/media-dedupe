package pipeline

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"media-dedupe/internal/cache"
	"media-dedupe/internal/model"
	"media-dedupe/internal/video"
)

func TestVideoExtractionCacheAndPreviews(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("counting wrapper uses sh")
	}
	ffmpeg, ffprobe := video.Tools()
	if ffmpeg == "" || ffprobe == "" {
		t.Skip("FFmpeg is required")
	}
	root := t.TempDir()
	input := filepath.Join(root, "sample.mp4")
	cmd := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=duration=6:size=320x180:rate=12", "-c:v", "libx264", "-preset", "ultrafast", "-threads", "2", "-g", "24", input)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate video: %v %s", err, out)
	}
	logPath := filepath.Join(t.TempDir(), "calls")
	t.Setenv("MEDIA_TEST_REAL_FFMPEG", ffmpeg)
	t.Setenv("MEDIA_TEST_FFMPEG_LOG", logPath)
	wrapper := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$MEDIA_TEST_FFMPEG_LOG\"\nexec \"$MEDIA_TEST_REAL_FFMPEG\" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	video.ConfigureTools(wrapper, ffprobe)
	defer video.ConfigureTools("", "")
	opts := Options{Paths: []string{root}, CachePath: filepath.Join(t.TempDir(), "cache.sqlite"), FrameDir: t.TempDir(), IncludeVideos: true, FrameCount: 8, VideoWorkers: 1, Workers: 1}
	run := func(wantCalls, wantPreviews int) []string {
		t.Helper()
		if err := os.WriteFile(logPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		result, err := Scan(opts)
		if err != nil || len(result.Errors) > 0 {
			t.Fatalf("scan err=%v result=%+v", err, result)
		}
		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		calls := strings.FieldsFunc(string(data), func(r rune) bool { return r == '\n' })
		if len(calls) != wantCalls {
			t.Fatalf("FFmpeg calls=%d want=%d: %s", len(calls), wantCalls, data)
		}
		for _, call := range calls {
			if strings.Contains(call, "fps=") || strings.Count(call, "-threads 2") != 2 || !strings.Contains(call, "-filter_threads 1") || strings.Index(call, "-ss ") > strings.Index(call, "-i ") {
				t.Fatalf("unbounded extraction: %s", call)
			}
		}
		c, err := cache.Open(opts.CachePath)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		var id int64
		if err := c.DB().QueryRow("SELECT id FROM files WHERE path = ?", input).Scan(&id); err != nil {
			t.Fatal(err)
		}
		paths, err := c.LoadFramePreviews(id)
		if err != nil || len(paths) != wantPreviews {
			t.Fatalf("previews=%v want=%d err=%v", paths, wantPreviews, err)
		}
		for _, path := range paths {
			if stat, err := os.Stat(path); err != nil || stat.Size() == 0 {
				t.Fatalf("preview missing: %s err=%v", path, err)
			}
		}
		return paths
	}
	paths := run(8, 8)
	run(0, 8)
	if err := os.Remove(paths[3]); err != nil {
		t.Fatal(err)
	}
	run(1, 8)
	opts.FrameCount = 4
	run(4, 4)
	run(0, 4)
	stamp := time.Now().Add(10 * time.Second)
	if err := os.Chtimes(input, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	run(4, 4)
	run(0, 4)

	// If changed-file extraction fails, a subsequent scan must not reuse old hashes.
	if err := os.WriteFile(input, []byte("invalid changed video"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		result, err := Scan(opts)
		if err != nil || len(result.Errors) == 0 {
			t.Fatalf("corrupt video should report errors: err=%v result=%+v", err, result)
		}
	}
	c, err := cache.Open(opts.CachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var stale int
	if err := c.DB().QueryRow("SELECT COUNT(*) FROM perceptual_hashes").Scan(&stale); err != nil || stale != 0 {
		t.Fatalf("stale hashes=%d err=%v", stale, err)
	}
}

func TestTranscodedVideosRemainSimilar(t *testing.T) {
	ffmpeg, ffprobe := video.Tools()
	if ffmpeg == "" || ffprobe == "" {
		t.Skip("FFmpeg is required")
	}
	root := t.TempDir()
	input := filepath.Join(root, "original.mp4")
	cmd := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=duration=8:size=640x360:rate=24", "-c:v", "libx264", "-preset", "ultrafast", "-threads", "2", "-g", "48", input)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate video: %v %s", err, out)
	}
	copyPath := filepath.Join(root, "recompressed.mp4")
	cmd = exec.Command(ffmpeg, "-v", "error", "-i", input, "-vf", "scale=320:180", "-c:v", "libx264", "-preset", "ultrafast", "-crf", "30", "-threads", "2", copyPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("transcode video: %v %s", err, out)
	}
	opts := Options{Paths: []string{root}, CachePath: filepath.Join(t.TempDir(), "cache.sqlite"), FrameDir: t.TempDir(), IncludeVideos: true, FrameCount: 8, VideoWorkers: 2, Workers: 1}
	for range 2 {
		result, err := Scan(opts)
		if err != nil || len(result.Errors) > 0 {
			t.Fatalf("scan err=%v result=%+v", err, result)
		}
		if len(result.Groups) != 1 || result.Groups[0].GroupType != model.GroupSimilarVideo || len(result.Groups[0].Items) != 2 {
			t.Fatalf("transcoded pair not matched: %+v", result.Groups)
		}
	}
	// A failure to write optional previews must not suppress similarity results.
	opts.FrameCount = 4
	opts.FrameDir = filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(opts.FrameDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Scan(opts)
	if err != nil || len(result.Errors) == 0 || len(result.Groups) != 1 {
		t.Fatalf("preview failure dropped results: err=%v result=%+v", err, result)
	}
	for _, failure := range result.Errors {
		if failure.Stage != "video_preview" {
			t.Fatalf("unexpected error: %+v", failure)
		}
	}
}

func TestLowFrameRateVideoUsesAvailableFrames(t *testing.T) {
	ffmpeg, ffprobe := video.Tools()
	if ffmpeg == "" || ffprobe == "" {
		t.Skip("FFmpeg is required")
	}
	root := t.TempDir()
	input := filepath.Join(root, "short.mp4")
	cmd := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=duration=1:size=320x180:rate=2", "-c:v", "libx264", "-preset", "ultrafast", "-threads", "2", input)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate video: %v %s", err, out)
	}
	opts := Options{Paths: []string{root}, CachePath: filepath.Join(t.TempDir(), "cache.sqlite"), FrameDir: t.TempDir(), IncludeVideos: true, FrameCount: 8, VideoWorkers: 1, Workers: 1}
	logPath := filepath.Join(t.TempDir(), "calls")
	if runtime.GOOS != "windows" {
		t.Setenv("MEDIA_TEST_REAL_FFMPEG", ffmpeg)
		t.Setenv("MEDIA_TEST_FFMPEG_LOG", logPath)
		wrapper := filepath.Join(t.TempDir(), "ffmpeg")
		if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$MEDIA_TEST_FFMPEG_LOG\"\nexec \"$MEDIA_TEST_REAL_FFMPEG\" \"$@\"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		video.ConfigureTools(wrapper, ffprobe)
		defer video.ConfigureTools("", "")
	}
	for pass := range 2 {
		if err := os.WriteFile(logPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		result, err := Scan(opts)
		if err != nil || len(result.Errors) != 0 {
			t.Fatalf("short readable video rejected: err=%v result=%+v", err, result)
		}
		if runtime.GOOS != "windows" {
			data, err := os.ReadFile(logPath)
			want := 8
			if pass == 1 {
				want = 0
			}
			if err != nil || strings.Count(string(data), "\n") != want {
				t.Fatalf("low-fps pass%d calls=%d want=%d err=%v", pass, strings.Count(string(data), "\n"), want, err)
			}
		}
	}
	c, err := cache.Open(opts.CachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var id int64
	if err := c.DB().QueryRow("SELECT id FROM files WHERE path = ?", input).Scan(&id); err != nil {
		t.Fatal(err)
	}
	hashes, err := c.LoadPerceptualHashes(id, video.FrameHashType(8))
	if err != nil || len(hashes) == 0 {
		t.Fatalf("no usable short-video hashes: %v %v", hashes, err)
	}
	paths, err := c.LoadFramePreviews(id)
	if err != nil || len(paths) == 0 {
		t.Fatalf("no short-video previews: %v %v", paths, err)
	}
	oldJPEG := filepath.Join(t.TempDir(), "old.jpg")
	if err := os.WriteFile(oldJPEG, []byte("old JPEG"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := video.ExtractFrameContext(context.Background(), input, 950, oldJPEG); !errors.Is(err, video.ErrNoFrame) {
		t.Fatalf("seek past last frame did not report no-frame: %v", err)
	}
	if _, err := os.Stat(oldJPEG); !os.IsNotExist(err) {
		t.Fatalf("old output survived empty seek: %v", err)
	}
}

func TestUnregisteredOldPreviewIsNotTrusted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("recording wrapper uses sh")
	}
	ffmpeg, ffprobe := video.Tools()
	if ffmpeg == "" || ffprobe == "" {
		t.Skip("FFmpeg is required")
	}
	root := t.TempDir()
	input := filepath.Join(root, "sample.mp4")
	cmd := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=duration=4:size=160x90:rate=12", "-c:v", "libx264", "-preset", "ultrafast", "-threads", "2", input)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate video: %v %s", err, out)
	}
	opts := Options{Paths: []string{root}, CachePath: filepath.Join(t.TempDir(), "cache.sqlite"), FrameDir: t.TempDir(), IncludeVideos: true, FrameCount: 4, VideoWorkers: 1, Workers: 1}
	if result, err := Scan(opts); err != nil || len(result.Errors) != 0 {
		t.Fatalf("initial scan err=%v result=%+v", err, result)
	}
	c, err := cache.Open(opts.CachePath)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := c.DB().QueryRow("SELECT id FROM files WHERE path = ?", input).Scan(&id); err != nil {
		t.Fatal(err)
	}
	c.Close()
	stalePath := framePreviewPath(opts.FrameDir, id, video.FrameHashType(4), 0)
	if err := os.Remove(stalePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stalePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stalePath, "block"), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(10 * time.Second)
	if err := os.Chtimes(input, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	result, err := Scan(opts)
	if err != nil || len(result.Errors) == 0 {
		t.Fatalf("preview cleanup/write failure not reported: err=%v result=%+v", err, result)
	}
	if err := os.RemoveAll(stalePath); err != nil {
		t.Fatal(err)
	}
	c, err = cache.Open(opts.CachePath)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := c.LoadFramePreviews(id)
	if err != nil || len(registered) != 0 {
		t.Fatalf("failed preview set was committed: paths=%v err=%v", registered, err)
	}
	// Simulate copy/cleanup failure: hashes are current, the old JPEG survives,
	// and no successful preview set has been committed for this fingerprint.
	if err := c.ReplaceFramePreviews(id, nil); err != nil {
		t.Fatal(err)
	}
	c.Close()
	if err := os.WriteFile(stalePath, []byte("old content"), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "calls")
	t.Setenv("MEDIA_TEST_REAL_FFMPEG", ffmpeg)
	t.Setenv("MEDIA_TEST_FFMPEG_LOG", logPath)
	wrapper := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$MEDIA_TEST_FFMPEG_LOG\"\nexec \"$MEDIA_TEST_REAL_FFMPEG\" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	video.ConfigureTools(wrapper, ffprobe)
	defer video.ConfigureTools("", "")
	if result, err := Scan(opts); err != nil || len(result.Errors) != 0 {
		t.Fatalf("retry err=%v result=%+v", err, result)
	}
	data, err := os.ReadFile(logPath)
	if err != nil || strings.Count(string(data), "\n") != 4 {
		t.Fatalf("unregistered previews not re-extracted: log=%s err=%v", data, err)
	}
	data, err = os.ReadFile(stalePath)
	if err != nil || string(data) == "old content" {
		t.Fatalf("stale preview reused: %v", err)
	}
}

func TestCancelActiveFrameExtraction(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("blocking wrapper uses sh")
	}
	started := filepath.Join(t.TempDir(), "started")
	t.Setenv("MEDIA_TEST_STARTED", started)
	wrapper := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nprintf started > \"$MEDIA_TEST_STARTED\"\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	video.ConfigureTools(wrapper, "")
	defer video.ConfigureTools("", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := video.ExtractFramesContext(ctx, "unused.mp4", model.VideoMetadata{DurationMs: 10000}, 8, t.TempDir())
		done <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("FFmpeg did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("FFmpeg was not canceled promptly")
	}
}

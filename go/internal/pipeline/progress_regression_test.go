package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"media-dedupe/internal/cache"
	"media-dedupe/internal/model"
	"media-dedupe/internal/progress"
)

func TestCorruptImageScanProgress(t *testing.T) {
	for _, workers := range []int{1, 4} {
		for _, healthy := range []bool{false, true} {
			t.Run(fmt.Sprintf("workers=%d/healthy=%t", workers, healthy), func(t *testing.T) {
				dir := t.TempDir()
				bad := filepath.Join(dir, "00-broken.jpg")
				if err := os.WriteFile(bad, []byte("broken Windows picture"), 0600); err != nil {
					t.Fatal(err)
				}
				files := 1
				if healthy {
					img := blobImage(96, 96, 42)
					writeJPEG(t, filepath.Join(dir, "01-good.jpg"), img, 95)
					writePNG(t, filepath.Join(dir, "02-good.png"), img)
					files += 2
				}
				var events []progress.Event
				cachePath := filepath.Join(t.TempDir(), "cache.sqlite")
				result, err := Scan(Options{
					Paths: []string{dir}, CachePath: cachePath, IncludeImages: true,
					Workers: workers, ProjectID: "progress-regression",
					OnEvent: func(ev progress.Event) { events = append(events, ev) },
				})
				if err != nil || result.Canceled {
					t.Fatalf("scan failed: result=%+v err=%v", result, err)
				}
				if len(result.Errors) != 1 || result.Errors[0].Kind != model.ErrorSkipped || result.Errors[0].Path != bad {
					t.Fatalf("skipped errors: %+v", result.Errors)
				}
				if healthy && (len(result.Groups) != 1 || len(result.Groups[0].Items) != 2) {
					t.Fatalf("healthy images were not compared: %+v", result.Groups)
				}
				var previous progress.Event
				var sawWarning, sawFileProgress bool
				for _, ev := range events {
					if ev.Percent < previous.Percent || ev.Errors < previous.Errors || ev.FilesSeen < previous.FilesSeen {
						t.Errorf("progress regressed: before=%+v after=%+v", previous, ev)
					}
					if ev.Stage == progress.StageError || ev.ProjectID != "progress-regression" {
						t.Errorf("unexpected event: %+v", ev)
					}
					if strings.Contains(ev.Message, "unknown format") {
						sawWarning = true
						if ev.Stage != progress.StageImage || ev.Percent < 40 || ev.FilesSeen != files || !strings.Contains(ev.Message, "已跳过") {
							t.Errorf("warning lost scan state: %+v", ev)
						}
					}
					if ev.Stage == progress.StageImage && ev.Phase == "running" && ev.Current == files && ev.Total == files && ev.Percent > 40 {
						sawFileProgress = true
					}
					previous = ev
				}
				if !sawWarning || !sawFileProgress || previous.Stage != progress.StageDone || previous.Percent != 100 || previous.Errors != 1 {
					t.Errorf("warning=%t fileProgress=%t final=%+v", sawWarning, sawFileProgress, previous)
				}
				c, err := cache.Open(cachePath)
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				errors, err := c.LoadErrors(nil)
				if err != nil || len(errors) != 1 || errors[0].Kind != model.ErrorSkipped {
					t.Fatalf("persisted errors=%+v err=%v", errors, err)
				}
				var remaining int
				if err := c.DB().QueryRow("SELECT COUNT(*) FROM files WHERE path = ?", bad).Scan(&remaining); err != nil || remaining != 0 {
					t.Fatalf("broken file cache count=%d err=%v", remaining, err)
				}
			})
		}
	}
}

func TestScanProgressFatalRemainsTerminal(t *testing.T) {
	var events []progress.Event
	state := &scanProgress{onEvent: func(ev progress.Event) { events = append(events, ev) }}
	state.emit(progress.Event{Stage: progress.StageImage, Phase: "running", Percent: 45, FilesSeen: 12})
	failures := newFailureTracker()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	count := 0
	hooks := &scanHooks{failures: failures, cancel: cancel, record: func(path, stage, kind, message string) {
		count++
		state.recordError(path, kind, message, count)
	}}
	hooks.file("disk.jpg", "image_metadata", syscall.EIO)
	state.emit(progress.Event{Stage: progress.StageImage, Phase: "done", Percent: 60})
	hooks.file("bad.jpg", "image_metadata", fmt.Errorf("corrupt JPEG"))
	state.emit(progress.Event{Stage: progress.StageDone, Phase: "done", Percent: 100})
	last := events[len(events)-1]
	if ctx.Err() == nil || failures.fatalErr() == nil || last.Stage != progress.StageError || last.Percent != 45 || last.FilesSeen != 12 || last.CurrentFile != "disk.jpg" || last.Errors != 2 {
		t.Fatalf("fatal failure lost: %+v, error=%v", last, failures.fatalErr())
	}
}

func TestCorruptImageScanCancelPreservesProgress(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.jpg")
	if err := os.WriteFile(bad, []byte("broken picture"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var previous progress.Event
	result, err := Scan(Options{
		Paths: []string{dir}, CachePath: filepath.Join(t.TempDir(), "cache.sqlite"), IncludeImages: true, Ctx: ctx,
		OnEvent: func(ev progress.Event) {
			if ev.Percent < previous.Percent || ev.Errors < previous.Errors {
				t.Errorf("cancellation lost progress: before=%+v after=%+v", previous, ev)
			}
			previous = ev
			if strings.Contains(ev.Message, "unknown format") {
				cancel()
			}
		},
	})
	if err != nil || !result.Canceled || previous.Stage != progress.StageCanceled || previous.Percent < 40 || previous.Errors != 1 {
		t.Fatalf("result=%+v err=%v final=%+v", result, err, previous)
	}
}

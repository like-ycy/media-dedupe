package appapi

import (
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"media-dedupe/internal/mediastore"
	"media-dedupe/internal/model"
	"media-dedupe/internal/progress"
	"media-dedupe/internal/project"
)

func TestScanStatusContinuesAfterCorruptImages(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a-broken.jpg"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(dir, "z-good.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	err = jpeg.Encode(file, image.NewRGBA(image.Rect(0, 0, 32, 32)), nil)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	store, err := project.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.Create(project.CreateInput{Name: "bad images", Paths: []string{dir}, IncludeImages: true})
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.store = store
	app.media = mediastore.New()
	for run := 0; run < 2; run++ {
		warnings := 0
		sawHealthyStart := false
		app.SetEmitter(func(name string, payload any) {
			if name == "scan:error" {
				t.Errorf("file failure aborted scan: %+v", payload)
			}
			if name != "scan:progress" {
				return
			}
			ev := payload.(progress.Event)
			if ev.Stage == progress.StageImage && ev.CurrentFile == file.Name() && ev.Current == 1 && ev.Total == 2 {
				sawHealthyStart = true
				status := app.GetScanStatus(p.ID)
				if !status.Running || status.Event.CurrentFile != file.Name() || status.Event.Current != 1 || status.Event.Total != 2 || status.Errors != 1 {
					t.Errorf("UI did not switch to healthy file before completion: %+v", status)
				}
			}
			if !strings.Contains(ev.Message, "unknown format") {
				return
			}
			warnings++
			status := app.GetScanStatus(p.ID)
			if !status.Running || status.LastError != "" || status.Event.Stage != progress.StageImage || status.Percent < 40 || status.FilesSeen != 2 || status.Errors != warnings {
				t.Errorf("UI status during warning: %+v", status)
			}
		})
		if err := app.StartScan(p.ID); err != nil {
			t.Fatal(err)
		}
		app.mu.Lock()
		done := app.scans[p.ID].done
		app.mu.Unlock()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("scan did not finish")
		}
		status := app.GetScanStatus(p.ID)
		if !sawHealthyStart || warnings != 1 || status.Running || status.LastError != "" || status.Percent != 100 || status.Errors != 1 || status.Event.Stage != progress.StageDone {
			t.Fatalf("run %d healthyStart=%t warnings=%d final status=%+v", run, sawHealthyStart, warnings, status)
		}
		errors, err := app.ListScanErrors(p.ID)
		if err != nil || len(errors) != 1 || errors[0].Kind != model.ErrorSkipped {
			t.Fatalf("run %d errors=%+v err=%v", run, errors, err)
		}
	}
}

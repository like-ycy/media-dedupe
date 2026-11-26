package appapi

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"media-dedupe/internal/model"
	"media-dedupe/internal/pipeline"
	"media-dedupe/internal/project"
)

func TestReviewFusedGroupDeleteRetainsOneFile(t *testing.T) {
	root := t.TempDir()
	mediaDir := filepath.Join(root, "media")
	if err := os.Mkdir(mediaDir, 0o700); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 96, 96))
	for y := 0; y < 96; y++ {
		for x := 0; x < 96; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 2), uint8(y * 2), uint8((x + y) % 128), 255})
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.jpg", "copy.jpeg", "similar.jpg"} {
		data := bytes.Clone(encoded.Bytes())
		if name == "similar.jpg" {
			data = append(data, []byte("trailing metadata")...)
		}
		if err := os.WriteFile(filepath.Join(mediaDir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := project.Open(filepath.Join(root, "app"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.Create(project.CreateInput{Name: "review", Paths: []string{mediaDir}, IncludeImages: true})
	if err != nil {
		t.Fatal(err)
	}
	st := project.DefaultSettings()
	st.AllowPermanentDelete = true
	if err := store.SaveSettings(st); err != nil {
		t.Fatal(err)
	}
	opts := pipeline.Options{Paths: p.Paths, CachePath: store.CachePath(p.ID), IncludeImages: true, Recursive: true}
	result, err := pipeline.Scan(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Groups) != 1 || len(result.Groups[0].Items) != 3 || result.Groups[0].GroupType != model.GroupSimilarImage {
		t.Fatalf("fusion: %+v", result.Groups)
	}
	g := result.Groups[0]
	repeat, err := pipeline.Scan(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(repeat.Groups) != 1 || repeat.Groups[0].StableKey != g.StableKey || repeat.Groups[0].RecommendedFileID != g.RecommendedFileID {
		t.Fatalf("rescan changed group: %+v", repeat.Groups)
	}
	app := &App{store: store}
	var all []string
	var keep string
	for _, item := range g.Items {
		all = append(all, item.Path)
		if item.FileID == g.RecommendedFileID {
			keep = item.Path
		}
	}
	if _, err := app.DeleteFiles(DeleteRequestDTO{ProjectID: p.ID, GroupIDs: []int64{g.GroupID}, Paths: all, Mode: "permanent"}); err == nil {
		t.Fatal("deleting every member should fail")
	}
	// Equal-quality visual matches require explicit review. Group-only deletion
	// must not silently promote them into cleanup candidates.
	if paths, _, err := app.expandCandidatePaths(p.ID, []int64{g.GroupID}); err != nil || len(paths) != 0 {
		t.Fatalf("review items auto-selected: %v %v", paths, err)
	}
	var selected []string
	for _, path := range all {
		if path != keep {
			selected = append(selected, path)
		}
	}
	deleted, err := app.DeleteFiles(DeleteRequestDTO{ProjectID: p.ID, GroupIDs: []int64{g.GroupID}, Paths: selected, Mode: "permanent"})
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted.Succeeded) != 2 || len(deleted.Failed) != 0 {
		t.Fatalf("delete: %+v", deleted)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("recommendation was deleted: %v", err)
	}
	entries, err := os.ReadDir(mediaDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("remaining files=%d err=%v", len(entries), err)
	}
}

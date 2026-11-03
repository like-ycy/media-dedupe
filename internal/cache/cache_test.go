package cache

import (
	"path/filepath"
	"testing"

	"media-dedupe/internal/model"
)

func TestIsUnchangedMustBeCheckedBeforeUpsert(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.sqlite")
	c, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	file := model.DiscoveredFile{
		Path:      "/tmp/a.jpg",
		MediaType: model.MediaImage,
		Extension: ".jpg",
		SizeBytes: 100,
		MTimeNs:   1_000,
	}

	if c.IsUnchanged(file) {
		t.Fatal("missing file should not be unchanged")
	}
	if _, err := c.UpsertFile(file); err != nil {
		t.Fatal(err)
	}
	if !c.IsUnchanged(file) {
		t.Fatal("same size+mtime should be unchanged after upsert")
	}

	// Simulate a content change: new size/mtime.
	changed := file
	changed.SizeBytes = 200
	changed.MTimeNs = 2_000

	// Check BEFORE upsert — pipeline must do the same.
	if c.IsUnchanged(changed) {
		t.Fatal("changed file must not be unchanged before upsert")
	}
	if _, err := c.UpsertFile(changed); err != nil {
		t.Fatal(err)
	}
	if !c.IsUnchanged(changed) {
		t.Fatal("after upsert, new metadata is current and looks unchanged")
	}
}

func TestUpsertInvalidatesAllDerivedDataOnlyOnChange(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	file := model.DiscoveredFile{Path: "/tmp/sample.mp4", MediaType: model.MediaVideo, Extension: ".mp4", SizeBytes: 100, MTimeNs: 10}
	id, err := c.UpsertFile(file)
	if err != nil {
		t.Fatal(err)
	}
	seed := func() {
		t.Helper()
		for _, err := range []error{
			c.SaveFileHash(id, file.SizeBytes, "full-hash"),
			c.SaveVideoMetadata(id, model.VideoMetadata{DurationMs: 10000, IsReadable: true}),
			c.SavePerceptualHashes(id, "video_frame_phash_v2_8", []string{"abc"}),
			c.SaveTextFact(id, file.SizeBytes, 10, "name", []uint64{1, 2}),
			c.ReplaceFramePreviews(id, []string{"frame.jpg"}),
		} {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	checkRows := func(want int) {
		t.Helper()
		for _, table := range []string{"file_hashes", "media_metadata", "perceptual_hashes", "text_facts", "frame_previews"} {
			var count int
			if err := c.DB().QueryRow("SELECT COUNT(*) FROM "+table+" WHERE file_id = ?", id).Scan(&count); err != nil || count != want {
				t.Fatalf("%s count=%d want=%d err=%v", table, count, want, err)
			}
		}
	}
	seed()
	if _, err := c.UpsertFile(file); err != nil {
		t.Fatal(err)
	}
	checkRows(1)
	for _, change := range []func(){
		func() { file.MTimeNs++ },
		func() { file.SizeBytes++ },
	} {
		change()
		if newID, err := c.UpsertFile(file); err != nil || newID != id {
			t.Fatalf("upsert id=%d want=%d err=%v", newID, id, err)
		}
		checkRows(0)
		// A retry after failed extraction must still see no stale derived data.
		if _, err := c.UpsertFile(file); err != nil {
			t.Fatal(err)
		}
		checkRows(0)
		seed()
	}
}

func TestReplaceFramePreviewsRemovesOldIndices(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	id, err := c.UpsertFile(model.DiscoveredFile{Path: "/tmp/v.mp4", MediaType: model.MediaVideo})
	if err != nil {
		t.Fatal(err)
	}
	for _, paths := range [][]string{{"a", "b", "c"}, {"new"}, nil} {
		if err := c.ReplaceFramePreviews(id, paths); err != nil {
			t.Fatal(err)
		}
		got, err := c.LoadFramePreviews(id)
		if err != nil || len(got) != len(paths) {
			t.Fatalf("previews=%v want=%v err=%v", got, paths, err)
		}
		for index := range paths {
			if got[index] != paths[index] {
				t.Fatalf("previews=%v want=%v", got, paths)
			}
		}
	}
}

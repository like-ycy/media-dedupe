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

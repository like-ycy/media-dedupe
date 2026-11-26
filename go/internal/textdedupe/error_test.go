package textdedupe

import (
	"errors"
	"io/fs"
	"path/filepath"
	"testing"

	"media-dedupe/internal/model"
)

// Read failures must keep their underlying error so callers can classify a
// missing file differently from a failing disk.
func TestFactsCachedKeepsUnderlyingError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone.txt")
	files := []model.DiscoveredFile{{Path: missing, MediaType: model.MediaText, Extension: ".txt"}}
	facts, errs := FactsCached(files, []int64{1}, nil, 1, nil)
	if len(facts) != 0 {
		t.Fatalf("facts=%d want=0", len(facts))
	}
	if len(errs) != 1 {
		t.Fatalf("errors=%d want=1", len(errs))
	}
	if !errors.Is(errs[0].Err, fs.ErrNotExist) {
		t.Fatalf("underlying error lost: %v", errs[0].Err)
	}
	if errs[0].Stage != "text" {
		t.Fatalf("stage=%s want text", errs[0].Stage)
	}
}

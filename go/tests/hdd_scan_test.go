package tests

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"media-dedupe/internal/cache"
	"media-dedupe/internal/discovery"
	"media-dedupe/internal/model"
	"media-dedupe/internal/pipeline"
)

func TestExactScanCandidatesAndCache(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"a/copy.txt": "same", "b/other.txt": "diff",
		"c/copy.TXT": "same", "d/unique.txt": "unique length",
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	opts := pipeline.Options{
		Paths: []string{root}, CachePath: filepath.Join(t.TempDir(), "cache.sqlite"),
		Recursive: true, IncludeTexts: true,
	}
	for _, workers := range []int{1, 3} {
		opts.Workers = workers
		result, err := pipeline.Scan(opts)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Errors) != 0 {
			t.Fatalf("scan errors: %v", result.Errors)
		}
		exact := 0
		for _, group := range result.Groups {
			if group.GroupType != model.GroupExact {
				continue
			}
			exact++
			if len(group.Items) != 2 {
				t.Fatalf("exact group: %+v", group)
			}
			for _, item := range group.Items {
				if filepath.Base(item.Path) != "copy.txt" && filepath.Base(item.Path) != "copy.TXT" {
					t.Fatalf("different content matched: %s", item.Path)
				}
			}
		}
		if exact != 1 {
			t.Fatalf("got %d exact groups, want 1", exact)
		}
		c, err := cache.Open(opts.CachePath)
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range discovery.Discover(context.Background(), opts.Paths, true, nil) {
			if !c.IsUnchanged(file) {
				t.Fatalf("file not cached: %s", file.Path)
			}
			id, err := c.UpsertFile(file)
			if err != nil {
				t.Fatal(err)
			}
			_, hashed, err := c.LoadFileHash(id)
			wantHash := filepath.Base(file.Path) != "unique.txt"
			if err != nil || hashed != wantHash {
				t.Fatalf("%s hashed=%v want=%v err=%v", file.Path, hashed, wantHash, err)
			}
		}
		c.Close()
	}
}

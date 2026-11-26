package discovery

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A canceled context (fatal storage error or user cancel) must stop the walk
// instead of enumerating the rest of the tree.
func TestDiscoverStopsOnCanceledContext(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.jpg", "nested/b.jpg"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := Discover(ctx, []string{root}, true, nil); len(got) != 0 {
		t.Fatalf("canceled discovery returned %d files", len(got))
	}
}

func TestDiscoverFindsFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.jpg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Discover(context.Background(), []string{root}, true, nil)
	if len(got) != 1 || filepath.Base(got[0].Path) != "a.jpg" {
		t.Fatalf("got %+v", got)
	}
}

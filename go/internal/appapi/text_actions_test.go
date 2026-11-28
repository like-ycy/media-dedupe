package appapi

import (
	"media-dedupe/internal/fsutil"
	"media-dedupe/internal/mediastore"
	"media-dedupe/internal/model"
	"media-dedupe/internal/project"
	"os"
	"path/filepath"
	"testing"
)

func TestTextBulkDeleteOnlyConfirmedCopies(t *testing.T) {
	root := t.TempDir()
	store, err := project.Open(filepath.Join(root, "app"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.Create(project.CreateInput{Name: "TXT", Paths: []string{root}, IncludeTexts: true})
	if err != nil {
		t.Fatal(err)
	}
	settings := project.DefaultSettings()
	settings.AllowPermanentDelete = true
	if err := store.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	app := &App{store: store, media: mediastore.New()}
	c, err := app.openProjectCache(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	var items []model.ReportItem
	for i, name := range []string{"reference.txt", "other-edition.txt", "confirmed-copy.txt"} {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte("test"), 0600); err != nil {
			t.Fatal(err)
		}
		// Store what a scan would have recorded: delete re-checks size and
		// timestamp against this row before touching the file.
		size, mtimeNs, inode, device, err := fsutil.SafeStat(path)
		if err != nil {
			t.Fatal(err)
		}
		id, err := c.UpsertFile(model.DiscoveredFile{
			Path: path, MediaType: model.MediaText, SizeBytes: size, MTimeNs: mtimeNs, Inode: inode, Device: device,
		})
		if err != nil {
			t.Fatal(err)
		}
		action := model.ActionReview
		if i == 2 {
			action = model.ActionCleanup
		}
		items = append(items, model.ReportItem{FileID: id, Path: path, Action: action, SizeBytes: size})
	}
	if err := c.ReplaceReportGroups([]model.ReportGroup{{GroupID: 1, GroupType: model.GroupTextName, RecommendedFileID: items[0].FileID, Items: items}}, 0); err != nil {
		t.Fatal(err)
	}
	c.Close()
	paths, _, err := app.expandCandidatePaths(p.ID, []int64{1})
	if err != nil || len(paths) != 1 || paths[0] != items[2].Path {
		t.Fatalf("expanded review files: %v %v", paths, err)
	}
	gs, err := app.ListGroups(p.ID, GroupFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if gs.Items[0].ReclaimableBytes != 4 || gs.Items[0].CleanupCount != 1 {
		t.Fatalf("incorrect reclaimable bytes %+v", gs.Items[0])
	}
	if err := os.Remove(items[1].Path); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DeleteFiles(DeleteRequestDTO{ProjectID: p.ID, GroupIDs: []int64{1}, Mode: "permanent"}); err == nil {
		t.Fatal("cleaned a mixed group with a missing retained version")
	}
	if _, err := os.Stat(items[2].Path); err != nil {
		t.Fatal("deleted the last copy after its representative disappeared")
	}
	if err := os.WriteFile(items[1].Path, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := app.DeleteFiles(DeleteRequestDTO{ProjectID: p.ID, GroupIDs: []int64{1}, Mode: "permanent"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Succeeded) != 1 || len(result.MarkedProcessed) != 0 {
		t.Fatalf("partial review group marked finished: %+v", result)
	}
	for _, it := range items[:2] {
		if _, err := os.Stat(it.Path); err != nil {
			t.Fatalf("deleted review item: %v", err)
		}
	}
	paths, _, err = app.expandCandidatePaths(p.ID, []int64{1})
	if err != nil || len(paths) != 0 {
		t.Fatalf("stale deletion candidates: %v %v", paths, err)
	}
}

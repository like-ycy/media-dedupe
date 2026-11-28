package appapi

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"media-dedupe/internal/fsutil"
	"media-dedupe/internal/mediastore"
	"media-dedupe/internal/model"
	"media-dedupe/internal/project"
)

// The results table is ordered, filtered and paged by the server so a page
// reflects the whole result set. This covers the API mapping (sort pass-through,
// media kind, label); the SQL itself is covered in the cache package.
func TestListGroupsOrdersAndLabelsFromServer(t *testing.T) {
	root := t.TempDir()
	store, err := project.Open(filepath.Join(root, "app"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.Create(project.CreateInput{Name: "list", Paths: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{store: store, media: mediastore.New()}
	c, err := app.openProjectCache(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	add := func(name string, size int64, kind model.MediaType, action model.Action) model.ReportItem {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		sizeBytes, mtimeNs, inode, device, err := fsutil.SafeStat(path)
		if err != nil {
			t.Fatal(err)
		}
		id, err := c.UpsertFile(model.DiscoveredFile{
			Path: path, MediaType: kind, SizeBytes: sizeBytes, MTimeNs: mtimeNs, Inode: inode, Device: device,
		})
		if err != nil {
			t.Fatal(err)
		}
		return model.ReportItem{FileID: id, Path: path, Action: action, SizeBytes: sizeBytes}
	}

	small := []model.ReportItem{
		add("small-keep.bin", 4, model.MediaImage, model.ActionKeep),
		add("small-a.bin", 8, model.MediaImage, model.ActionCleanup),
	}
	large := []model.ReportItem{
		add("large-keep.bin", 4, model.MediaVideo, model.ActionKeep),
		add("large-a.bin", 900, model.MediaVideo, model.ActionCleanup),
	}
	if err := c.ReplaceReportGroups([]model.ReportGroup{
		{GroupID: 1, GroupType: model.GroupExact, Confidence: 1, RecommendedFileID: small[0].FileID, Items: small},
		{GroupID: 2, GroupType: model.GroupExact, Confidence: 0.5, RecommendedFileID: large[0].FileID, Items: large},
	}, 0); err != nil {
		t.Fatal(err)
	}

	byConfidence, err := app.ListGroups(p.ID, GroupFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(byConfidence.Items) != 2 || byConfidence.Items[0].GroupID != 1 {
		t.Fatalf("default order: %+v", byConfidence)
	}
	if byConfidence.Total != 2 || byConfidence.HasMore {
		t.Fatalf("page envelope: %+v", byConfidence)
	}
	if byConfidence.Items[0].MediaKind != "image" || byConfidence.Items[1].MediaKind != "video" {
		t.Fatalf("media kind: %+v", byConfidence.Items)
	}
	if byConfidence.Items[0].TypeLabel == "" || byConfidence.Items[0].CleanupCount != 1 {
		t.Fatalf("labels: %+v", byConfidence.Items[0])
	}

	bySize, err := app.ListGroups(p.ID, GroupFilter{Sort: "size"})
	if err != nil {
		t.Fatal(err)
	}
	if len(bySize.Items) != 2 || bySize.Items[0].GroupID != 2 {
		t.Fatalf("size order: %+v", bySize)
	}

	page, err := app.ListGroups(p.ID, GroupFilter{Sort: "id", Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].GroupID != 2 || page.Total != 2 || page.HasMore {
		t.Fatalf("page: %+v", page)
	}

	// The project header is derived from the same data, so it must agree with the
	// list rather than with a stale stored summary.
	if err := app.refreshLastSummary(p.ID); err != nil {
		t.Fatal(err)
	}
	stored, ok := store.Get(p.ID)
	if !ok {
		t.Fatal("project disappeared")
	}
	if stored.LastSummary.Groups != 2 || stored.LastSummary.PendingGroups != 2 || stored.LastSummary.ReclaimableBytes != 908 {
		t.Fatalf("summary: %+v", stored.LastSummary)
	}
}

// The results table used to request 500 rows and stop, so group 501 of 620 was
// unreachable no matter what the user did. Paging must reach the last row and
// report the true total while doing it.
func TestListGroupsPagesBeyondTheOldCap(t *testing.T) {
	root := t.TempDir()
	store, err := project.Open(filepath.Join(root, "app"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.Create(project.CreateInput{Name: "paged", Paths: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{store: store, media: mediastore.New()}
	c, err := app.openProjectCache(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	const (
		groupCount = 620
		pageSize   = 100
	)
	all := make([]model.ReportGroup, 0, groupCount)
	for gid := 1; gid <= groupCount; gid++ {
		var items []model.ReportItem
		for _, name := range []string{"keep.bin", "dup.bin"} {
			path := filepath.Join(root, fmt.Sprintf("g%d-%s", gid, name))
			id, err := c.UpsertFile(model.DiscoveredFile{Path: path, MediaType: model.MediaImage, SizeBytes: 16, MTimeNs: 1})
			if err != nil {
				t.Fatal(err)
			}
			action := model.ActionKeep
			if name == "dup.bin" {
				action = model.ActionCleanup
			}
			items = append(items, model.ReportItem{FileID: id, Path: path, Action: action, SizeBytes: 16})
		}
		all = append(all, model.ReportGroup{
			GroupID: int64(gid), GroupType: model.GroupExact, Confidence: 1,
			RecommendedFileID: items[0].FileID, Items: items,
		})
	}
	if err := c.ReplaceReportGroups(all, 0); err != nil {
		t.Fatal(err)
	}

	first, err := app.ListGroups(p.ID, GroupFilter{Sort: "id"})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != pageSize || first.Limit != pageSize || !first.HasMore {
		t.Fatalf("default page: limit=%d len=%d hasMore=%v", first.Limit, len(first.Items), first.HasMore)
	}
	if first.Total != groupCount {
		t.Fatalf("total must cover every match, got %d", first.Total)
	}

	last, err := app.ListGroups(p.ID, GroupFilter{Sort: "id", Limit: pageSize, Offset: 600})
	if err != nil {
		t.Fatal(err)
	}
	if len(last.Items) != groupCount-600 || last.HasMore {
		t.Fatalf("last page: len=%d hasMore=%v", len(last.Items), last.HasMore)
	}
	if last.Items[len(last.Items)-1].GroupID != groupCount {
		t.Fatalf("group %d unreachable, last row is %d", groupCount, last.Items[len(last.Items)-1].GroupID)
	}

	// A client asking for everything still gets one bounded page; the ceiling is
	// the server's, not the caller's.
	clamped, err := app.ListGroups(p.ID, GroupFilter{Sort: "id", Limit: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if clamped.Limit != 500 || len(clamped.Items) != 500 || !clamped.HasMore {
		t.Fatalf("clamp: limit=%d len=%d hasMore=%v", clamped.Limit, len(clamped.Items), clamped.HasMore)
	}

	// Offset past the end is an empty page, not an error.
	beyond, err := app.ListGroups(p.ID, GroupFilter{Sort: "id", Offset: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if len(beyond.Items) != 0 || beyond.HasMore || beyond.Total != groupCount {
		t.Fatalf("beyond end: %+v", beyond)
	}
}

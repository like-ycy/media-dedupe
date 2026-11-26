package cache

import (
	"media-dedupe/internal/model"
	"path/filepath"
	"testing"
)

func TestTextFingerprintMigrationAndRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.sqlite")
	c, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	id, err := c.UpsertFile(model.DiscoveredFile{Path: "novel.txt", MediaType: model.MediaText, SizeBytes: 20, MTimeNs: 1})
	if err != nil {
		t.Fatal(err)
	}
	want := model.TextFingerprint{Digest: "sha256", Length: 10, Features: []uint64{1, 4, 12}}
	if err := c.SaveTextFact(id, 20, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := c.LoadTextFact(id, 20)
	if err != nil || !ok || got.Digest != want.Digest || got.Length != want.Length || len(got.Features) != 3 {
		t.Fatalf("roundtrip %+v %v %v", got, ok, err)
	}
	if _, ok, _ := c.LoadTextFact(id, 21); ok {
		t.Fatal("accepted different size")
	}
	if _, err := c.DB().Exec(`UPDATE text_facts SET version=3`); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := c.LoadTextFact(id, 20); ok {
		t.Fatal("accepted legacy algorithm")
	}
	if _, err := c.DB().Exec(`ALTER TABLE text_facts DROP COLUMN text_hash`); err != nil {
		t.Fatal(err)
	}
	c.Close()
	c, err = Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.SaveTextFact(id, 20, want); err != nil {
		t.Fatalf("old schema not migrated: %v", err)
	}
}

func TestTextReviewNeverBecomesCleanupOnReferenceChange(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var ids []int64
	for _, p := range []string{"a.txt", "b.txt", "c.txt"} {
		id, err := c.UpsertFile(model.DiscoveredFile{Path: p, MediaType: model.MediaText})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	g := model.ReportGroup{GroupID: 1, GroupType: model.GroupTextName, RecommendedFileID: ids[0], Items: []model.ReportItem{{FileID: ids[0], Action: model.ActionReview}, {FileID: ids[1], Action: model.ActionReview}, {FileID: ids[2], Action: model.ActionCleanup}}}
	if err := c.ReplaceReportGroups([]model.ReportGroup{g}, 0); err != nil {
		t.Fatal(err)
	}
	if err := c.UpdateRecommended(1, 999); err == nil {
		t.Fatal("accepted non-member")
	}
	if err := c.UpdateRecommended(1, ids[1]); err != nil {
		t.Fatal(err)
	}
	gs, err := c.LoadReportGroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range gs[0].Items {
		if it.Action != model.ActionReview {
			t.Fatalf("promoted to cleanup %+v", it)
		}
	}
	if _, err := c.DeleteFilesByPaths([]string{"b.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := c.PruneReportGroups(); err != nil {
		t.Fatal(err)
	}
	gs, err = c.LoadReportGroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range gs[0].Items {
		if it.Action != model.ActionReview {
			t.Fatalf("pruning promoted to cleanup %+v", it)
		}
	}
}

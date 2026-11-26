package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"media-dedupe/internal/model"
)

func TestReviewFusionPreservesExactWinnerWithAlternateRepresentative(t *testing.T) {
	root := t.TempDir()
	img := blobImage(240, 180, 7)
	writeJPEG(t, filepath.Join(root, "a.jpg"), img, 92)
	data, err := os.ReadFile(filepath.Join(root, "a.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.heic"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	opts := Options{Paths: []string{root}, CachePath: filepath.Join(t.TempDir(), "cache.sqlite"), IncludeImages: true, Recursive: true}
	first, err := Scan(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Groups) != 1 {
		t.Fatalf("exact groups: %+v", first.Groups)
	}
	winner := first.Groups[0].RecommendedFileID
	writeJPEG(t, filepath.Join(root, "c.jpg"), img, 55)
	for i := 0; i < 2; i++ {
		result, err := Scan(opts)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Groups) != 1 || len(result.Groups[0].Items) != 3 {
			t.Fatalf("fused groups: %+v", result.Groups)
		}
		if result.Groups[0].RecommendedFileID != winner {
			t.Fatalf("winner changed from %d to %d", winner, result.Groups[0].RecommendedFileID)
		}
		for _, item := range result.Groups[0].Items {
			if item.FileID == winner && item.Similarity != 1 {
				t.Fatalf("winner similarity: %v", item.Similarity)
			}
		}
	}
}

func TestReviewDedupeRealignsChangedRecommendation(t *testing.T) {
	groups := []model.ReportGroup{
		{GroupType: model.GroupExact, RecommendedFileID: 1, Items: []model.ReportItem{{FileID: 1, Path: "/a"}, {FileID: 2, Path: "/b"}}},
		{GroupType: model.GroupSimilarImage, RecommendedFileID: 1, Items: []model.ReportItem{
			{FileID: 1, Path: "/a", QualityScore: 1, Similarity: 1},
			{FileID: 3, Path: "/c", QualityScore: .8, Similarity: .97},
			{FileID: 4, Path: "/d", QualityScore: .5, Similarity: .96},
		}},
	}
	got := dedupeGroupsByPath(groups)
	if len(got) != 2 || got[1].RecommendedFileID != 3 {
		t.Fatalf("groups: %+v", got)
	}
	if got[1].Items[0].Similarity != 1 || got[1].Items[1].Similarity >= 0 {
		t.Fatalf("old recommendation similarities leaked: %+v", got[1].Items)
	}
}

func TestReviewDiscardedGroupDoesNotClaimPaths(t *testing.T) {
	groups := []model.ReportGroup{
		{RecommendedFileID: 1, Items: []model.ReportItem{{FileID: 1, Path: "/a"}, {FileID: 2, Path: "/b"}}},
		{RecommendedFileID: 1, Items: []model.ReportItem{{FileID: 1, Path: "/a"}, {FileID: 3, Path: "/c"}}},
		{RecommendedFileID: 3, Items: []model.ReportItem{{FileID: 3, Path: "/c"}, {FileID: 4, Path: "/d"}}},
	}
	if got := dedupeGroupsByPath(groups); len(got) != 2 {
		t.Fatalf("discarded singleton hid a later group: %+v", got)
	}
}

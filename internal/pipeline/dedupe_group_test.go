package pipeline

import (
	"os/exec"
	"path/filepath"
	"testing"

	"media-dedupe/internal/cache"
	"media-dedupe/internal/match"
	"media-dedupe/internal/model"
	"media-dedupe/internal/video"
)

// TestGroupsReportSimilarityToRecommendation covers P1-2: a chained group must
// not advertise 100% for a member that was never compared with the file being
// kept, and the group confidence must expose the missing link.
func TestGroupsReportSimilarityToRecommendation(t *testing.T) {
	items := []pathQuality{
		{fileID: 1, path: "/a", size: 10, quality: 0.9},
		{fileID: 2, path: "/b", size: 10, quality: 0.8},
		{fileID: 3, path: "/c", size: 10, quality: 0.7},
	}
	// A≈B≈C, but A and C were never compared.
	edges := []model.SimilarityEdge{
		{LeftFileID: 1, RightFileID: 2, GroupType: model.GroupSimilarImage, Similarity: 0.97},
		{LeftFileID: 2, RightFileID: 3, GroupType: model.GroupSimilarImage, Similarity: 0.96},
	}
	edgeSim := map[[2]int64]float64{
		match.EdgeKey(1, 2): 0.97,
		match.EdgeKey(2, 3): 0.96,
	}
	groups := groupsFromComponents(items, edges, edgeSim, model.GroupSimilarImage, "test")
	if len(groups) != 1 {
		t.Fatalf("expected one group, got %+v", groups)
	}
	group := groups[0]
	if group.RecommendedFileID != 1 {
		t.Fatalf("expected file 1 to be recommended, got %d", group.RecommendedFileID)
	}
	similarity := map[int64]float64{}
	for _, item := range group.Items {
		similarity[item.FileID] = item.Similarity
	}
	if similarity[1] != 1.0 {
		t.Fatalf("recommended file should report 100%%, got %v", similarity[1])
	}
	if similarity[2] != 0.97 {
		t.Fatalf("direct neighbour should report its edge, got %v", similarity[2])
	}
	if similarity[3] >= 0 {
		t.Fatalf("uncompared member must not report a similarity, got %v", similarity[3])
	}
	if group.Confidence != 0 {
		t.Fatalf("chained group confidence must count the missing edge, got %v", group.Confidence)
	}
}

// TestFuseExactGroupsMergesSiblings covers P0-2 at the group level.
func TestFuseExactGroupsMergesSiblings(t *testing.T) {
	exact := []model.ReportGroup{{
		GroupType:         model.GroupExact,
		Confidence:        1,
		RecommendedFileID: 1,
		Items: []model.ReportItem{
			{FileID: 1, Path: "/a", QualityScore: 0.8, Reasons: []string{"identical SHA-256 file hash"}},
			{FileID: 2, Path: "/a-copy", QualityScore: 0.8, Reasons: []string{"identical SHA-256 file hash"}},
		},
	}}
	similar := []model.ReportGroup{{
		GroupType:         model.GroupSimilarImage,
		Confidence:        0.97,
		RecommendedFileID: 1,
		Items: []model.ReportItem{
			{FileID: 1, Path: "/a", Action: model.ActionKeep, Similarity: 1, QualityScore: 0.8, Reasons: []string{"visual image hash match"}},
			{FileID: 3, Path: "/b", Action: model.ActionCleanup, Similarity: 0.97, QualityScore: 0.6, Reasons: []string{"visual image hash match"}},
		},
	}}
	reps := map[int64]int64{1: 1, 2: 1}
	groupOfRep := map[int64]int{1: 0}

	fused, absorbed := fuseExactGroups(similar, exact, reps, groupOfRep)
	if _, ok := absorbed[0]; !ok {
		t.Fatalf("exact group should be absorbed, got %v", absorbed)
	}
	if len(fused) != 1 {
		t.Fatalf("expected one fused group, got %+v", fused)
	}
	group := fused[0]
	if len(group.Items) != 3 {
		t.Fatalf("expected three members, got %+v", group.Items)
	}
	actions := map[int64]model.Action{}
	for _, item := range group.Items {
		actions[item.FileID] = item.Action
		switch item.FileID {
		case 2:
			if item.Similarity != 1.0 {
				t.Fatalf("byte-identical sibling should be 100%%, got %v", item.Similarity)
			}
			if item.Action == model.ActionReview {
				t.Fatalf("sibling should get an actionable verdict")
			}
		case 3:
			if item.Similarity != 0.97 {
				t.Fatalf("similar member should keep its edge, got %v", item.Similarity)
			}
		}
	}
	if group.RecommendedFileID != 1 {
		t.Fatalf("exact winner should stay recommended, got %d", group.RecommendedFileID)
	}
	if group.GroupType != model.GroupSimilarImage {
		t.Fatalf("fused group keeps the similarity type, got %s", group.GroupType)
	}
	if actions[1] != model.ActionKeep {
		t.Fatalf("recommended file keeps its verdict, got %s", actions[1])
	}
	if actions[3] != model.ActionCleanup {
		t.Fatalf("similar member keeps its verdict, got %s", actions[3])
	}
}

// TestFuseExactGroupsKeepsTextRecommendation covers the text ranking rule:
// content-similar text files recommend the largest one, and their quality score
// is capped, so rescoring the fused group by quality would swap the
// recommendation to a smaller file.
func TestFuseExactGroupsKeepsTextRecommendation(t *testing.T) {
	exact := []model.ReportGroup{{
		GroupType:         model.GroupExact,
		Confidence:        1,
		RecommendedFileID: 4,
		Items: []model.ReportItem{
			{FileID: 4, Path: "/small.txt", Action: model.ActionKeep, QualityScore: 1, SizeBytes: 31_000_000},
			{FileID: 6, Path: "/small-copy.txt", Action: model.ActionCleanup, QualityScore: 1, SizeBytes: 31_000_000},
		},
	}}
	similar := []model.ReportGroup{{
		GroupType:         model.GroupSimilarText,
		Confidence:        0.95,
		RecommendedFileID: 5,
		Items: []model.ReportItem{
			{FileID: 4, Path: "/small.txt", Action: model.ActionReview, Similarity: 0.95, QualityScore: 1, SizeBytes: 31_000_000},
			{FileID: 5, Path: "/big.txt", Action: model.ActionKeep, Similarity: 1, QualityScore: 1, SizeBytes: 32_000_000},
		},
	}}
	reps := map[int64]int64{4: 4, 6: 4}
	groupOfRep := map[int64]int{4: 0}

	fused, absorbed := fuseExactGroups(similar, exact, reps, groupOfRep)
	if _, ok := absorbed[0]; !ok {
		t.Fatalf("exact group should be absorbed, got %v", absorbed)
	}
	group := fused[0]
	if group.RecommendedFileID != 5 {
		t.Fatalf("largest text file must stay recommended, got %d", group.RecommendedFileID)
	}
	for _, item := range group.Items {
		switch item.FileID {
		case 5:
			if item.Action != model.ActionKeep {
				t.Fatalf("big.txt should keep, got %s", item.Action)
			}
		case 4:
			if item.Action != model.ActionReview {
				t.Fatalf("reviewing a similar text file must survive fusion, got %s", item.Action)
			}
		case 6:
			if item.Action == model.ActionKeep {
				t.Fatalf("only one file may be kept, got keep for the copy")
			}
		}
	}
}

// TestDedupeGroupsByPath keeps the first occurrence of a path so the delete
// panel cannot count one file twice.
func TestDedupeGroupsByPath(t *testing.T) {
	groups := []model.ReportGroup{
		{GroupType: model.GroupExact, RecommendedFileID: 1, Items: []model.ReportItem{
			{FileID: 1, Path: "/a", QualityScore: 0.9},
			{FileID: 2, Path: "/b", QualityScore: 0.8},
		}},
		{GroupType: model.GroupSimilarImage, RecommendedFileID: 2, Items: []model.ReportItem{
			{FileID: 2, Path: "/b", QualityScore: 0.8},
			{FileID: 3, Path: "/c", QualityScore: 0.7},
		}},
	}
	out := dedupeGroupsByPath(groups)
	if len(out) != 1 {
		t.Fatalf("expected the duplicate group to be dropped, got %+v", out)
	}
	if len(out[0].Items) != 2 {
		t.Fatalf("expected the first group to stay intact, got %+v", out[0].Items)
	}
}

// TestDedupeGroupsByPathKeepsCaseDistinctFiles checks that two paths differing
// only in case stay separate: on Linux, and on case-sensitive APFS or Windows
// directories, they are two files, and folding case would drop a member and
// dissolve a two-member exact group.
func TestDedupeGroupsByPathKeepsCaseDistinctFiles(t *testing.T) {
	groups := []model.ReportGroup{{
		GroupType:         model.GroupExact,
		RecommendedFileID: 1,
		Items: []model.ReportItem{
			{FileID: 1, Path: "/media/A.jpg", QualityScore: 0.9},
			{FileID: 2, Path: "/media/a.jpg", QualityScore: 0.9},
		},
	}}
	out := dedupeGroupsByPath(groups)
	if len(out) != 1 || len(out[0].Items) != 2 {
		t.Fatalf("paths differing in case must both survive, got %+v", out)
	}
}

// TestDedupeGroupsByPathDropsRepeatedFile: the same file reached twice is still
// reported once, even if the paths were written differently.
func TestDedupeGroupsByPathDropsRepeatedFile(t *testing.T) {
	groups := []model.ReportGroup{
		{GroupType: model.GroupExact, RecommendedFileID: 1, Items: []model.ReportItem{
			{FileID: 1, Path: "/media/a.jpg", QualityScore: 0.9},
			{FileID: 2, Path: "/media/b.jpg", QualityScore: 0.8},
		}},
		{GroupType: model.GroupSimilarImage, RecommendedFileID: 1, Items: []model.ReportItem{
			{FileID: 1, Path: "/media/./a.jpg", QualityScore: 0.9},
			{FileID: 3, Path: "/media/c.jpg", QualityScore: 0.7},
		}},
	}
	out := dedupeGroupsByPath(groups)
	if len(out) != 1 {
		t.Fatalf("the second group must not repeat file 1, got %+v", out)
	}
	if ids := []int64{out[0].Items[0].FileID, out[0].Items[1].FileID}; ids[0] != 1 || ids[1] != 2 {
		t.Fatalf("unexpected members: %v", ids)
	}
}

// TestVideoFrameHashesStoreTimestamps covers P1-1's storage contract: hashes are
// persisted with their timeline position, and the cache key moved to v3 so old
// position-less rows are never reused.
func TestVideoFrameHashesStoreTimestamps(t *testing.T) {
	ffmpeg, _ := video.Tools()
	if ffmpeg == "" {
		t.Skip("FFmpeg is required")
	}
	root := t.TempDir()
	input := filepath.Join(root, "clip.mp4")
	cmd := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=duration=6:size=320x180:rate=12", "-c:v", "libx264", "-preset", "ultrafast", "-threads", "2", input)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate video: %v %s", err, out)
	}
	cachePath := filepath.Join(t.TempDir(), "cache.sqlite")
	result := scanDir(t, root, Options{IncludeVideos: true, CachePath: cachePath, FrameCount: 8})
	if len(result.Errors) > 0 {
		t.Fatalf("unexpected errors: %+v", result.Errors)
	}

	c, err := cache.Open(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var fileID int64
	if err := c.DB().QueryRow("SELECT id FROM files WHERE path = ?", input).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	hashType := video.FrameHashType(8)
	if hashType != "video_frame_phash_v3_8" {
		t.Fatalf("unexpected cache key %q", hashType)
	}
	rows, err := c.LoadFrameHashes(fileID, hashType)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("no frame hashes stored")
	}
	last := int64(-1)
	for _, row := range rows {
		if row.TimestampMs <= 0 {
			t.Fatalf("frame %d has no timestamp", row.Index)
		}
		if row.TimestampMs <= last {
			t.Fatalf("timestamps are not increasing: %v", rows)
		}
		last = row.TimestampMs
	}
}

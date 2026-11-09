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

func seedGroupFiles(t *testing.T, c *Cache, paths ...string) []int64 {
	t.Helper()
	ids := make([]int64, 0, len(paths))
	for _, path := range paths {
		id, err := c.UpsertFile(model.DiscoveredFile{Path: path, MediaType: model.MediaImage, Extension: ".jpg"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// Removing a stored file must not leave a group with a dangling recommendation.
func TestPruneReportGroupsRepairsRecommendation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.sqlite")
	c, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ids := seedGroupFiles(t, c, "/tmp/a.jpg", "/tmp/b.jpg", "/tmp/c.jpg")
	err = c.ReplaceReportGroups([]model.ReportGroup{{
		GroupType:         model.GroupSimilarImage,
		Confidence:        0.9,
		StableKey:         "key",
		RecommendedFileID: ids[0],
		Items: []model.ReportItem{
			{FileID: ids[0], Path: "/tmp/a.jpg", Action: model.ActionKeep, QualityScore: 0.9},
			{FileID: ids[1], Path: "/tmp/b.jpg", Action: model.ActionCleanup, QualityScore: 0.7},
			{FileID: ids[2], Path: "/tmp/c.jpg", Action: model.ActionCleanup, QualityScore: 0.8},
		},
	}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeleteFilesByPaths([]string{"/tmp/a.jpg"}); err != nil {
		t.Fatal(err)
	}
	if err := c.PruneReportGroups(); err != nil {
		t.Fatal(err)
	}
	groups, err := c.LoadReportGroups()
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups=%d want=1", len(groups))
	}
	if groups[0].RecommendedFileID != ids[2] {
		t.Fatalf("recommended=%d want=%d", groups[0].RecommendedFileID, ids[2])
	}
	for _, it := range groups[0].Items {
		want := model.ActionCleanup
		if it.FileID == ids[2] {
			want = model.ActionKeep
		}
		if it.Action != want {
			t.Fatalf("file %d action=%s want=%s", it.FileID, it.Action, want)
		}
	}
}

// Review markers must survive the repair: re-picking a recommendation is not a
// reason to tell the user which copy to keep.
func TestPruneReportGroupsKeepsReviewMarkers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.sqlite")
	c, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ids := seedGroupFiles(t, c, "/tmp/a.jpg", "/tmp/b.jpg", "/tmp/c.jpg")
	err = c.ReplaceReportGroups([]model.ReportGroup{{
		GroupType:         model.GroupSimilarImage,
		Confidence:        0.9,
		StableKey:         "key",
		RecommendedFileID: ids[0],
		Items: []model.ReportItem{
			{FileID: ids[0], Path: "/tmp/a.jpg", Action: model.ActionReview, QualityScore: 0.9},
			{FileID: ids[1], Path: "/tmp/b.jpg", Action: model.ActionReview, QualityScore: 0.89},
			{FileID: ids[2], Path: "/tmp/c.jpg", Action: model.ActionReview, QualityScore: 0.88},
		},
	}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeleteFilesByPaths([]string{"/tmp/a.jpg"}); err != nil {
		t.Fatal(err)
	}
	if err := c.PruneReportGroups(); err != nil {
		t.Fatal(err)
	}
	groups, err := c.LoadReportGroups()
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups=%d want=1", len(groups))
	}
	for _, it := range groups[0].Items {
		if it.Action != model.ActionReview {
			t.Fatalf("file %d action=%s want review", it.FileID, it.Action)
		}
	}
}

// TestUpdateRecommendedRealignsSimilarity covers the case where the user picks a
// different file to keep. Similarity is measured against the recommended file,
// so the stored numbers must follow the new choice instead of keeping the ones
// the scan computed for the previous recommendation.
func TestUpdateRecommendedRealignsSimilarity(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ids := seedGroupFiles(t, c, "/tmp/a.jpg", "/tmp/b.jpg", "/tmp/c.jpg")
	// Chain A≈B≈C: the scan reports B at 0.9 against A and C with no direct edge.
	if err := c.ReplaceReportGroups([]model.ReportGroup{{
		GroupType:         model.GroupSimilarImage,
		Confidence:        0.9,
		StableKey:         "key",
		RecommendedFileID: ids[0],
		Items: []model.ReportItem{
			{FileID: ids[0], Path: "/tmp/a.jpg", Action: model.ActionKeep, Similarity: 1, QualityScore: 0.9},
			{FileID: ids[1], Path: "/tmp/b.jpg", Action: model.ActionCleanup, Similarity: 0.9, QualityScore: 0.8},
			{FileID: ids[2], Path: "/tmp/c.jpg", Action: model.ActionCleanup, Similarity: -1, QualityScore: 0.7},
		},
	}}, 1); err != nil {
		t.Fatal(err)
	}
	groups, err := c.LoadReportGroups()
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups=%d want=1", len(groups))
	}
	groupID := groups[0].GroupID
	if err := c.UpdateRecommended(groupID, ids[2]); err != nil {
		t.Fatal(err)
	}
	groups, err = c.LoadReportGroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range groups[0].Items {
		if it.FileID == ids[2] {
			if it.Similarity != 1 {
				t.Fatalf("new recommendation similarity=%v want=1", it.Similarity)
			}
			continue
		}
		if it.Similarity >= 0 {
			t.Fatalf("file %d kept stale similarity %v, want unknown", it.FileID, it.Similarity)
		}
	}
}

// TestUpdateRecommendedKeepsExactSimilarity: for byte-identical copies the
// similarity does not depend on which one is recommended, and a match that was
// only confirmed by the sampled fingerprint tier keeps its own level instead of
// being rewritten as a full 1.0.
func TestUpdateRecommendedKeepsExactSimilarity(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ids := seedGroupFiles(t, c, "/tmp/a.jpg", "/tmp/b.jpg")
	if err := c.ReplaceReportGroups([]model.ReportGroup{{
		GroupType:         model.GroupExact,
		Confidence:        1,
		StableKey:         "key",
		RecommendedFileID: ids[0],
		Items: []model.ReportItem{
			{FileID: ids[0], Path: "/tmp/a.jpg", Action: model.ActionKeep, Similarity: sampledSimilarity, QualityScore: 0.9},
			{FileID: ids[1], Path: "/tmp/b.jpg", Action: model.ActionCleanup, Similarity: sampledSimilarity, QualityScore: 0.9},
		},
	}}, 1); err != nil {
		t.Fatal(err)
	}
	groups, err := c.LoadReportGroups()
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups=%d want=1", len(groups))
	}
	groupID := groups[0].GroupID
	if err := c.UpdateRecommended(groupID, ids[1]); err != nil {
		t.Fatal(err)
	}
	groups, err = c.LoadReportGroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range groups[0].Items {
		if it.Similarity != sampledSimilarity {
			t.Fatalf("file %d similarity=%v want=%v", it.FileID, it.Similarity, sampledSimilarity)
		}
	}
	if groups[0].RecommendedFileID != ids[1] {
		t.Fatalf("recommended=%d want=%d", groups[0].RecommendedFileID, ids[1])
	}
}

// sampledSimilarity is the level the pipeline uses when byte identity rests on
// the sampled fingerprint rather than a full read.
const sampledSimilarity = 0.99

// TestFrameHashRoundTrip keeps frame slot, timestamp and hash together so video
// frames can be aligned by time after a rescan.
func TestFrameHashRoundTrip(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	file := model.DiscoveredFile{Path: "/tmp/clip.mp4", MediaType: model.MediaVideo, Extension: ".mp4", SizeBytes: 100, MTimeNs: 10}
	id, err := c.UpsertFile(file)
	if err != nil {
		t.Fatal(err)
	}
	// Slots 1 and 4 survived; the gap must be preserved, not compacted.
	frames := []FrameHashRow{
		{Index: 1, TimestampMs: 500, Hash: "aaaaaaaaaaaaaaaa"},
		{Index: 4, TimestampMs: 2000, Hash: "bbbbbbbbbbbbbbbb"},
	}
	if err := c.SaveFrameHashes(id, "video_frame_phash_v3_8", frames); err != nil {
		t.Fatal(err)
	}
	loaded, err := c.LoadFrameHashes(id, "video_frame_phash_v3_8")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 || loaded[0].Index != 1 || loaded[0].TimestampMs != 500 || loaded[1].Index != 4 {
		t.Fatalf("unexpected rows: %+v", loaded)
	}
	// Rewriting replaces the whole set rather than appending duplicates.
	if err := c.SaveFrameHashes(id, "video_frame_phash_v3_8", frames[:1]); err != nil {
		t.Fatal(err)
	}
	loaded, err = c.LoadFrameHashes(id, "video_frame_phash_v3_8")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected one row after replacement, got %+v", loaded)
	}
}

// TestPruneReportGroupsRealignsSimilarity covers the automatic repick after a
// deleted file: the surviving members' similarities were measured against the
// old recommendation, so they must not be presented as if they described the new
// one.
func TestPruneReportGroupsRealignsSimilarity(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ids := seedGroupFiles(t, c, "/tmp/a.jpg", "/tmp/b.jpg", "/tmp/c.jpg")
	if err := c.ReplaceReportGroups([]model.ReportGroup{{
		GroupType:         model.GroupSimilarImage,
		Confidence:        0.9,
		StableKey:         "key",
		RecommendedFileID: ids[0],
		Items: []model.ReportItem{
			{FileID: ids[0], Path: "/tmp/a.jpg", Action: model.ActionKeep, Similarity: 1, QualityScore: 0.9},
			{FileID: ids[1], Path: "/tmp/b.jpg", Action: model.ActionCleanup, Similarity: 0.99, QualityScore: 0.8},
			{FileID: ids[2], Path: "/tmp/c.jpg", Action: model.ActionCleanup, Similarity: 0.7, QualityScore: 0.7},
		},
	}}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeleteFilesByPaths([]string{"/tmp/a.jpg"}); err != nil {
		t.Fatal(err)
	}
	if err := c.PruneReportGroups(); err != nil {
		t.Fatal(err)
	}
	groups, err := c.LoadReportGroups()
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups=%d want=1", len(groups))
	}
	group := groups[0]
	if group.RecommendedFileID != ids[1] {
		t.Fatalf("recommended=%d want=%d", group.RecommendedFileID, ids[1])
	}
	for _, it := range group.Items {
		if it.FileID == ids[1] {
			if it.Similarity != 1 {
				t.Fatalf("new recommendation similarity=%v want=1", it.Similarity)
			}
			continue
		}
		if it.Similarity >= 0 {
			t.Fatalf("file %d kept a similarity measured against the deleted file: %v", it.FileID, it.Similarity)
		}
	}
}

package pipeline

import (
	"errors"
	"fmt"
	"io/fs"
	"syscall"
	"testing"

	"media-dedupe/internal/model"
)

func TestIsFatalIOError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"missing file", fs.ErrNotExist, false},
		{"permission", fs.ErrPermission, false},
		{"corrupt media", errors.New("decode image config: invalid JPEG"), false},
		{"disk io", syscall.EIO, true},
		{"wrapped disk io", fmt.Errorf("hash /x.mp4: %w", syscall.EIO), true},
		{"no space", syscall.ENOSPC, true},
		{"too many files", syscall.EMFILE, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isFatalIOError(tt.err); got != tt.want {
				t.Fatalf("isFatalIOError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestDropFailedGroups(t *testing.T) {
	failures := newFailureTracker()
	failures.mark("/b.jpg")
	groups := []model.ReportGroup{{
		GroupType:         model.GroupExact,
		RecommendedFileID: 1,
		Items: []model.ReportItem{
			{FileID: 1, Path: "/a.jpg", Action: model.ActionKeep},
			{FileID: 2, Path: "/b.jpg", Action: model.ActionCleanup},
			{FileID: 3, Path: "/c.jpg", Action: model.ActionCleanup},
		},
	}}
	got := dropFailedGroups(groups, failures)
	if len(got) != 1 {
		t.Fatalf("groups = %d, want 1", len(got))
	}
	if len(got[0].Items) != 2 {
		t.Fatalf("items = %d, want 2", len(got[0].Items))
	}
	for _, it := range got[0].Items {
		if it.Path == "/b.jpg" {
			t.Fatalf("failed file still present")
		}
	}
}

func TestDropFailedGroupsDropsSingletons(t *testing.T) {
	failures := newFailureTracker()
	failures.mark("/b.jpg")
	groups := []model.ReportGroup{{
		GroupType:         model.GroupExact,
		RecommendedFileID: 1,
		Items: []model.ReportItem{
			{FileID: 1, Path: "/a.jpg", Action: model.ActionKeep},
			{FileID: 2, Path: "/b.jpg", Action: model.ActionCleanup},
		},
	}}
	if got := dropFailedGroups(groups, failures); len(got) != 0 {
		t.Fatalf("groups = %d, want 0", len(got))
	}
}

func TestDropFailedGroupsRepicksRecommendation(t *testing.T) {
	failures := newFailureTracker()
	failures.mark("/a.jpg")
	groups := []model.ReportGroup{{
		GroupType:         model.GroupSimilarImage,
		RecommendedFileID: 1,
		Items: []model.ReportItem{
			{FileID: 1, Path: "/a.jpg", Action: model.ActionKeep, QualityScore: 0.9},
			{FileID: 2, Path: "/b.jpg", Action: model.ActionCleanup, QualityScore: 0.7},
			{FileID: 3, Path: "/c.jpg", Action: model.ActionCleanup, QualityScore: 0.8},
		},
	}}
	got := dropFailedGroups(groups, failures)
	if len(got) != 1 {
		t.Fatalf("groups = %d, want 1", len(got))
	}
	if got[0].RecommendedFileID != 3 {
		t.Fatalf("recommended = %d, want 3", got[0].RecommendedFileID)
	}
	for _, it := range got[0].Items {
		if it.FileID == 3 && it.Action != model.ActionKeep {
			t.Fatalf("recommended action = %s, want keep", it.Action)
		}
	}
}

// A scan without failures must not rewrite scored actions: close quality ties
// are reported as "review required" on purpose.
func TestDropFailedGroupsPreservesReviewMarkers(t *testing.T) {
	groups := []model.ReportGroup{{
		GroupType:         model.GroupSimilarImage,
		RecommendedFileID: 1,
		Items: []model.ReportItem{
			{FileID: 1, Path: "/a.jpg", Action: model.ActionReview, QualityScore: 0.90},
			{FileID: 2, Path: "/b.jpg", Action: model.ActionReview, QualityScore: 0.895},
		},
	}}
	got := dropFailedGroups(groups, newFailureTracker())
	if len(got) != 1 {
		t.Fatalf("groups=%d want=1", len(got))
	}
	for _, it := range got[0].Items {
		if it.Action != model.ActionReview {
			t.Fatalf("file %d action=%s want review", it.FileID, it.Action)
		}
	}
}

// Re-picking a recommendation keeps the review rule of the group type.
func TestDropFailedGroupsRepickKeepsReviewRules(t *testing.T) {
	failures := newFailureTracker()
	failures.mark("/a.jpg")
	groups := []model.ReportGroup{{
		GroupType:         model.GroupSimilarImage,
		RecommendedFileID: 1,
		Items: []model.ReportItem{
			{FileID: 1, Path: "/a.jpg", Action: model.ActionReview, QualityScore: 0.90},
			{FileID: 2, Path: "/b.jpg", Action: model.ActionReview, QualityScore: 0.895},
			{FileID: 3, Path: "/c.jpg", Action: model.ActionReview, QualityScore: 0.89},
		},
	}}
	got := dropFailedGroups(groups, failures)
	if len(got) != 1 {
		t.Fatalf("groups=%d want=1", len(got))
	}
	if got[0].RecommendedFileID != 2 {
		t.Fatalf("recommended=%d want=2", got[0].RecommendedFileID)
	}
	for _, it := range got[0].Items {
		if it.Action != model.ActionReview {
			t.Fatalf("file %d action=%s want review", it.FileID, it.Action)
		}
	}
}

func TestScanHooksReadClassifiesCacheWriteAsFatal(t *testing.T) {
	failures := newFailureTracker()
	canceled := false
	var kinds []string
	h := &scanHooks{
		record:   func(_, _, kind, _ string) { kinds = append(kinds, kind) },
		failures: failures,
		cancel:   func() { canceled = true },
	}
	err := fmt.Errorf("save hash: %w", &cacheWriteError{err: errors.New("database or disk is full (13)")})
	h.read("/a.jpg", "file_hash", err)
	if !canceled {
		t.Fatal("cache write failure must abort the scan")
	}
	if failures.fatalErr() == nil {
		t.Fatal("fatal error not recorded")
	}
	if failures.failed("/a.jpg") {
		t.Fatal("a cache failure must not mark the source file as skipped")
	}
	if len(kinds) != 1 || kinds[0] != model.ErrorFatal {
		t.Fatalf("kinds=%v want [fatal]", kinds)
	}
}

func TestScanHooksReadSkipsUnreadableFile(t *testing.T) {
	failures := newFailureTracker()
	var kinds []string
	h := &scanHooks{
		record:   func(_, _, kind, _ string) { kinds = append(kinds, kind) },
		failures: failures,
	}
	err := &fs.PathError{Op: "open", Path: "/a.jpg", Err: fs.ErrNotExist}
	h.read("/a.jpg", "file_hash", err)
	if failures.fatalErr() != nil {
		t.Fatal("missing file must not abort the scan")
	}
	if !failures.failed("/a.jpg") {
		t.Fatal("missing file should be skipped")
	}
	if len(kinds) != 1 || kinds[0] != model.ErrorSkipped {
		t.Fatalf("kinds=%v want [skipped]", kinds)
	}
}

// TestScanHooksKeepsConfirmedDuplicateOnLaterFailure covers the interaction
// between P0-2 and the failure tracker: representatives now re-enter the
// approximate stages, so a failure there must not delete a group whose byte
// identity the exact pass already settled. The problem is still reported, but as
// a warning instead of "skipped".
func TestScanHooksKeepsConfirmedDuplicateOnLaterFailure(t *testing.T) {
	failures := newFailureTracker()
	var kinds []string
	h := &scanHooks{
		record:   func(_, _, kind, _ string) { kinds = append(kinds, kind) },
		failures: failures,
	}
	h.markConfirmed([]model.ReportGroup{{
		GroupType:         model.GroupExact,
		RecommendedFileID: 1,
		Items: []model.ReportItem{
			{FileID: 1, Path: "/media/a.mp4"},
			{FileID: 2, Path: "/media/a-copy.mp4"},
		},
	}})

	err := errors.New("no usable frames extracted")
	h.file("/media/a.mp4", "video_frame_phash", err)
	if failures.failed("/media/a.mp4") {
		t.Fatal("a confirmed duplicate must not be dropped by a later stage")
	}
	if len(kinds) != 1 || kinds[0] != model.ErrorAux {
		t.Fatalf("kinds=%v want [aux]", kinds)
	}

	// A file without a confirmed twin keeps the old behaviour.
	h.file("/media/other.mp4", "video_frame_phash", err)
	if !failures.failed("/media/other.mp4") {
		t.Fatal("an unconfirmed file that cannot be read should still be skipped")
	}
	if len(kinds) != 2 || kinds[1] != model.ErrorSkipped {
		t.Fatalf("kinds=%v want [aux skipped]", kinds)
	}
}

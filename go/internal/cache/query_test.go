package cache

import (
	"fmt"
	"path/filepath"
	"testing"

	"media-dedupe/internal/model"
)

// summaryFixture builds four groups that differ in status, type, media kind and
// reclaimable size, so filtering, ordering and the totals can be checked against
// hand-written expectations instead of the code under test.
func summaryFixture(t *testing.T) *Cache {
	t.Helper()
	c, err := Open(filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	add := func(path string, size int64, media model.MediaType) int64 {
		id, err := c.UpsertFile(model.DiscoveredFile{Path: path, MediaType: media, SizeBytes: size, MTimeNs: 1})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	item := func(path string, size int64, media model.MediaType, action model.Action) model.ReportItem {
		return model.ReportItem{FileID: add(path, size, media), Path: path, Action: action, SizeBytes: size}
	}

	groups := []model.ReportGroup{
		{
			GroupID: 1, GroupType: model.GroupExact, Confidence: 1, Status: model.GroupPending, RecommendedFileID: 0,
			Items: []model.ReportItem{
				item("/m/one-keep.bin", 1000, model.MediaImage, model.ActionKeep),
				item("/m/one-a.bin", 100, model.MediaImage, model.ActionCleanup),
				item("/m/one-b.bin", 200, model.MediaImage, model.ActionCleanup),
			},
		},
		{
			GroupID: 2, GroupType: model.GroupExact, Confidence: 1, Status: model.GroupPending, RecommendedFileID: 0,
			Items: []model.ReportItem{
				// The recommendation is flagged as a cleanup candidate on purpose:
				// counts and bytes must still exclude it.
				item("/m/two-keep.bin", 5000, model.MediaVideo, model.ActionCleanup),
				item("/m/two-a.bin", 500, model.MediaVideo, model.ActionCleanup),
			},
		},
		{
			GroupID: 3, GroupType: model.GroupSimilarText, Confidence: 0.9, Status: model.GroupIgnored, RecommendedFileID: 0,
			Items: []model.ReportItem{
				item("/m/three-keep.bin", 10, model.MediaText, model.ActionReview),
				item("/m/three-a.bin", 20, model.MediaText, model.ActionReview),
			},
		},
		{
			GroupID: 4, GroupType: model.GroupTextEqual, Confidence: 0.95, Status: model.GroupPending, RecommendedFileID: 0,
			Items: []model.ReportItem{
				item("/m/four-keep.bin", 7, model.MediaImage, model.ActionKeep),
				item("/m/four-a.bin", 70, model.MediaText, model.ActionCleanup),
				item("/m/four-b.bin", 8, model.MediaText, model.ActionReview),
			},
		},
	}
	for i := range groups {
		groups[i].RecommendedFileID = groups[i].Items[0].FileID
	}
	if err := c.ReplaceReportGroups(groups, 0); err != nil {
		t.Fatal(err)
	}
	return c
}

func summaryIDs(rows []GroupSummary) []int64 {
	out := make([]int64, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.GroupID)
	}
	return out
}

func TestLoadGroupSummariesFiltersOrdersAndPages(t *testing.T) {
	c := summaryFixture(t)

	cases := []struct {
		name  string
		query GroupQuery
		want  []int64
	}{
		{"pending default", GroupQuery{}, []int64{1, 2, 4}},
		{"all groups by id", GroupQuery{Status: "all", Sort: "id"}, []int64{1, 2, 3, 4}},
		{"size order", GroupQuery{Status: "all", Sort: "size"}, []int64{2, 1, 4, 3}},
		{"members order", GroupQuery{Status: "all", Sort: "members"}, []int64{1, 4, 2, 3}},
		{"type filter", GroupQuery{Status: "all", GroupType: "text_equal"}, []int64{4}},
		{"search filter", GroupQuery{Status: "all", Search: "two-"}, []int64{2}},
		{"page", GroupQuery{Status: "all", Sort: "id", Limit: 2, Offset: 1}, []int64{2, 3}},
		{"page past end", GroupQuery{Status: "all", Sort: "id", Limit: 2, Offset: 9}, []int64{}},
	}
	for _, tc := range cases {
		rows, err := c.LoadGroupSummaries(tc.query)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got := summaryIDs(rows)
		if fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestLoadGroupSummariesCountsAndReclaim(t *testing.T) {
	c := summaryFixture(t)
	rows, err := c.LoadGroupSummaries(GroupQuery{Status: "all", Sort: "id"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("rows: %d", len(rows))
	}
	first, second, third, fourth := rows[0], rows[1], rows[2], rows[3]
	if first.MemberCount != 3 || first.CleanupCount != 2 || first.ReclaimableBytes != 300 {
		t.Fatalf("group 1: %+v", first)
	}
	// The recommended file is a cleanup candidate in the fixture; it must not be
	// counted or reclaimed.
	if second.MemberCount != 2 || second.CleanupCount != 1 || second.ReclaimableBytes != 500 {
		t.Fatalf("group 2: %+v", second)
	}
	if third.CleanupCount != 0 || third.ReclaimableBytes != 0 || third.Status != string(model.GroupIgnored) {
		t.Fatalf("group 3: %+v", third)
	}
	if fourth.CleanupCount != 1 || fourth.ReclaimableBytes != 70 {
		t.Fatalf("group 4: %+v", fourth)
	}
	if first.Images != 3 || first.Videos != 0 || first.Texts != 0 {
		t.Fatalf("group 1 media: %+v", first)
	}
	if second.Videos != 2 {
		t.Fatalf("group 2 media: %+v", second)
	}
	if fourth.Images != 1 || fourth.Texts != 2 {
		t.Fatalf("group 4 media: %+v", fourth)
	}
}

func TestLoadGroupTotalsMatchesPendingGroups(t *testing.T) {
	c := summaryFixture(t)
	totals, err := c.LoadGroupTotals()
	if err != nil {
		t.Fatal(err)
	}
	want := GroupTotals{Groups: 4, ExactGroups: 2, SimilarGroups: 2, PendingGroups: 3, ReclaimableBytes: 870}
	if totals != want {
		t.Fatalf("totals: got %+v want %+v", totals, want)
	}
	// Reclaimable bytes only count pending groups, matching ComputeSummary.
	rows, err := c.LoadGroupSummaries(GroupQuery{Status: "all"})
	if err != nil {
		t.Fatal(err)
	}
	var pending int64
	for _, row := range rows {
		if row.Status == string(model.GroupPending) {
			pending += row.ReclaimableBytes
		}
	}
	if pending != totals.ReclaimableBytes {
		t.Fatalf("pending reclaim mismatch: %d vs %d", pending, totals.ReclaimableBytes)
	}
}

func TestLoadGroupReadsOneGroup(t *testing.T) {
	c := summaryFixture(t)
	group, found, err := c.LoadGroup(2)
	if err != nil || !found {
		t.Fatalf("load: %v %v", found, err)
	}
	if group.GroupID != 2 || len(group.Items) != 2 || group.GroupType != model.GroupExact {
		t.Fatalf("group: %+v", group)
	}
	if _, found, err := c.LoadGroup(999); err != nil || found {
		t.Fatalf("missing group reported found=%v err=%v", found, err)
	}
}

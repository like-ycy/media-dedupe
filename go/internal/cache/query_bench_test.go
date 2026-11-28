package cache

import (
	"fmt"
	"path/filepath"
	"testing"

	"media-dedupe/internal/model"
)

// 5000 groups x 3 members: a size where the difference between "load everything
// and filter in Go" and "ask SQL for one page" is visible. Roughly 3.6s vs 22ms
// for the list and 3.6s vs 1.4ms for one group on an M1 Pro; the point is the
// shape (O(groups) vs O(page)), not the absolute numbers.
const benchGroups = 5000

func benchCache(tb testing.TB) *Cache {
	tb.Helper()
	c, err := Open(filepath.Join(tb.TempDir(), "bench.sqlite"))
	if err != nil {
		tb.Fatal(err)
	}
	var all []model.ReportGroup
	for g := 1; g <= benchGroups; g++ {
		var items []model.ReportItem
		for i := 0; i < 3; i++ {
			path := fmt.Sprintf("/m/%d-%d.bin", g, i)
			id, err := c.UpsertFile(model.DiscoveredFile{Path: path, MediaType: model.MediaImage, SizeBytes: int64(g * 10), MTimeNs: 1})
			if err != nil {
				tb.Fatal(err)
			}
			action := model.ActionCleanup
			if i == 0 {
				action = model.ActionKeep
			}
			items = append(items, model.ReportItem{FileID: id, Path: path, Action: action, SizeBytes: int64(g * 10)})
		}
		all = append(all, model.ReportGroup{
			GroupID: int64(g), GroupType: model.GroupExact, Confidence: float64(g%100) / 100,
			RecommendedFileID: items[0].FileID, Items: items,
		})
	}
	if err := c.ReplaceReportGroups(all, 0); err != nil {
		tb.Fatal(err)
	}
	return c
}

// BenchmarkGroupSummaries is the results list: one page, one query.
func BenchmarkGroupSummaries(b *testing.B) {
	c := benchCache(b)
	defer c.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.LoadGroupSummaries(GroupQuery{Limit: 500}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkLoadGroup is the detail page: two queries, independent of group count.
func BenchmarkLoadGroup(b *testing.B) {
	c := benchCache(b)
	defer c.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, found, err := c.LoadGroup(benchGroups / 2); err != nil || !found {
			b.Fatalf("load: %v %v", found, err)
		}
	}
}

// BenchmarkLoadReportGroups is the full load that export still needs, kept as the
// baseline the two above are measured against.
func BenchmarkLoadReportGroups(b *testing.B) {
	c := benchCache(b)
	defer c.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.LoadReportGroups(); err != nil {
			b.Fatal(err)
		}
	}
}

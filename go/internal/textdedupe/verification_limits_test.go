package textdedupe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"media-dedupe/internal/model"
)

// A group of near-identical editions can be arbitrarily large. Verifying every
// pair is O(k²) file reads, so the pass stops at a bound and every member says
// it was not fully covered — a silent stop would look like "verified, no
// containment found".
func TestVerifyGroupsBoundsPairsPerGroup(t *testing.T) {
	dir := t.TempDir()
	const members = 30 // 435 pairs, above the 400 cap
	var facts []Fact
	var items []model.ReportItem
	for i := 0; i < members; i++ {
		content := body(int64(100+i), 400)
		path := filepath.Join(dir, "edition.txt"+string(rune('a'+i)))
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		f := fact(int64(i+1), path, content)
		facts = append(facts, f)
		items = append(items, model.ReportItem{FileID: f.FileID, Path: path, Action: model.ActionReview})
	}
	groups := []model.ReportGroup{{
		GroupID: 1, GroupType: model.GroupSimilarText, Confidence: .95,
		RecommendedFileID: items[0].FileID, Items: items,
	}}

	if errs := VerifyGroups(context.Background(), groups, facts, nil, nil); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	for _, it := range groups[0].Items {
		joined := strings.Join(it.Reasons, " ")
		if !strings.Contains(joined, "仅复核了前 400 对全文") {
			t.Fatalf("item %s does not disclose the bound: %v", it.Path, it.Reasons)
		}
	}
}

// The verification cache only stores evidence. A failing cache must not drop the
// rest of the scan's evidence on the floor: it is reported once and the pairs are
// verified in memory instead.
type brokenVerificationCache struct {
	loads int
	saves int
}

func (c *brokenVerificationCache) LoadTextVerification(string, string) (model.TextVerification, bool, error) {
	c.loads++
	return model.TextVerification{}, false, errors.New("database is locked")
}

func (c *brokenVerificationCache) SaveTextVerification(string, string, model.TextVerification) error {
	c.saves++
	return errors.New("database is locked")
}

func TestVerifyGroupsSurvivesCacheFailure(t *testing.T) {
	dir := t.TempDir()
	short := body(7, 500)
	long := "前言" + short + "后记"
	var facts []Fact
	var items []model.ReportItem
	for i, content := range []string{short, long} {
		path := filepath.Join(dir, "pair"+string(rune('a'+i))+".txt")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		f := fact(int64(i+1), path, content)
		facts = append(facts, f)
		items = append(items, model.ReportItem{FileID: f.FileID, Path: path, Action: model.ActionReview})
	}
	groups := []model.ReportGroup{{
		GroupID: 1, GroupType: model.GroupTextContained, Confidence: .95,
		RecommendedFileID: items[0].FileID, Items: items,
	}}

	cache := &brokenVerificationCache{}
	errs := VerifyGroups(context.Background(), groups, facts, cache, nil)
	if len(errs) != 1 {
		t.Fatalf("cache failure should be reported once, got %v", errs)
	}
	if errs[0].Stage != "text_cache" {
		t.Fatalf("stage=%s", errs[0].Stage)
	}
	if cache.loads != 1 {
		t.Fatalf("kept querying a broken cache: %d loads", cache.loads)
	}
	// The pair was still verified in memory, so the containment evidence exists.
	joined := strings.Join(groups[0].Items[0].Reasons, " ")
	if !strings.Contains(joined, "全文复核确认") {
		t.Fatalf("evidence lost with the cache: %v", groups[0].Items[0].Reasons)
	}
}

// Reasons are stored per file. A large group must not write one line per member
// into every row, but the file still has to say that relations were summarised.
func TestGroupEvidenceIsBounded(t *testing.T) {
	dir := t.TempDir()
	base := []rune(body(900, 600))
	var facts []Fact
	for i := 0; i < 12; i++ {
		// Each edition differs in a few characters: similar, not equal.
		variant := append([]rune(nil), base...)
		for k := 0; k < 3; k++ {
			variant[i*7+k] = '甲'
		}
		content := string(variant)
		path := filepath.Join(dir, "book"+string(rune('a'+i))+".txt")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		facts = append(facts, fact(int64(i+1), path, content))
	}
	groups := Groups(facts, .92)
	if len(groups) == 0 {
		t.Fatal("no group formed; fixture no longer represents a large component")
	}
	g := groups[0]
	if len(g.Items) < 3 {
		t.Fatalf("expected a merged component, got %d members", len(g.Items))
	}
	for _, it := range g.Items {
		joined := strings.Join(it.Reasons, " ")
		if relations := strings.Count(joined, "与「"); relations > maxEvidencePerMember {
			t.Fatalf("%s kept %d relation lines: %v", it.Path, relations, it.Reasons)
		}
		if len(g.Items) > maxEvidencePerMember+1 && !strings.Contains(joined, "未逐条列出") {
			t.Fatalf("%s does not mention the summarised relations: %v", it.Path, it.Reasons)
		}
	}
}

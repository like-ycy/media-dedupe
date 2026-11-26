package report

import (
	"encoding/json"
	"strings"
	"testing"

	"media-dedupe/internal/model"
)

func TestReviewUnknownSimilarityExports(t *testing.T) {
	groups := []model.ReportGroup{{GroupType: model.GroupSimilarImage, RecommendedFileID: 1, Items: []model.ReportItem{{FileID: 1, Path: "a.jpg", Similarity: 1}, {FileID: 2, Path: "b.jpg", Similarity: -1}}}}
	encoded, err := RenderJSON(groups, nil, model.ScanMeta{})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Groups []struct {
			Items []struct {
				Similarity *float64 `json:"similarity"`
			} `json:"items"`
		} `json:"groups"`
	}
	if err := json.Unmarshal([]byte(encoded), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Groups[0].Items[1].Similarity != nil || *doc.Groups[0].Items[0].Similarity != 1 {
		t.Fatal(encoded)
	}
	text := RenderText(groups, nil, model.ScanMeta{})
	html := RenderHTML(groups, nil, model.ScanMeta{}, "")
	for _, s := range []string{text, html} {
		if !strings.Contains(s, "sim=—") || strings.Contains(s, "sim=-1") {
			t.Fatal(s)
		}
	}
}

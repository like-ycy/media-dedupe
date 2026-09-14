package match

import (
	"testing"

	"media-dedupe/internal/model"
)

func TestConnectedComponents(t *testing.T) {
	edges := []model.SimilarityEdge{
		{LeftFileID: 1, RightFileID: 2},
		{LeftFileID: 2, RightFileID: 3},
		{LeftFileID: 10, RightFileID: 11},
	}
	comps := ConnectedComponents(edges)
	if len(comps) != 2 {
		t.Fatalf("expected 2 components, got %d: %v", len(comps), comps)
	}
	if len(comps[0]) != 3 || len(comps[1]) != 2 {
		t.Fatalf("unexpected sizes: %v", comps)
	}
}

func TestVideoHashSimilarityIdentical(t *testing.T) {
	// 16 hex chars = 64 bits
	a := []string{"0000000000000000", "ffffffffffffffff"}
	b := []string{"0000000000000000", "ffffffffffffffff"}
	sim := VideoHashSimilarity(a, b)
	if sim != 1.0 {
		t.Fatalf("expected 1.0, got %v", sim)
	}
}

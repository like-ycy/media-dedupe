package candidate

import "testing"

func TestImageBucketKey(t *testing.T) {
	w, h := ImageBucketKey(100, 50)
	if w%8 != 0 || h%8 != 0 {
		t.Fatalf("expected quantized, got %d %d", w, h)
	}
}

func TestMaxHammingDistance(t *testing.T) {
	// threshold 0.8 on 64 bits => d <= 12.8 => 12
	d := MaxHammingDistance(0.80, 64)
	if d != 12 {
		t.Fatalf("expected 12, got %d", d)
	}
}

func TestExpandImageCandidates(t *testing.T) {
	items := []ImageCandidateItem{
		{Width: 100, Height: 100},
		{Width: 104, Height: 100}, // neighboring bucket
		{Width: 2000, Height: 1000},
	}
	pairs := ExpandImageCandidates(items)
	found := false
	for _, p := range pairs {
		if p == [2]int{0, 1} {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected pair 0-1, got %v", pairs)
	}
}

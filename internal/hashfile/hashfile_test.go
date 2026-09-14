package hashfile

import "testing"

func TestHammingDistance(t *testing.T) {
	d, err := HammingDistance("ffff", "0000")
	if err != nil {
		t.Fatal(err)
	}
	if d != 16 {
		t.Fatalf("expected 16, got %d", d)
	}
	d, err = HammingDistance("abc", "abc")
	if err != nil || d != 0 {
		t.Fatalf("expected 0, got %d err=%v", d, err)
	}
}

func TestSimilarity(t *testing.T) {
	s, err := Similarity("ff", "ff")
	if err != nil || s != 1.0 {
		t.Fatalf("expected 1.0, got %v err=%v", s, err)
	}
}

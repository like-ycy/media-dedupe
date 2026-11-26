package hashfile

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

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

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSampleHashFileDistinguishesMiddleContent(t *testing.T) {
	base := bytes.Repeat([]byte{0x7f}, 6<<20)
	// Same size, same head and tail, different middle: the sampled fingerprint
	// must still tell them apart.
	other := append([]byte(nil), base...)
	copy(other[len(other)/2:], bytes.Repeat([]byte{0x01}, 4096))

	left := writeTemp(t, "left.bin", base)
	right := writeTemp(t, "right.bin", base)
	changed := writeTemp(t, "changed.bin", other)

	leftHash, err := SampleHashFile(left, int64(len(base)), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	rightHash, err := SampleHashFile(right, int64(len(base)), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	changedHash, err := SampleHashFile(changed, int64(len(base)), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if leftHash != rightHash {
		t.Fatalf("identical files produced different fingerprints")
	}
	if leftHash == changedHash {
		t.Fatalf("fingerprint ignored a changed middle segment")
	}
}

func TestSampleHashFileRejectsSizeChange(t *testing.T) {
	path := writeTemp(t, "small.bin", bytes.Repeat([]byte{0x02}, 1024))
	if _, err := SampleHashFile(path, 2048, 1<<20); err == nil {
		t.Fatal("expected an error when the recorded size no longer matches")
	}
}

func TestSampleHashFileFullReadForSmallFiles(t *testing.T) {
	data := []byte("tiny video placeholder content")
	path := writeTemp(t, "tiny.mp4", data)
	sampled, err := SampleHashFile(path, int64(len(data)), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	again, err := SampleHashFile(path, int64(len(data)), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if sampled != again {
		t.Fatal("fingerprint is not stable for small files")
	}
	full, err := SHA256File(path)
	if err != nil {
		t.Fatal(err)
	}
	if sampled == full {
		t.Fatal("sampled fingerprint must be domain-separated from the full hash")
	}
}

package textdedupe

import (
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func referenceFeatures(content string) []uint64 {
	runes := []rune(content)
	if len(runes) < featureNgram {
		return nil
	}
	hashes := make([]uint64, 0, len(runes)-featureNgram+1)
	for i := 0; i+featureNgram <= len(runes); i++ {
		hashes = append(hashes, mix64(fnv64Runes(runes[i:i+featureNgram])))
	}
	sort.Slice(hashes, func(i, j int) bool { return hashes[i] < hashes[j] })
	unique := make([]uint64, 0, min(len(hashes), maxFeatures))
	for _, value := range hashes {
		if len(unique) == 0 || unique[len(unique)-1] != value {
			unique = append(unique, value)
			if len(unique) == maxFeatures {
				break
			}
		}
	}
	return unique
}

func randomText(n int) string {
	rng := rand.New(rand.NewSource(41))
	var b strings.Builder
	for range n {
		b.WriteRune(rune(0x4e00 + rng.Intn(20000)))
	}
	return b.String()
}

func TestFeaturesEqualFullSort(t *testing.T) {
	cases := []string{"", "abc", "abcde", "aaaaaaa", strings.Repeat("chapter repeated paragraph ", 10000), randomText(300000), string([]byte{'a', 0xff, 'b', 'c', 'd', 'e'})}
	for i, content := range cases {
		if got, want := features(content), referenceFeatures(content); !reflect.DeepEqual(got, want) {
			t.Fatalf("case %d: feature mismatch got=%d want=%d", i, len(got), len(want))
		}
	}
}

func BenchmarkFeatures(b *testing.B) {
	content := randomText(1000000)
	for _, algorithm := range []struct {
		name string
		fn   func(string) []uint64
	}{{"full_sort", referenceFeatures}, {"bounded_heap", features}} {
		b.Run(algorithm.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(content)))
			for b.Loop() {
				_ = algorithm.fn(content)
			}
		})
	}
}

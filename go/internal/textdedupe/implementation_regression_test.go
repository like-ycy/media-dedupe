package textdedupe

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"math/rand"
	"media-dedupe/internal/model"
	"media-dedupe/internal/novelname"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestLegacyGB18030MappingsAndInvalidSequences(t *testing.T) {
	cases := map[string]string{string([]byte{0xa1, 0x40}): "\ue4c6", string([]byte{0xa3, 0xa0}): "\ue5e5", string([]byte{0xa1, 0xa1}): "\u3000", string([]byte{0x84, 0x31, 0xa4, 0x37}): "\ufffd"}
	for input, want := range cases {
		got, err := decodeText([]byte(input))
		if err != nil || got != want {
			t.Fatalf("%x => %q %v, want %q", input, got, err, want)
		}
	}
	for _, input := range [][]byte{{0x80}, {0xff}, {0x81}, {0x81, 0x7f}, {0x81, 0x30, 0x81}, {0x81, 0x3a, 0x81, 0x30}, {0x84, 0x31, 0xa5, 0x30}, {0xfe, 0x39, 0xfe, 0x39}} {
		if got, err := decodeText(input); err == nil {
			t.Fatalf("invalid %x accepted as %q", input, got)
		}
	}
}

func TestLegacyGB18030AllReferenceMappings(t *testing.T) {
	root := os.Getenv("TXT_REFERENCE_ROOT")
	if root == "" {
		t.Skip("external codec reference fixtures")
	}
	raw, err := os.ReadFile(filepath.Join(root, "gb-pairs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pairs [][2]int
	if err := json.Unmarshal(raw, &pairs); err != nil {
		t.Fatal(err)
	}
	for _, p := range pairs {
		got, err := decodeGB18030([]byte{byte(p[0] >> 8), byte(p[0])})
		if err != nil || got != string(rune(p[1])) {
			t.Fatalf("two-byte %x => %q, want %U, err %v", p[0], got, p[1], err)
		}
	}
	raw, err = os.ReadFile(filepath.Join(root, "gb-four.bin"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(raw); i += 8 {
		got, err := decodeText(raw[i : i+4])
		want := rune(binary.LittleEndian.Uint32(raw[i+4 : i+8]))
		if err != nil || got != string(want) {
			t.Fatalf("four-byte %x => %q, want %U, err %v", raw[i:i+4], got, want, err)
		}
	}
}

func TestBoundedIntersectionPreservesClassifier(t *testing.T) {
	rng := rand.New(rand.NewSource(29))
	for n := 0; n < 1800; n++ {
		sample := func() []uint64 {
			values := map[uint64]bool{}
			size := rng.Intn(1100)
			for len(values) < size {
				values[uint64(rng.Intn(2400))] = true
			}
			out := make([]uint64, 0, len(values))
			for v := range values {
				out = append(out, v)
			}
			sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
			if len(out) > 1024 {
				out = out[:1024]
			}
			return out
		}
		a := Fact{TextFingerprint: model.TextFingerprint{Length: rng.Intn(20000), Features: sample()}, Names: []novelname.Name{novelname.Parse("相同名称的小说.txt")}}
		b := Fact{TextFingerprint: model.TextFingerprint{Length: rng.Intn(20000), Features: sample()}, Names: []novelname.Name{novelname.Parse("不同名称的作品.txt")}}
		if n%2 == 0 {
			b.Names = a.Names
		}
		if n%4 == 0 {
			b.Length = a.Length
		}
		if n%6 == 0 {
			b.Features = append([]uint64(nil), a.Features...)
		}
		left, right, common := baselineSharedSample(a.Features, b.Features)
		edge := float64(2*common) / float64(max(1, left+right))
		for _, threshold := range []float64{.01, .1, .5, .9, .92, .97, 1, edge, math.Nextafter(edge, 0), math.Nextafter(edge, 1)} {
			got, gok := compare(a, b, threshold)
			want, wok := baselineCompare(a, b, threshold)
			if gok != wok || got != want {
				t.Fatalf("case %d threshold %.18f got %+v %v want %+v %v", n, threshold, got, gok, want, wok)
			}
		}
	}
}

type statusCache struct {
	value model.TextFingerprint
	hit   bool
	saves int
}

func (c *statusCache) LoadTextFact(id, size int64) (model.TextFingerprint, bool, error) {
	return c.value, c.hit, nil
}
func (c *statusCache) SaveTextFact(id, size int64, f model.TextFingerprint) error {
	c.value = f
	c.hit = true
	c.saves++
	return nil
}
func TestDecodeFailureRetainsNameAndCachesOnlyDecoding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "同一本小说.txt")
	if err := os.WriteFile(path, []byte{0xff}, 0600); err != nil {
		t.Fatal(err)
	}
	files := []model.DiscoveredFile{{Path: path, MediaType: model.MediaText, SizeBytes: 1}}
	c := &statusCache{}
	facts, errs := FactsCached(files, []int64{1}, []bool{false}, 1, c)
	if len(facts) != 1 || len(errs) != 1 || errs[0].Stage != "text_decode" || facts[0].DecodeError == "" || c.saves != 1 {
		t.Fatalf("facts=%+v errors=%+v saves=%d", facts, errs, c.saves)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	warm, werrs := FactsCached(files, []int64{1}, []bool{true}, 1, c)
	if !reflect.DeepEqual(facts, warm) || len(werrs) != 1 || werrs[0].Stage != "text_decode" || c.saves != 1 {
		t.Fatal("failed decoding not reused")
	}
	c = &statusCache{}
	missing, merrs := FactsCached(files, []int64{1}, nil, 1, c)
	if len(missing) != 0 || len(merrs) != 1 || merrs[0].Stage != "text" || c.saves != 0 {
		t.Fatal("cached transient read failure")
	}
}

func TestFullTextVerificationEvidenceAndChangedFile(t *testing.T) {
	dir := t.TempDir()
	short := body(41, 3000)
	long := body(42, 1000) + short + body(43, 1000)
	names := []string{"短篇.txt", "长篇.txt"}
	contents := []string{short, long}
	var facts []Fact
	for i, name := range names {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(contents[i]), 0600)
		f := fact(int64(i+1), p, contents[i])
		facts = append(facts, f)
	}
	groups := Groups(facts, .92)
	if len(groups) != 1 {
		t.Fatalf("groups=%+v", groups)
	}
	errs := VerifyGroups(context.Background(), groups, facts, nil, nil)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if !strings.Contains(strings.Join(groups[0].Items[0].Reasons, " "), "第 1001–4000 字符") {
		t.Fatal(groups[0].Items[0].Reasons)
	}
	for _, it := range groups[0].Items {
		if it.Action != model.ActionReview {
			t.Fatal("verification escalated cleanup")
		}
	}
	os.WriteFile(facts[0].Path, []byte("changed"), 0600)
	if len(VerifyGroups(context.Background(), Groups(facts, .92), facts, nil, nil)) != 1 {
		t.Fatal("changed input verified")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if len(VerifyGroups(canceled, Groups(facts, .92), facts, nil, nil)) != 0 {
		t.Fatal("cancel read files")
	}
	a, b := &model.ReportItem{Path: "a"}, &model.ReportItem{Path: "b"}
	appendVerification(a, b, verifyTextPair(&verificationText{content: "a b c", compact: "abc"}, &verificationText{content: "XabcY", compact: "XabcY"}))
	if !strings.Contains(strings.Join(a.Reasons, " "), "忽略空白") {
		t.Fatal("missing whitespace qualification")
	}
}

func baselineCompare(a, b Fact, threshold float64) (relation, bool) {
	nameScore := 0.0
	for _, an := range a.Names {
		for _, bn := range b.Names {
			nameScore = math.Max(nameScore, novelname.Match(an, bn))
		}
	}
	left, right, common := baselineSharedSample(a.Features, b.Features)
	if a.Length >= 200 && b.Length >= 200 && left >= 32 && right >= 32 && min(len(a.Features), len(b.Features)) >= 64 {
		lc, rc := float64(common)/float64(left), float64(common)/float64(right)
		dice := 2 * float64(common) / float64(left+right)
		if dice >= threshold {
			return relation{kind: model.GroupSimilarText, score: dice, leftCoverage: lc, rightCoverage: rc}, true
		}
		if common >= 32 && ((a.Length < b.Length && lc >= math.Max(.97, threshold)) || (b.Length < a.Length && rc >= math.Max(.97, threshold))) {
			return relation{kind: model.GroupTextContained, score: dice, leftCoverage: lc, rightCoverage: rc}, true
		}
		if nameScore == 1 && common >= 32 && math.Min(lc, rc) >= .10 {
			return relation{kind: model.GroupTextContained, score: dice, leftCoverage: lc, rightCoverage: rc, partial: true}, true
		}
	}
	if nameScore >= .9 {
		return relation{kind: model.GroupTextName, score: NoDirectSimilarity}, true
	}
	return relation{}, false
}

// Compare bottom-k sketches under a shared cutoff. Dividing the intersection
// by each sketch's original size would hide a short text contained in a novel.
func baselineSharedSample(a, b []uint64) (int, int, int) {
	cutoff := uint64(math.MaxUint64)
	if len(a) == maxFeatures {
		cutoff = min(cutoff, a[len(a)-1])
	}
	if len(b) == maxFeatures {
		cutoff = min(cutoff, b[len(b)-1])
	}
	na := sort.Search(len(a), func(i int) bool { return a[i] > cutoff })
	nb := sort.Search(len(b), func(i int) bool { return b[i] > cutoff })
	i, j, common := 0, 0, 0
	for i < na && j < nb {
		if a[i] < b[j] {
			i++
		} else if a[i] > b[j] {
			j++
		} else {
			common++
			i++
			j++
		}
	}
	return na, nb, common
}

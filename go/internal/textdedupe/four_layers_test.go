package textdedupe

import (
	"context"
	"encoding/binary"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"media-dedupe/internal/model"
	"media-dedupe/internal/novelname"
)

func fact(id int64, name, body string) Fact {
	return Fact{FileID: id, Path: name, Size: int64(len(body)), Names: []novelname.Name{novelname.Parse(name)}, TextFingerprint: model.TextFingerprint{Digest: textDigest(body), Length: utf8.RuneCountInString(body), Features: features(body)}}
}
func body(seed int64, n int) string {
	r := rand.New(rand.NewSource(seed))
	out := make([]rune, n)
	for i := range out {
		out[i] = rune(0x4e00 + r.Intn(1000))
	}
	return string(out)
}

func TestConservativeTextEquality(t *testing.T) {
	want := "甲乙丙 ABC\n保留  两个空格\n"
	gb, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(want))
	if err != nil {
		t.Fatal(err)
	}
	inputs := [][]byte{[]byte(want), append([]byte{0xef, 0xbb, 0xbf}, []byte(want)...), []byte(strings.ReplaceAll(want, "\n", "\r\n")), gb}
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		b := make([]byte, 2)
		order.PutUint16(b, 0xfeff)
		for _, u := range utf16.Encode([]rune(want)) {
			v := make([]byte, 2)
			order.PutUint16(v, u)
			b = append(b, v...)
		}
		inputs = append(inputs, b)
	}
	for i, data := range inputs {
		text, err := decodeText(data)
		if err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		if normalize(text) != want {
			t.Fatalf("%d: lossy decoding", i)
		}
		if textDigest(normalize(text)) != textDigest(want) {
			t.Fatal("different digest")
		}
	}
	for _, other := range []string{strings.ReplaceAll(want, "ABC", "abc"), strings.ReplaceAll(want, "  ", " "), want + "广告", want + "。"} {
		if textDigest(other) == textDigest(want) {
			t.Fatal("lossy equality")
		}
	}
	for _, data := range [][]byte{{0xff}, {0xff, 0xfe, 0x00}, {0xff, 0xfe, 0x00, 0xd8}, {0xef, 0xbb, 0xbf, 0xff}} {
		if _, err := decodeText(data); err == nil {
			t.Fatalf("accepted invalid encoding %x", data)
		}
	}
	groups := Groups([]Fact{fact(1, "甲.txt", "短文"), fact(2, "乙.txt", "短文")}, .92)
	if len(groups) != 1 || groups[0].GroupType != model.GroupTextEqual {
		t.Fatalf("short equality: %+v", groups)
	}
}

func TestContentAndNameLayers(t *testing.T) {
	whole := body(1, 24000)
	short := string([]rune(whole)[4000:9000])
	near := whole + body(2, 300)
	cases := []struct {
		name string
		a, b Fact
		kind model.GroupType
	}{
		{"subset different names", fact(1, "短版.txt", short), fact(2, "长版.txt", whole), model.GroupTextContained},
		{"near different names", fact(1, "甲.txt", whole), fact(2, "乙.txt", near), model.GroupSimilarText},
		{"name only", fact(1, "妻心如刀.txt", body(3, 3000)), fact(2, "妻心如刀www.txt2016.com.txt", body(4, 3000)), model.GroupTextName},
		{"complementary chapters", fact(1, "绿傲都市1-70.txt", body(5, 3000)), fact(2, "绿傲都市71-76.txt", body(6, 3000)), model.GroupTextName},
		{"prequel", fact(1, "龙战士传说.txt", body(7, 3000)), fact(2, "龙战士传说——前传.txt", body(8, 3000)), model.GroupTextName},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gs := Groups([]Fact{tc.a, tc.b}, .92)
			if len(gs) != 1 || gs[0].GroupType != tc.kind {
				t.Fatalf("want %s: %+v", tc.kind, gs)
			}
			for _, it := range gs[0].Items {
				if it.Action != model.ActionReview {
					t.Fatalf("unconfirmed cleanup %+v", it)
				}
			}
		})
	}
	if got := Groups([]Fact{fact(1, "不同作品甲.txt", body(9, 5000)), fact(2, "无关作品乙.txt", body(10, 5000))}, .92); len(got) != 0 {
		t.Fatalf("unrelated grouped: %+v", got)
	}
}

func TestMixedGroupKeepsEqualEvidenceWithoutCleaningVersions(t *testing.T) {
	a := fact(1, "同一作品.txt", body(1, 9000))
	b := fact(2, "改名版本.txt", body(1, 9000))
	c := fact(3, "同一作品（前传）.txt", body(2, 6000))
	gs := Groups([]Fact{a, b, c}, .92)
	if len(gs) != 1 || len(gs[0].Items) != 3 {
		t.Fatalf("lost related/equal members %+v", gs)
	}
	for _, it := range gs[0].Items {
		if it.FileID == 2 && it.Action != model.ActionCleanup {
			t.Fatal("lost confirmed equality")
		}
		if it.FileID != 2 && it.Action != model.ActionReview {
			t.Fatal("inferred deletion from title")
		}
	}
}

func TestLowDiversityAndCanceledWork(t *testing.T) {
	a, b := fact(1, "甲.txt", strings.Repeat("固定样板文字", 2000)), fact(2, "乙.txt", strings.Repeat("固定样板文字", 1000))
	if len(Groups([]Fact{a, b}, .92)) != 0 {
		t.Fatal("repetition is not coverage evidence")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if len(GroupsContext(ctx, []Fact{fact(1, "同一个作品.txt", body(1, 4000)), fact(2, "同一个作品.txt", body(2, 4000))}, .92)) != 0 {
		t.Fatal("canceled grouping")
	}
	path := filepath.Join(t.TempDir(), "小说.txt")
	if err := os.WriteFile(path, []byte("小说正文"), 0600); err != nil {
		t.Fatal(err)
	}
	if facts, _ := FactsCachedContext(ctx, []model.DiscoveredFile{{Path: path, MediaType: model.MediaText}}, []int64{1}, nil, 1, nil); len(facts) != 0 {
		t.Fatal("canceled facts")
	}
}

func TestInventoryNamesOnlyScale(t *testing.T) {
	data, err := os.ReadFile("../../testdata/txt_file_names.txt")
	if err != nil {
		t.Skip("local inventory not present")
	}
	var fs []Fact
	for i, line := range strings.Split(string(data), "\n") {
		name := strings.TrimSpace(line)
		if name != "" {
			fs = append(fs, fact(int64(i+1), name, ""))
		}
	}
	gs := Groups(fs, .92)
	for _, g := range gs {
		for _, it := range g.Items {
			if it.Action != model.ActionReview {
				t.Fatal("names-only inventory produced cleanup")
			}
		}
	}
	t.Logf("inventory: %d names, %d review groups", len(fs), len(gs))
}

func TestPartialOverlapAndSimilarityChainStayReview(t *testing.T) {
	shared := []rune(body(12, 24000))
	a, b := fact(1, "重叠的书名1-100.txt", string(shared[:14000])), fact(2, "重叠的书名80-150.txt", string(shared[10000:]))
	relation, ok := compare(a, b, .92)
	if !ok || !relation.partial {
		t.Fatalf("partial overlap lost: %+v %v", relation, ok)
	}
	for _, g := range Groups([]Fact{a, b}, .92) {
		for _, i := range g.Items {
			if i.IsCleanup() {
				t.Fatal("partial overlap auto-cleaned")
			}
		}
	}
	original := []rune(body(21, 20000))
	changed := []rune(body(22, 20000))
	one := fact(1, "甲.txt", string(original))
	two := fact(2, "乙.txt", string(changed[:700])+string(original[700:]))
	three := fact(3, "丙.txt", string(changed[:1400])+string(original[1400:]))
	gs := Groups([]Fact{one, two, three}, .95)
	if len(gs) != 1 || len(gs[0].Items) != 3 {
		t.Fatalf("expected connected review graph, got %+v", gs)
	}
	if gs[0].Confidence != 0 {
		t.Fatalf("chain confidence is not conservative: %+v", gs[0])
	}
	for _, i := range gs[0].Items {
		if i.IsCleanup() {
			t.Fatal("chain auto-cleaned")
		}
	}
}

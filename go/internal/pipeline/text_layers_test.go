package pipeline

import (
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"media-dedupe/internal/model"
)

func novelBody(seed int64, n int) string {
	r := rand.New(rand.NewSource(seed))
	out := make([]rune, n)
	for i := range out {
		out[i] = rune(0x4e00 + r.Intn(1000))
	}
	return string(out)
}

func TestTXTScanLayersAndCache(t *testing.T) {
	root := t.TempDir()
	media := filepath.Join(root, "books")
	if err := os.Mkdir(media, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(media, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("完全相同.txt", []byte(novelBody(1, 4000)))
	write("任意改名.txt", []byte(novelBody(1, 4000)))
	write("编码甲.txt", []byte("编码不同但正文一致\n保留空格 ABC\n"))
	gb, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte("编码不同但正文一致\r\n保留空格 ABC\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	write("编码乙.txt", gb)
	long := novelBody(2, 20000)
	short := []byte(string([]rune(long)[4000:9000]))
	write("奇幻冒险之旅1-200.txt", []byte(long))
	write("奇幻冒险之旅1-50.txt", short)
	write("短版下载副本.txt", short)
	write("名称相同的作品.txt", []byte(novelBody(3, 3000)))
	write("名称相同的作品www.example.com.txt", []byte(novelBody(4, 3000)))
	opts := Options{Paths: []string{media}, CachePath: filepath.Join(root, "cache.sqlite"), IncludeTexts: true, Recursive: true, TextWorkers: 2}
	a, err := Scan(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Errors) != 0 {
		t.Fatalf("scan errors: %+v", a.Errors)
	}
	if len(a.Groups) != 4 {
		t.Fatalf("want 4 groups, got %+v", a.Groups)
	}
	types := map[model.GroupType]int{}
	for _, g := range a.Groups {
		types[g.GroupType]++
		cleanup := 0
		for _, it := range g.Items {
			if it.IsCleanup() {
				cleanup++
			}
		}
		switch g.GroupType {
		case model.GroupExact, model.GroupTextEqual:
			if cleanup != 1 {
				t.Fatalf("lost equal cleanup: %+v", g)
			}
		case model.GroupTextContained:
			if len(g.Items) != 3 || cleanup != 1 {
				t.Fatalf("lost raw copy or cleaned a version: %+v", g)
			}
		case model.GroupTextName:
			if cleanup != 0 {
				t.Fatalf("name-only cleanup: %+v", g)
			}
		default:
			t.Fatalf("unexpected type: %s", g.GroupType)
		}
	}
	for _, typ := range []model.GroupType{model.GroupExact, model.GroupTextEqual, model.GroupTextContained, model.GroupTextName} {
		if types[typ] != 1 {
			t.Fatalf("missing layer %s: %v", typ, types)
		}
	}
	b, err := Scan(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Groups, b.Groups) {
		t.Fatalf("cold/warm mismatch\ncold=%+v\nwarm=%+v", a.Groups, b.Groups)
	}
}

func TestLargeTXTAlwaysVerifiesFullHash(t *testing.T) {
	root := t.TempDir()
	a := make([]byte, 5<<20)
	for i := range a {
		a[i] = 'a'
	}
	b := append([]byte(nil), a...)
	b[(1<<20)+100] = 'b'
	for name, data := range map[string][]byte{"甲.txt": a, "乙.txt": b} {
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	r, err := Scan(Options{Paths: []string{root}, CachePath: filepath.Join(t.TempDir(), "cache.sqlite"), IncludeTexts: true, Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range r.Groups {
		if g.GroupType == model.GroupExact || g.GroupType == model.GroupTextEqual {
			t.Fatalf("sample collision incorrectly confirmed: %+v", g)
		}
	}
}

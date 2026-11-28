package pipeline

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"media-dedupe/internal/cache"
	"media-dedupe/internal/model"
	"media-dedupe/internal/progress"
)

func TestDecodeWarningsKeepNamesAndRecoverAfterFileChanges(t *testing.T) {
	root := t.TempDir()
	media := filepath.Join(root, "novels")
	os.Mkdir(media, 0700)
	paths := []string{filepath.Join(media, "同名长篇小说.txt"), filepath.Join(media, "同名长篇小说(1).txt"), filepath.Join(media, "同名长篇小说(2).txt")}
	for i, data := range [][]byte{[]byte(novelBody(70, 3000)), {0xff, 0x11}, {0xff, 0x12}} {
		if err := os.WriteFile(paths[i], data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	events := []progress.Event{}
	opts := Options{Paths: []string{media}, CachePath: filepath.Join(root, "cache.sqlite"), IncludeTexts: true, Recursive: true, TextWorkers: 2, OnEvent: func(ev progress.Event) { events = append(events, ev) }}
	a, err := Scan(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Groups) != 1 || len(a.Groups[0].Items) != 3 || len(a.Errors) != 2 {
		t.Fatalf("result %+v", a)
	}
	for _, e := range a.Errors {
		if e.Kind != model.ErrorText || e.Stage != "text_decode" {
			t.Fatalf("error %+v", e)
		}
	}
	for _, it := range a.Groups[0].Items {
		if it.Action != model.ActionReview {
			t.Fatalf("unverified cleanup %+v", it)
		}
	}
	sawTextProgress := false
	for _, ev := range events {
		if ev.Stage == progress.StageText && ev.Total > 0 && ev.Current > 0 {
			sawTextProgress = true
		}
	}
	if !sawTextProgress {
		t.Fatal("no text progress")
	}
	b, err := Scan(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Groups, b.Groups) {
		t.Fatal("cold/warm groups differ")
	}
	c, err := cache.Open(opts.CachePath)
	if err != nil {
		t.Fatal(err)
	}
	var failures, files int
	c.DB().QueryRow("SELECT count(*) FROM text_facts WHERE decode_error != ''").Scan(&failures)
	c.DB().QueryRow("SELECT count(*) FROM files").Scan(&files)
	c.Close()
	if failures != 2 || files != 3 {
		t.Fatalf("cached failures=%d files=%d", failures, files)
	}
	os.WriteFile(paths[1], []byte(novelBody(72, 4000)), 0600)
	d, err := Scan(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Errors) != 1 {
		t.Fatalf("did not retry changed file: %+v", d.Errors)
	}
}

func TestTextCacheVersionFiveMigratesLegacySchema(t *testing.T) {
	root := t.TempDir()
	media := filepath.Join(root, "books")
	os.Mkdir(media, 0700)
	path := filepath.Join(media, "私用区.txt")
	os.WriteFile(path, []byte{0xa1, 0x40}, 0600)
	opts := Options{Paths: []string{media}, CachePath: filepath.Join(root, "cache.sqlite"), IncludeTexts: true, Recursive: true}
	if _, err := Scan(opts); err != nil {
		t.Fatal(err)
	}
	c, err := cache.Open(opts.CachePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB().Exec("UPDATE text_facts SET version=4,text_hash='stale'"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB().Exec("ALTER TABLE text_facts DROP COLUMN decode_error"); err != nil {
		t.Fatal(err)
	}
	// Simulate a cache from a binary that predates schema versioning: no version
	// recorded, so the next open migrates it.
	if _, err := c.DB().Exec("PRAGMA user_version = 0"); err != nil {
		t.Fatal(err)
	}
	c.Close()
	if r, err := Scan(opts); err != nil || len(r.Errors) > 0 {
		t.Fatalf("migration result=%+v err=%v", r, err)
	}
	c, err = cache.Open(opts.CachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var digest string
	var version int
	if err := c.DB().QueryRow("SELECT text_hash,version FROM text_facts").Scan(&digest, &version); err != nil {
		t.Fatal(err)
	}
	if digest == "stale" || digest == "" || version != 5 {
		t.Fatalf("stale digest=%q version=%d", digest, version)
	}
}

func TestFullTextProofCacheSurvivesRenameWithCurrentPaths(t *testing.T) {
	root := t.TempDir()
	media := filepath.Join(root, "books")
	os.Mkdir(media, 0700)
	short := novelBody(89, 3000)
	long := novelBody(90, 1000) + short
	old := filepath.Join(media, "short.txt")
	renamed := filepath.Join(media, "renamed.txt")
	os.WriteFile(old, []byte(short), 0600)
	os.WriteFile(filepath.Join(media, "long.txt"), []byte(long), 0600)
	opts := Options{Paths: []string{media}, CachePath: filepath.Join(root, "cache.sqlite"), IncludeTexts: true, Recursive: true}
	a, err := Scan(opts)
	if err != nil || len(a.Groups) != 1 {
		t.Fatalf("scan=%+v err=%v", a, err)
	}
	c, err := cache.Open(opts.CachePath)
	if err != nil {
		t.Fatal(err)
	}
	var before int
	c.DB().QueryRow("SELECT count(*) FROM text_verifications").Scan(&before)
	c.Close()
	if before != 1 {
		t.Fatalf("proof rows=%d", before)
	}
	os.Rename(old, renamed)
	b, err := Scan(opts)
	if err != nil || len(b.Groups) != 1 {
		t.Fatalf("scan=%+v err=%v", b, err)
	}
	seen := false
	for _, item := range b.Groups[0].Items {
		for _, reason := range item.Reasons {
			if strings.Contains(reason, old) {
				t.Fatal("stale path in cached proof")
			}
			if strings.Contains(reason, "全文复核") {
				seen = true
			}
		}
	}
	if !seen {
		t.Fatal("lost proof after rename")
	}
	c, err = cache.Open(opts.CachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var after int
	c.DB().QueryRow("SELECT count(*) FROM text_verifications").Scan(&after)
	if after != before {
		t.Fatal("proof not keyed by content")
	}
}

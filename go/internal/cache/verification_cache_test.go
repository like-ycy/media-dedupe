package cache

import (
	"path/filepath"
	"testing"

	"media-dedupe/internal/model"
)

func TestVerificationCacheKeysVersionAndClear(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "cache.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	want := model.TextVerification{Matched: true, ShorterIsLeft: true, StartChar: 201, CharacterCount: 500, ExtraCharacters: 300}
	if err := c.SaveTextVerification("a", "b", want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := c.LoadTextVerification("a", "b")
	if err != nil || !ok || got != want {
		t.Fatalf("got=%+v ok=%v err=%v", got, ok, err)
	}
	if _, ok, err := c.LoadTextVerification("a", "c"); err != nil || ok {
		t.Fatal("stale text digest reused")
	}
	c.DB().Exec("UPDATE text_verifications SET version=0")
	if _, ok, err := c.LoadTextVerification("a", "b"); err != nil || ok {
		t.Fatal("stale algorithm reused")
	}
	c.DB().Exec("UPDATE text_verifications SET version=1,result_json='bad json'")
	if _, _, err := c.LoadTextVerification("a", "b"); err == nil {
		t.Fatal("hidden corrupt cache")
	}
	if err := c.Clear(); err != nil {
		t.Fatal(err)
	}
	var count int
	c.DB().QueryRow("SELECT count(*) FROM text_verifications").Scan(&count)
	if count != 0 {
		t.Fatal("proof cache not cleared")
	}
}

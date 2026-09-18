package updater

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

func TestMatchAsset(t *testing.T) {
	assets := []ReleaseAsset{
		{Name: "media-dedupe-app_1.2.0_darwin_arm64.zip", BrowserDownloadURL: "https://example.com/a"},
		{Name: "media-dedupe-app_1.2.0_darwin_amd64.zip", BrowserDownloadURL: "https://example.com/b"},
		{Name: "media-dedupe-app_1.2.0_windows_amd64.zip", BrowserDownloadURL: "https://example.com/c"},
		{Name: "SHA256SUMS.txt", BrowserDownloadURL: "https://example.com/s"},
		{Name: "aap_1.2.0_darwin_arm64.zip", BrowserDownloadURL: "https://example.com/old"},
	}

	got := matchAsset(assets, "darwin", "arm64")
	if got == nil || got.Name != "media-dedupe-app_1.2.0_darwin_arm64.zip" {
		t.Fatalf("darwin/arm64 match = %+v", got)
	}

	got = matchAsset(assets, "darwin", "amd64")
	if got == nil || got.Name != "media-dedupe-app_1.2.0_darwin_amd64.zip" {
		t.Fatalf("darwin/amd64 match = %+v", got)
	}

	got = matchAsset(assets, "windows", "amd64")
	if got == nil || got.Name != "media-dedupe-app_1.2.0_windows_amd64.zip" {
		t.Fatalf("windows/amd64 match = %+v", got)
	}

	if matchAsset(assets, "linux", "amd64") != nil {
		t.Fatal("expected nil for unsupported platform")
	}
}

func TestMatchAssetRejectsLegacyNames(t *testing.T) {
	assets := []ReleaseAsset{
		{Name: "aap_1.2.0_darwin_arm64.zip"},
		{Name: "media-dedupe-app_1.2.0_windows_amd64.tar.gz"},
		{Name: "media-dedupe-app_1.2.0_darwin_arm64.tar.gz"},
	}

	if matchAsset(assets, "darwin", "arm64") != nil {
		t.Fatal("legacy asset names should not match")
	}
	if matchAsset(assets, "windows", "amd64") != nil {
		t.Fatal("tar.gz asset should not match")
	}
}

func TestCheckUpdate(t *testing.T) {
	rel := map[string]any{
		"tag_name": "v9.9.9",
		"name":     "media-dedupe v9.9.9",
		"body":     "notes",
		"html_url": "https://github.com/like-ycy/media-dedupe/releases/tag/v9.9.9",
		"assets": []map[string]any{
			{
				"name":                 "media-dedupe-app_9.9.9_" + runtime.GOOS + "_" + runtime.GOARCH + ".zip",
				"size":                 1234,
				"browser_download_url": "https://example.com/pkg",
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(rel)
	}))
	defer srv.Close()

	old := GitHubAPI
	GitHubAPI = srv.URL
	defer func() { GitHubAPI = old }()

	m := New()
	info, err := m.CheckUpdate()
	if err != nil {
		t.Fatalf("CheckUpdate: %v", err)
	}
	if !info.HasUpdate {
		t.Fatalf("expected HasUpdate, got %+v", info)
	}
	if info.LatestVersion != "v9.9.9" {
		t.Fatalf("LatestVersion = %q", info.LatestVersion)
	}
	if info.DownloadURL == "" && runtime.GOOS != "linux" {
		// linux 无匹配资产属正常
		if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
			t.Fatalf("expected DownloadURL for %s/%s", runtime.GOOS, runtime.GOARCH)
		}
	}
	if info.AssetName != "" && !strings.HasSuffix(info.AssetName, ".zip") {
		t.Fatalf("unexpected asset name %q", info.AssetName)
	}
	if m.LatestInfo() == nil {
		t.Fatal("LatestInfo should be cached")
	}
}

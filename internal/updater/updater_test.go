package updater

import (
	"crypto/sha256"
	"encoding/hex"
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

// TestCheckUpdateRecordsChecksumsURL keeps the SHA256SUMS.txt download URL so a
// package without a digest can still be verified.
func TestCheckUpdateRecordsChecksumsURL(t *testing.T) {
	rel := map[string]any{
		"tag_name": "v9.9.9",
		"assets": []map[string]any{
			{
				"name":                 "media-dedupe-app_9.9.9_" + runtime.GOOS + "_" + runtime.GOARCH + ".zip",
				"size":                 1234,
				"browser_download_url": "https://example.com/pkg",
			},
			{"name": "SHA256SUMS.txt", "browser_download_url": "https://example.com/sums"},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(rel)
	}))
	defer srv.Close()

	old := GitHubAPI
	GitHubAPI = srv.URL
	defer func() { GitHubAPI = old }()

	info, err := New().CheckUpdate()
	if err != nil {
		t.Fatalf("CheckUpdate: %v", err)
	}
	if info.ChecksumsURL != "https://example.com/sums" {
		t.Fatalf("ChecksumsURL = %q", info.ChecksumsURL)
	}
}

// updateTestServer serves a package and its checksum file.
func updateTestServer(t *testing.T, payload []byte, sums string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/pkg", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	})
	mux.HandleFunc("/sums", func(w http.ResponseWriter, r *http.Request) {
		if sums == "" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(sums))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newTestManager(srv *httptest.Server, assetName, digest, checksumsURL string, size int64) *Manager {
	m := New()
	m.latestInfo = &UpdateInfo{
		HasUpdate:    true,
		DownloadURL:  srv.URL + "/pkg",
		AssetName:    assetName,
		AssetSize:    size,
		Digest:       digest,
		ChecksumsURL: checksumsURL,
	}
	return m
}

// TestStartDownloadFallsBackToChecksumsFile covers the missing-digest path: the
// package is verified against SHA256SUMS.txt before it may be installed.
func TestStartDownloadFallsBackToChecksumsFile(t *testing.T) {
	payload := []byte("fake update package bytes")
	sum := sha256.Sum256(payload)
	srv := updateTestServer(t, payload, hex.EncodeToString(sum[:])+"  app.zip\n")

	m := newTestManager(srv, "app.zip", "", srv.URL+"/sums", int64(len(payload)))
	if err := m.StartDownload(false, nil); err != nil {
		t.Fatalf("download: %v", err)
	}
	if _, _, err := m.GetDownloadedFile(); err != nil {
		t.Fatalf("package should be complete: %v", err)
	}
}

// TestStartDownloadRejectsMismatchedChecksums proves a wrong digest stops the
// install instead of silently skipping verification.
func TestStartDownloadRejectsMismatchedChecksums(t *testing.T) {
	payload := []byte("fake update package bytes")
	other := sha256.Sum256([]byte("different content"))
	srv := updateTestServer(t, payload, hex.EncodeToString(other[:])+"  app.zip\n")

	m := newTestManager(srv, "app.zip", "", srv.URL+"/sums", int64(len(payload)))
	if err := m.StartDownload(false, nil); err == nil {
		t.Fatal("expected a checksum failure")
	}
	if _, _, err := m.GetDownloadedFile(); err == nil {
		t.Fatal("a rejected package must not be marked complete")
	}
}

// TestStartDownloadRequiresAnyChecksum is the core P1-3 guarantee: no digest and
// no checksum file means no automatic install.
func TestStartDownloadRequiresAnyChecksum(t *testing.T) {
	payload := []byte("fake update package bytes")
	srv := updateTestServer(t, payload, "")

	m := newTestManager(srv, "app.zip", "", "", int64(len(payload)))
	err := m.StartDownload(false, nil)
	if err == nil {
		t.Fatal("expected the download to be refused")
	}
	if !strings.Contains(err.Error(), "缺少校验信息") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "releases") {
		t.Fatalf("error should point at the release page: %v", err)
	}
	if _, _, err := m.GetDownloadedFile(); err == nil {
		t.Fatal("a refused package must not be marked complete")
	}
}

// TestStartDownloadAcceptsReleaseDigest is the regression guard for the normal
// path: an asset digest from the API still verifies on its own.
func TestStartDownloadAcceptsReleaseDigest(t *testing.T) {
	payload := []byte("fake update package bytes")
	sum := sha256.Sum256(payload)
	srv := updateTestServer(t, payload, "")

	m := newTestManager(srv, "app.zip", "sha256:"+hex.EncodeToString(sum[:]), "", int64(len(payload)))
	if err := m.StartDownload(false, nil); err != nil {
		t.Fatalf("download: %v", err)
	}
}

func TestProxyURL(t *testing.T) {
	const target = "https://github.com/o/r/releases/download/v1/pkg.zip"
	if got := proxyURL(target, true); got != DefaultProxyPrefix+target {
		t.Fatalf("proxy url = %q", got)
	}
	if got := proxyURL(target, false); got != target {
		t.Fatalf("direct url = %q", got)
	}
	if got := proxyURL("", true); got != "" {
		t.Fatalf("empty url = %q", got)
	}
}

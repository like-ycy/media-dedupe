package updater

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

type reviewTransport func(*http.Request) (*http.Response, error)

func (f reviewTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReviewChecksumFailuresCannotComplete(t *testing.T) {
	payload := []byte("review update payload")
	for _, tc := range []struct{ name, digest, sums string }{
		{name: "missing file"},
		{name: "missing asset", sums: strings.Repeat("0", 64) + "  other.zip\n"},
		{name: "invalid hex", sums: strings.Repeat("z", 64) + "  app.zip\n"},
		{name: "unsupported digest", digest: "sha1:1234"},
		{name: "mismatched digest", digest: "sha256:" + strings.Repeat("0", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := updateTestServer(t, payload, tc.sums)
			m := newTestManager(srv, "app.zip", tc.digest, srv.URL+"/sums", int64(len(payload)))
			if err := m.StartDownload(false, nil); err == nil {
				t.Fatal("accepted invalid checksum")
			}
			if _, _, err := m.GetDownloadedFile(); err == nil {
				t.Fatal("marked complete")
			}
			if _, err := os.Stat(m.downloadedTo); !os.IsNotExist(err) {
				t.Fatalf("unverified package remains: %v", err)
			}
		})
	}
}

func TestReviewProxyCoversPackageAndChecksum(t *testing.T) {
	const pkg = "https://github.com/o/r/releases/download/v1/app.zip"
	const sums = "https://github.com/o/r/releases/download/v1/SHA256SUMS.txt"
	payload := "review update payload"
	digest := sha256.Sum256([]byte(payload))
	seen := map[string]bool{}
	old := http.DefaultTransport
	http.DefaultTransport = reviewTransport(func(r *http.Request) (*http.Response, error) {
		seen[r.URL.String()] = true
		body := payload
		if r.URL.String() == DefaultProxyPrefix+sums {
			body = fmt.Sprintf("%x *app.zip\n", digest)
		} else if r.URL.String() != DefaultProxyPrefix+pkg {
			return nil, fmt.Errorf("unexpected URL %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)), Header: make(http.Header), Request: r}, nil
	})
	defer func() { http.DefaultTransport = old }()
	m := New()
	m.latestInfo = &UpdateInfo{DownloadURL: pkg, ChecksumsURL: sums, AssetName: "app.zip", AssetSize: int64(len(payload))}
	defer func() {
		if m.tempDir != "" {
			_ = os.RemoveAll(m.tempDir)
		}
	}()
	if err := m.StartDownload(true, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.GetDownloadedFile(); err != nil {
		t.Fatal(err)
	}
	if !seen[DefaultProxyPrefix+pkg] || !seen[DefaultProxyPrefix+sums] {
		t.Fatalf("URLs: %+v", seen)
	}
}

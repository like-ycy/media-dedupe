package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"media-dedupe/internal/version"
)

const (
	RepoOwner = "like-ycy"
	RepoName  = "media-dedupe"
	// 国内加速代理前缀
	DefaultProxyPrefix = "https://ghfast.top/"
)

// GitHubAPI 是默认请求的 Releases API 地址，测试中可被覆盖。
var GitHubAPI = "https://api.github.com/repos/" + RepoOwner + "/" + RepoName + "/releases/latest"

// ReleaseAsset 表示 GitHub Release 中的附件资产。
type ReleaseAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	Digest             string `json:"digest"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// GitHubRelease 表示 GitHub Release API 返回的元数据。
type GitHubRelease struct {
	TagName string         `json:"tag_name"`
	Name    string         `json:"name"`
	Body    string         `json:"body"`
	HTMLURL string         `json:"html_url"`
	Assets  []ReleaseAsset `json:"assets"`
}

// UpdateInfo 是传递给前端和业务层的更新信息。
type UpdateInfo struct {
	HasUpdate      bool   `json:"hasUpdate"`
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion"`
	ReleaseName    string `json:"releaseName"`
	ReleaseNotes   string `json:"releaseNotes"`
	ReleaseURL     string `json:"releaseUrl"`
	DownloadURL    string `json:"downloadUrl"`
	AssetName      string `json:"assetName"`
	AssetSize      int64  `json:"assetSize"`
	Digest         string `json:"digest,omitempty"`
	Platform       string `json:"platform"`
}

// DownloadProgress 表示下载进度状态。
type DownloadProgress struct {
	Downloaded int64   `json:"downloaded"`
	Total      int64   `json:"total"`
	Percent    float64 `json:"percent"`
	Speed      int64   `json:"speed"` // bytes per second
}

// Manager 负责管理检查、下载及应用更新。
type Manager struct {
	mu               sync.Mutex
	httpClient       *http.Client
	downloadCtx      context.Context
	cancelDown       context.CancelFunc
	downloadDone     chan struct{}
	downloading      bool
	downloadedTo     string
	tempDir          string
	downloadComplete bool
	applyPending     bool
	latestInfo       *UpdateInfo
}

// New 创建 Manager 实例。
func New() *Manager {
	return &Manager{
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// CheckUpdate 请求 GitHub 获取最新版本，并与当前版本进行比较。
func (m *Manager) CheckUpdate() (*UpdateInfo, error) {
	currentVer := version.GetVersion()

	req, err := http.NewRequest("GET", GitHubAPI, nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "media-dedupe-app")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 GitHub Releases 失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API 响应异常状态码: %d", resp.StatusCode)
	}

	var rel GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("解析 Release 信息失败: %w", err)
	}

	targetAsset := matchAsset(rel.Assets, runtime.GOOS, runtime.GOARCH)

	hasUpdate := version.Compare(rel.TagName, currentVer) > 0

	info := &UpdateInfo{
		HasUpdate:      hasUpdate,
		CurrentVersion: currentVer,
		LatestVersion:  rel.TagName,
		ReleaseName:    rel.Name,
		ReleaseNotes:   rel.Body,
		ReleaseURL:     rel.HTMLURL,
		Platform:       runtime.GOOS,
	}

	if targetAsset != nil {
		info.DownloadURL = targetAsset.BrowserDownloadURL
		info.AssetName = targetAsset.Name
		info.AssetSize = targetAsset.Size
		info.Digest = targetAsset.Digest
	}

	m.mu.Lock()
	m.latestInfo = info
	m.mu.Unlock()

	return info, nil
}

// LatestInfo 返回最近一次检查结果，供启动阶段前端补取事件。
func (m *Manager) LatestInfo() *UpdateInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.latestInfo == nil {
		return nil
	}
	info := *m.latestInfo
	return &info
}

// matchAsset 匹配当前系统平台与架构对应的资产。
// 命名约定（与 release.yml 一致）：media-dedupe-app_{ver}_{goos}_{goarch}.zip
func matchAsset(assets []ReleaseAsset, goos, goarch string) *ReleaseAsset {
	wantSuffix := "_" + goos + "_" + goarch + ".zip"
	for i := range assets {
		name := strings.ToLower(assets[i].Name)
		if strings.HasPrefix(name, "media-dedupe-app_") && strings.HasSuffix(name, wantSuffix) {
			return &assets[i]
		}
	}
	return nil
}

// StartDownload 开始下载更新包，通过回调通知进度。
func (m *Manager) StartDownload(useProxy bool, onProgress func(DownloadProgress)) error {
	m.mu.Lock()
	if m.downloading {
		m.mu.Unlock()
		return fmt.Errorf("已有正在进行的更新下载")
	}
	if m.latestInfo == nil || m.latestInfo.DownloadURL == "" {
		m.mu.Unlock()
		return fmt.Errorf("未找到有效的新版本下载地址")
	}

	downloadURL := m.latestInfo.DownloadURL
	if useProxy {
		downloadURL = DefaultProxyPrefix + downloadURL
	}

	assetName := m.latestInfo.AssetName
	expectedSize := m.latestInfo.AssetSize
	expectedDigest := m.latestInfo.Digest
	if assetName == "" {
		m.mu.Unlock()
		return fmt.Errorf("更新包文件名无效")
	}
	assetName = filepath.Base(assetName)
	if assetName == "." || assetName == string(filepath.Separator) {
		m.mu.Unlock()
		return fmt.Errorf("更新包文件名无效")
	}
	if !strings.HasSuffix(strings.ToLower(assetName), ".zip") {
		m.mu.Unlock()
		return fmt.Errorf("更新包格式不受支持: %s", assetName)
	}

	tempDir, err := os.MkdirTemp("", "media-dedupe-update-*")
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("创建临时目录失败: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	downloadDone := make(chan struct{})
	m.downloadCtx = ctx
	m.cancelDown = cancel
	m.downloadDone = downloadDone
	m.downloading = true
	m.tempDir = tempDir
	m.downloadedTo = filepath.Join(tempDir, assetName)
	m.downloadComplete = false
	destPath := m.downloadedTo
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		tempDir := m.tempDir
		cleanup := !m.downloadComplete && !m.applyPending
		m.downloading = false
		m.cancelDown = nil
		m.downloadCtx = nil
		m.downloadDone = nil
		m.mu.Unlock()
		close(downloadDone)
		if cleanup && tempDir != "" {
			_ = os.RemoveAll(tempDir)
		}
	}()
	req, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
	if err != nil {
		return fmt.Errorf("创建下载请求失败: %w", err)
	}
	req.Header.Set("User-Agent", "media-dedupe-app")

	client := &http.Client{
		Timeout: 0, // 下载大文件不设单次请求全局超时，由 ctx 控制
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载服务器返回异常状态码: %d", resp.StatusCode)
	}

	totalSize := resp.ContentLength
	if totalSize <= 0 && expectedSize > 0 {
		totalSize = expectedSize
	}

	outFile, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("创建目标文件失败: %w", err)
	}
	defer outFile.Close()

	buf := make([]byte, 64*1024)
	hasher := sha256.New()
	var downloaded int64
	lastTime := time.Now()
	var lastBytes int64

	for {
		select {
		case <-ctx.Done():
			outFile.Close()
			_ = os.Remove(destPath)
			return fmt.Errorf("下载已取消")
		default:
		}

		n, rErr := resp.Body.Read(buf)
		if n > 0 {
			if _, wErr := outFile.Write(buf[:n]); wErr != nil {
				return fmt.Errorf("写入文件失败: %w", wErr)
			}
			if _, hErr := hasher.Write(buf[:n]); hErr != nil {
				return fmt.Errorf("计算文件校验失败: %w", hErr)
			}
			downloaded += int64(n)

			now := time.Now()
			elapsed := now.Sub(lastTime).Seconds()
			if elapsed >= 0.3 || rErr == io.EOF {
				speed := int64(float64(downloaded-lastBytes) / elapsed)
				lastTime = now
				lastBytes = downloaded

				var pct float64
				if totalSize > 0 {
					pct = float64(downloaded) / float64(totalSize) * 100
					if pct > 100 {
						pct = 100
					}
				}

				if onProgress != nil {
					onProgress(DownloadProgress{
						Downloaded: downloaded,
						Total:      totalSize,
						Percent:    pct,
						Speed:      speed,
					})
				}
			}
		}

		if rErr == io.EOF {
			break
		}
		if rErr != nil {
			return fmt.Errorf("读取数据流失败: %w", rErr)
		}
	}
	if err := outFile.Close(); err != nil {
		return fmt.Errorf("关闭下载文件失败: %w", err)
	}
	if expectedSize > 0 && downloaded != expectedSize {
		_ = os.Remove(destPath)
		return fmt.Errorf("下载文件大小校验失败: 期望 %d 字节，实际 %d 字节", expectedSize, downloaded)
	}
	if expectedDigest != "" {
		if err := verifyDigest(expectedDigest, hasher.Sum(nil)); err != nil {
			_ = os.Remove(destPath)
			return err
		}
	}
	m.mu.Lock()
	m.downloadComplete = true
	m.mu.Unlock()

	return nil
}

func verifyDigest(expected string, actual []byte) error {
	parts := strings.SplitN(strings.TrimSpace(expected), ":", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "sha256") {
		return fmt.Errorf("不支持的更新包摘要格式: %q", expected)
	}
	want, err := hex.DecodeString(parts[1])
	if err != nil || len(want) != sha256.Size {
		return fmt.Errorf("更新包摘要格式无效: %q", expected)
	}
	if !strings.EqualFold(hex.EncodeToString(actual), parts[1]) {
		return fmt.Errorf("更新包 SHA-256 校验失败")
	}
	return nil
}

// CancelDownload 取消当前的下载。
func (m *Manager) CancelDownload() {
	m.mu.Lock()
	cancel := m.cancelDown
	done := m.downloadDone
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// CleanTempDir 清理更新临时目录。
func (m *Manager) CleanTempDir() {
	m.mu.Lock()
	if m.applyPending {
		m.mu.Unlock()
		return
	}
	dir := m.tempDir
	m.tempDir = ""
	m.downloadedTo = ""
	m.downloadComplete = false
	m.mu.Unlock()

	if dir != "" {
		_ = os.RemoveAll(dir)
	}
}

// markApplyPending 保留临时目录，交由后台替换脚本清理。
func (m *Manager) markApplyPending() {
	m.mu.Lock()
	m.applyPending = true
	m.mu.Unlock()
}

// GetDownloadedFile 获取已下载的安装包路径。
func (m *Manager) GetDownloadedFile() (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.downloading {
		return "", "", fmt.Errorf("更新包仍在下载中")
	}
	if !m.downloadComplete || m.downloadedTo == "" {
		return "", "", fmt.Errorf("尚未下载完成更新包")
	}
	return m.downloadedTo, m.tempDir, nil
}

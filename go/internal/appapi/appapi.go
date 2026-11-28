package appapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"media-dedupe/internal/cache"
	"media-dedupe/internal/discovery"
	"media-dedupe/internal/fsutil"
	"media-dedupe/internal/hashfile"
	"media-dedupe/internal/mediastore"
	"media-dedupe/internal/model"
	"media-dedupe/internal/ops"
	"media-dedupe/internal/pipeline"
	"media-dedupe/internal/progress"
	"media-dedupe/internal/project"
	"media-dedupe/internal/report"
	"media-dedupe/internal/updater"
	"media-dedupe/internal/version"
	"media-dedupe/internal/video"
)

// App is the single binding surface for the Wails frontend.
type App struct {
	store         *project.Store
	media         *mediastore.Store
	ctx           context.Context
	emit          func(event string, payload any)
	mu            sync.Mutex
	scans         map[string]*scanState
	updater       *updater.Manager
	pendingUpdate *updater.UpdateInfo
}

type scanState struct {
	cancel context.CancelFunc
	done   chan struct{}
	status ScanStatusDTO
}

type AppInfo struct {
	Version    string `json:"version"`
	FFmpeg     bool   `json:"ffmpeg"`
	FFprobe    bool   `json:"ffprobe"`
	AppData    string `json:"appData"`
	DeleteMode string `json:"deleteMode"`
	Engine     string `json:"engine"`
	Platform   string `json:"platform"`
}

type ProjectDTO struct {
	ID               string              `json:"id"`
	Name             string              `json:"name"`
	CreatedAt        string              `json:"createdAt"`
	UpdatedAt        string              `json:"updatedAt"`
	Paths            []string            `json:"paths"`
	Recursive        bool                `json:"recursive"`
	Threshold        float64             `json:"threshold"`
	IncludeImages    bool                `json:"includeImages"`
	IncludeVideos    bool                `json:"includeVideos"`
	IncludeTexts     bool                `json:"includeTexts"`
	TextThreshold    float64             `json:"textThreshold"`
	TextWorkers      int                 `json:"textWorkers"`
	FrameCount       int                 `json:"frameCount"`
	VideoCompareMode string              `json:"videoCompareMode"`
	Workers          int                 `json:"workers"`
	VideoWorkers     int                 `json:"videoWorkers"`
	EnableThumbs     bool                `json:"enableThumbs"`
	LastScanAt       *string             `json:"lastScanAt"`
	LastSummary      project.LastSummary `json:"lastSummary"`
	Scanning         bool                `json:"scanning"`
	FFmpegReady      bool                `json:"ffmpegReady"`
}

type PathStatus struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	IsDir  bool   `json:"isDir"`
	Error  string `json:"error,omitempty"`
}

type ScanStatusDTO struct {
	ProjectID  string          `json:"projectId"`
	Running    bool            `json:"running"`
	Canceled   bool            `json:"canceled"`
	LastError  string          `json:"lastError,omitempty"`
	Event      *progress.Event `json:"event,omitempty"`
	StartedAt  string          `json:"startedAt,omitempty"`
	FinishedAt string          `json:"finishedAt,omitempty"`
	Percent    float64         `json:"percent"`
	Groups     int             `json:"groups"`
	FilesSeen  int             `json:"filesSeen"`
	Errors     int             `json:"errors"`
	DurationMs int64           `json:"durationMs"`
}

// ScanErrorDTO is one file that the latest scan could not process.
type ScanErrorDTO struct {
	Path    string `json:"path"`
	Stage   string `json:"stage"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

type GroupListDTO struct {
	GroupID           int64   `json:"groupId"`
	GroupType         string  `json:"groupType"`
	Confidence        float64 `json:"confidence"`
	RecommendedFileID int64   `json:"recommendedFileId"`
	Status            string  `json:"status"`
	MemberCount       int     `json:"memberCount"`
	CleanupCount      int     `json:"cleanupCount"`
	ReclaimableBytes  int64   `json:"reclaimableBytes"`
	TypeLabel         string  `json:"typeLabel"`
	MediaKind         string  `json:"mediaKind"` // image | video | text | mixed
}

// GroupPageDTO is one page of the results table plus the size of the whole
// match set, so the UI can say "showing 100 of 4213" instead of pretending the
// page is everything that matched.
type GroupPageDTO struct {
	Items   []GroupListDTO `json:"items"`
	Total   int            `json:"total"`
	Limit   int            `json:"limit"`
	Offset  int            `json:"offset"`
	HasMore bool           `json:"hasMore"`
}

// Group page size: small enough that the first paint is instant on a big
// project, large enough that scrolling is not a request per row.
const (
	defaultGroupPageSize = 100
	maxGroupPageSize     = 500
)

type ItemDTO struct {
	FileID          int64    `json:"fileId"`
	Path            string   `json:"path"`
	Action          string   `json:"action"`
	Similarity      float64  `json:"similarity"`
	HammingDistance *int     `json:"hammingDistance"`
	QualityScore    float64  `json:"qualityScore"`
	SizeBytes       int64    `json:"sizeBytes"`
	ThumbURL        string   `json:"thumbUrl"`
	Width           int      `json:"width"`
	Height          int      `json:"height"`
	DurationMs      int64    `json:"durationMs"`
	MediaType       string   `json:"mediaType"`
	MediaHashHex    string   `json:"mediaHashHex"`
	IsRecommended   bool     `json:"isRecommended"`
	Exists          bool     `json:"exists"`
	Reasons         []string `json:"reasons"`
}

type GroupDetailDTO struct {
	GroupID           int64     `json:"groupId"`
	GroupType         string    `json:"groupType"`
	TypeLabel         string    `json:"typeLabel"`
	Confidence        float64   `json:"confidence"`
	RecommendedFileID int64     `json:"recommendedFileId"`
	Status            string    `json:"status"`
	Items             []ItemDTO `json:"items"`
	ReclaimableBytes  int64     `json:"reclaimableBytes"`
}

type SettingsDTO struct {
	DefaultThreshold         float64 `json:"defaultThreshold"`
	DefaultIncludeVideos     bool    `json:"defaultIncludeVideos"`
	DefaultIncludeTexts      bool    `json:"defaultIncludeTexts"`
	DefaultTextThreshold     float64 `json:"defaultTextThreshold"`
	DefaultTextWorkers       int     `json:"defaultTextWorkers"`
	DefaultFrameCount        int     `json:"defaultFrameCount"`
	DefaultVideoCompareMode  string  `json:"defaultVideoCompareMode"`
	DefaultWorkers           int     `json:"defaultWorkers"`
	DefaultVideoWorkers      int     `json:"defaultVideoWorkers"`
	DefaultVerifySampledHash bool    `json:"defaultVerifySampledHash"`
	DefaultDeleteMode        string  `json:"defaultDeleteMode"`
	AllowPermanentDelete     bool    `json:"allowPermanentDelete"`
	FFmpegPath               string  `json:"ffmpegPath"`
	FFprobePath              string  `json:"ffprobePath"`
	FFmpegAvailable          bool    `json:"ffmpegAvailable"`
	FFprobeAvailable         bool    `json:"ffprobeAvailable"`
}

type DeleteRequestDTO struct {
	ProjectID string   `json:"projectId"`
	Mode      string   `json:"mode"`
	Paths     []string `json:"paths"`
	GroupIDs  []int64  `json:"groupIds"`
	FileIDs   []int64  `json:"fileIds"`
}

// DeleteFailureDTO is one file that could not be deleted. FileID and GroupID let
// the UI retry exactly that subset instead of replaying a request whose other
// files are already gone.
type DeleteFailureDTO struct {
	Path    string `json:"path"`
	Message string `json:"message"`
	FileID  int64  `json:"fileId"`
	GroupID int64  `json:"groupId"`
}

type DeleteResultDTO struct {
	Succeeded       []string           `json:"succeeded"`
	Failed          []DeleteFailureDTO `json:"failed"`
	Mode            string             `json:"mode"`
	MarkedProcessed []int64            `json:"markedProcessed"`
	// MetadataErrors lists results-database updates that did not apply. The files
	// are already removed at that point, so the only recovery is a rescan; it
	// must be reported instead of being swallowed.
	MetadataErrors []string `json:"metadataErrors,omitempty"`
}

type FinishedPayload struct {
	ProjectID  string `json:"projectId"`
	Groups     int    `json:"groups"`
	FilesSeen  int    `json:"filesSeen"`
	Errors     int    `json:"errors"`
	DurationMs int64  `json:"durationMs"`
	Canceled   bool   `json:"canceled"`
}

// GroupFilter is the ListGroups query.
type GroupFilter struct {
	Status    string `json:"status"`
	GroupType string `json:"groupType"`
	Search    string `json:"search"`
	// Sort is resolved against a fixed list of orderings in the cache layer;
	// anything else falls back to confidence.
	Sort   string `json:"sort"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}

func NewApp() *App {
	return &App{
		scans:   map[string]*scanState{},
		updater: updater.New(),
	}
}

func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx
	store, err := project.Open("")
	if err != nil {
		store, _ = project.Open(filepath.Join(os.TempDir(), "media-dedupe-app"))
	}
	a.store = store
	// SetMediaBase already configured the store (and its base URL) before the
	// Wails runtime starts; recreating it here would drop the base and make
	// every preview URL resolve to "".
	if a.media == nil {
		a.media = mediastore.New()
	}
	_ = a.media.AllowRoot(filepath.Join(store.Root(), "projects"))
	if st, err := store.LoadSettings(); err == nil {
		video.ConfigureTools(st.FFmpegPath, st.FFprobePath)
	} else {
		video.ConfigureTools("", "")
	}
	go a.checkUpdateOnStartup()
}

// SetMediaBase configures absolute base URL for local media server.
func (a *App) SetMediaBase(base string) {
	if a.media == nil {
		a.media = mediastore.New()
	}
	a.media.SetBase(base)
}

// ServeHTTP implements http.Handler for the media server mount.
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if a.media == nil {
		http.NotFound(w, r)
		return
	}
	a.media.Handler().ServeHTTP(w, r)
}

func (a *App) SetEmitter(fn func(event string, payload any)) {
	a.emit = fn
}

func (a *App) AppReady() AppInfo {
	ffmpeg, ffprobe := video.Available()
	mode := "recycle"
	if a.store != nil {
		if st, err := a.store.LoadSettings(); err == nil {
			mode = st.DefaultDeleteMode
		}
	}
	root := ""
	if a.store != nil {
		root = a.store.Root()
	}
	return AppInfo{
		Version:    version.GetVersion(),
		FFmpeg:     ffmpeg,
		FFprobe:    ffprobe,
		AppData:    root,
		DeleteMode: mode,
		Engine:     "SQLite size+mtime + SHA-256 + pHash + TXT 指纹 + FFmpeg",
		Platform:   runtime.GOOS,
	}
}

func (a *App) toProjectDTO(p project.Project) ProjectDTO {
	ffmpeg, _ := video.Available()
	a.mu.Lock()
	scanning := false
	if st, ok := a.scans[p.ID]; ok {
		scanning = st.status.Running
	}
	a.mu.Unlock()
	return ProjectDTO{
		ID:            p.ID,
		Name:          p.Name,
		CreatedAt:     p.CreatedAt,
		UpdatedAt:     p.UpdatedAt,
		Paths:         p.Paths,
		Recursive:     p.Recursive,
		Threshold:     p.Threshold,
		IncludeImages: p.IncludeImages,
		IncludeVideos: p.IncludeVideos,
		IncludeTexts:  p.IncludeTexts, TextThreshold: p.TextThreshold, TextWorkers: p.TextWorkers,
		FrameCount:       p.FrameCount,
		VideoCompareMode: p.VideoCompareMode,
		Workers:          p.Workers,
		VideoWorkers:     p.VideoWorkers,
		EnableThumbs:     p.EnableThumbs,
		LastScanAt:       p.LastScanAt,
		LastSummary:      p.LastSummary,
		Scanning:         scanning,
		FFmpegReady:      ffmpeg,
	}
}

func (a *App) ListProjects() []ProjectDTO {
	list := a.store.List()
	out := make([]ProjectDTO, 0, len(list))
	for _, p := range list {
		out = append(out, a.toProjectDTO(p))
	}
	return out
}

func (a *App) GetProject(id string) (ProjectDTO, error) {
	p, ok := a.store.Get(id)
	if !ok {
		return ProjectDTO{}, errors.New("项目不存在")
	}
	return a.toProjectDTO(p), nil
}

func (a *App) CreateProject(input project.CreateInput) (ProjectDTO, error) {
	st, err := a.store.LoadSettings()
	if err != nil {
		return ProjectDTO{}, err
	}
	if input.Threshold <= 0 {
		input.Threshold = st.DefaultThreshold
	}
	if input.FrameCount <= 0 {
		input.FrameCount = st.DefaultFrameCount
	}
	if input.VideoCompareMode == "" {
		input.VideoCompareMode = st.DefaultVideoCompareMode
	}
	if input.Workers <= 0 {
		input.Workers = st.DefaultWorkers
	}
	if input.VideoWorkers <= 0 {
		input.VideoWorkers = st.DefaultVideoWorkers
	}
	if input.TextThreshold <= 0 {
		input.TextThreshold = st.DefaultTextThreshold
	}
	if input.TextWorkers <= 0 {
		input.TextWorkers = st.DefaultTextWorkers
	}
	// Default to images when nothing is selected.
	if !input.IncludeImages && !input.IncludeVideos && !input.IncludeTexts {
		input.IncludeImages = true
	}
	p, err := a.store.Create(input)
	if err != nil {
		return ProjectDTO{}, err
	}
	for _, path := range p.Paths {
		_ = a.touchRecent(path)
	}
	return a.toProjectDTO(p), nil
}

func (a *App) UpdateProject(id string, patch project.UpdateInput) (ProjectDTO, error) {
	p, err := a.store.Update(id, patch)
	if err != nil {
		return ProjectDTO{}, err
	}
	if patch.Paths != nil {
		for _, path := range p.Paths {
			_ = a.touchRecent(path)
		}
	}
	return a.toProjectDTO(p), nil
}

func (a *App) DeleteProject(id string) error {
	a.mu.Lock()
	if st, ok := a.scans[id]; ok && st != nil && st.status.Running {
		a.mu.Unlock()
		return errors.New("扫描进行中，无法删除项目")
	}
	a.mu.Unlock()
	return a.store.Delete(id)
}

func (a *App) touchRecent(path string) error {
	global := filepath.Join(a.store.Root(), "recent.sqlite")
	c, err := cache.Open(global)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.AddRecentDir(path)
}

func (a *App) ListRecentDirs() []string {
	global := filepath.Join(a.store.Root(), "recent.sqlite")
	c, err := cache.Open(global)
	if err != nil {
		return []string{}
	}
	defer c.Close()
	dirs, err := c.ListRecentDirs(30)
	if err != nil || dirs == nil {
		return []string{}
	}
	return dirs
}

func (a *App) SelectDirectory() (string, error) {
	if a.ctx == nil {
		return "", errors.New("应用尚未初始化")
	}
	return wailsruntime.OpenDirectoryDialog(a.ctx, wailsruntime.OpenDialogOptions{Title: "选择扫描目录"})
}

func (a *App) CheckPaths(paths []string) []PathStatus {
	out := make([]PathStatus, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		st := PathStatus{Path: p}
		if p == "" {
			st.Error = "路径为空"
			out = append(out, st)
			continue
		}
		fi, err := os.Stat(p)
		if err != nil {
			st.Exists = false
			st.Error = "路径不存在"
		} else {
			st.Exists = true
			st.IsDir = fi.IsDir()
			if !fi.IsDir() {
				st.Error = "不是目录"
			}
		}
		out = append(out, st)
	}
	return out
}

func (a *App) GetSettings() (SettingsDTO, error) {
	st, err := a.store.LoadSettings()
	if err != nil {
		return SettingsDTO{}, err
	}
	ffmpeg, ffprobe := video.Available()
	return SettingsDTO{
		DefaultThreshold:         st.DefaultThreshold,
		DefaultIncludeVideos:     st.DefaultIncludeVideos,
		DefaultIncludeTexts:      st.DefaultIncludeTexts,
		DefaultTextThreshold:     st.DefaultTextThreshold,
		DefaultTextWorkers:       st.DefaultTextWorkers,
		DefaultFrameCount:        st.DefaultFrameCount,
		DefaultVideoCompareMode:  st.DefaultVideoCompareMode,
		DefaultWorkers:           st.DefaultWorkers,
		DefaultVideoWorkers:      st.DefaultVideoWorkers,
		DefaultVerifySampledHash: st.DefaultVerifySampled,
		DefaultDeleteMode:        st.DefaultDeleteMode,
		AllowPermanentDelete:     st.AllowPermanentDelete,
		FFmpegPath:               st.FFmpegPath,
		FFprobePath:              st.FFprobePath,
		FFmpegAvailable:          ffmpeg,
		FFprobeAvailable:         ffprobe,
	}, nil
}

func (a *App) SaveSettings(dto SettingsDTO) error {
	st := project.Settings{
		DefaultThreshold:        dto.DefaultThreshold,
		DefaultIncludeVideos:    dto.DefaultIncludeVideos,
		DefaultIncludeTexts:     dto.DefaultIncludeTexts,
		DefaultTextThreshold:    dto.DefaultTextThreshold,
		DefaultTextWorkers:      dto.DefaultTextWorkers,
		DefaultFrameCount:       dto.DefaultFrameCount,
		DefaultVideoCompareMode: dto.DefaultVideoCompareMode,
		DefaultWorkers:          dto.DefaultWorkers,
		DefaultVideoWorkers:     dto.DefaultVideoWorkers,
		DefaultVerifySampled:    dto.DefaultVerifySampledHash,
		DefaultDeleteMode:       dto.DefaultDeleteMode,
		AllowPermanentDelete:    dto.AllowPermanentDelete,
		FFmpegPath:              dto.FFmpegPath,
		FFprobePath:             dto.FFprobePath,
	}
	if err := a.store.SaveSettings(st); err != nil {
		return err
	}
	video.ConfigureTools(st.FFmpegPath, st.FFprobePath)
	return nil
}

func (a *App) StartScan(projectID string) error {
	p, ok := a.store.Get(projectID)
	if !ok {
		return errors.New("项目不存在")
	}
	settings, err := a.store.LoadSettings()
	if err != nil {
		return fmt.Errorf("读取扫描设置失败: %w", err)
	}
	a.mu.Lock()
	if st, exists := a.scans[projectID]; exists && st.status.Running {
		a.mu.Unlock()
		return errors.New("该项目已在扫描中")
	}
	ctx, cancel := context.WithCancel(context.Background())
	st := &scanState{
		cancel: cancel,
		done:   make(chan struct{}),
		status: ScanStatusDTO{
			ProjectID: projectID,
			Running:   true,
			// Sub-second precision identifies one scan run from the next, so the
			// UI can tell two fast scans apart.
			StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
		},
	}
	a.scans[projectID] = st
	a.mu.Unlock()

	if len(p.Paths) > 0 {
		_ = a.touchRecent(p.Paths[0])
	}
	go a.runScan(ctx, p, st, settings.DefaultVerifySampled)
	return nil
}

func (a *App) runScan(ctx context.Context, p project.Project, st *scanState, verifySampledHash bool) {
	defer close(st.done)
	start := time.Now()

	_ = a.media.AllowRoot(a.store.ThumbDir(p.ID))
	_ = a.media.AllowRoot(a.store.FrameDir(p.ID))

	opts := pipeline.Options{
		Paths:             p.Paths,
		CachePath:         a.store.CachePath(p.ID),
		ThumbDir:          a.store.ThumbDir(p.ID),
		FrameDir:          a.store.FrameDir(p.ID),
		Threshold:         p.Threshold,
		Recursive:         p.Recursive,
		IncludeImages:     p.IncludeImages,
		IncludeVideos:     p.IncludeVideos,
		IncludeTexts:      p.IncludeTexts,
		TextThreshold:     p.TextThreshold,
		TextWorkers:       p.TextWorkers,
		Workers:           p.Workers,
		VideoWorkers:      p.VideoWorkers,
		FrameCount:        p.FrameCount,
		VideoCompareMode:  p.VideoCompareMode,
		VerifySampledHash: verifySampledHash,
		Ctx:               ctx,
		ProjectID:         p.ID,
		OnEvent: func(ev progress.Event) {
			a.mu.Lock()
			st.status.Event = &ev
			st.status.Percent = ev.Percent
			st.status.FilesSeen = ev.FilesSeen
			st.status.Errors = ev.Errors
			if ev.Stage == progress.StageCanceled {
				st.status.Canceled = true
				st.status.Running = false
			}
			a.mu.Unlock()
			a.emitEvent("scan:progress", ev)
		},
	}

	result, err := pipeline.Scan(opts)

	a.mu.Lock()
	st.status.Running = false
	st.status.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err != nil {
		st.status.LastError = err.Error()
	}
	st.status.DurationMs = time.Since(start).Milliseconds()
	a.mu.Unlock()

	if err != nil {
		a.emitEvent("scan:error", map[string]any{"projectId": p.ID, "message": err.Error()})
		return
	}
	if result.Canceled {
		a.emitEvent("scan:canceled", map[string]any{"projectId": p.ID})
		return
	}

	summary := project.ComputeSummary(result.Groups)
	if c, cerr := cache.Open(a.store.CachePath(p.ID)); cerr == nil {
		if totals, terr := c.LoadGroupTotals(); terr == nil {
			summary = project.LastSummary{
				Groups:           totals.Groups,
				ExactGroups:      totals.ExactGroups,
				SimilarGroups:    totals.SimilarGroups,
				PendingGroups:    totals.PendingGroups,
				ReclaimableBytes: totals.ReclaimableBytes,
			}
		}
		_ = c.Close()
	}
	summary.FilesSeen = result.Meta.FilesSeen
	_ = a.store.SaveLastScan(p.ID, summary)

	a.emitEvent("scan:finished", FinishedPayload{
		ProjectID:  p.ID,
		Groups:     len(result.Groups),
		FilesSeen:  result.Meta.FilesSeen,
		Errors:     result.Meta.FilesFailed,
		DurationMs: time.Since(start).Milliseconds(),
	})
}

func (a *App) emitEvent(name string, payload any) {
	if a.emit != nil {
		a.emit(name, payload)
	}
}

// logWarning records a degraded path that did not fail the operation. It exists
// so a swallowed error still leaves a trace in the app log instead of vanishing.
func (a *App) logWarning(message string) {
	if a.ctx != nil {
		wailsruntime.LogWarning(a.ctx, message)
	}
}

func (a *App) CancelScan(projectID string) error {
	a.mu.Lock()
	st, ok := a.scans[projectID]
	a.mu.Unlock()
	if !ok || !st.status.Running {
		return errors.New("没有正在进行的扫描")
	}
	st.cancel()
	return nil
}

func (a *App) GetScanStatus(projectID string) ScanStatusDTO {
	a.mu.Lock()
	defer a.mu.Unlock()
	if st, ok := a.scans[projectID]; ok {
		return st.status
	}
	return ScanStatusDTO{ProjectID: projectID, Running: false}
}

// ListScanErrors returns the files the latest scan skipped, so the UI can show
// them after completion instead of failing the whole run.
func (a *App) ListScanErrors(projectID string) ([]ScanErrorDTO, error) {
	c, err := a.openProjectCache(projectID)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	runID, ok, err := c.LatestScanRunID()
	if err != nil {
		return nil, err
	}
	if !ok {
		return []ScanErrorDTO{}, nil
	}
	errs, err := c.LoadErrors(&runID)
	if err != nil {
		return nil, err
	}
	out := make([]ScanErrorDTO, 0, len(errs))
	for _, e := range errs {
		out = append(out, ScanErrorDTO{Path: e.Path, Stage: e.Stage, Kind: e.Kind, Message: e.Message})
	}
	return out, nil
}

// refreshLastSummary recomputes the project's stored summary from current
// group states, keeping the results header and sidebar badge up to date
// after groups are ignored / processed / their files deleted.
func (a *App) refreshLastSummary(projectID string) error {
	if a.store == nil {
		return errors.New("app not ready")
	}
	c, err := a.openProjectCache(projectID)
	if err != nil {
		return err
	}
	totals, err := c.LoadGroupTotals()
	_ = c.Close()
	if err != nil {
		return err
	}
	return a.store.UpdateLastSummary(projectID, project.LastSummary{
		Groups:           totals.Groups,
		ExactGroups:      totals.ExactGroups,
		SimilarGroups:    totals.SimilarGroups,
		PendingGroups:    totals.PendingGroups,
		ReclaimableBytes: totals.ReclaimableBytes,
	})
}

func (a *App) openProjectCache(projectID string) (*cache.Cache, error) {
	if a.store == nil {
		return nil, errors.New("app not ready")
	}
	if _, ok := a.store.Get(projectID); !ok {
		return nil, errors.New("项目不存在")
	}
	return cache.Open(a.store.CachePath(projectID))
}

func groupTypeLabel(t string) string { return model.GroupType(t).Label() }

func (a *App) mediaURLFromPath(path string) string {
	if path == "" || a.media == nil {
		return ""
	}
	return a.media.URL(path)
}

// imageThumbSrc returns an inline data URL so Wails WebView always shows image thumbs.
func imageThumbSrc(path string) string {
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 || len(b) > 2_000_000 {
		return ""
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b)
}

// resolveImageThumb returns a preview src for an image item without generating a thumb.
// Preview is the original file via mediastore (Explorer-style), like the file manager.
// Video/text return empty so the UI can show fixed icons.
func (a *App) resolveImageThumb(projectID string, fileID int64, srcPath, thumbPath string) string {
	if a.media == nil {
		return ""
	}
	if mt, ok := discovery.Classify(srcPath); !ok || mt != model.MediaImage {
		return ""
	}
	if err := a.media.AllowFile(srcPath); err != nil {
		return ""
	}
	if u := a.media.URL(srcPath); u != "" {
		return u
	}
	// Fallback: legacy thumb file from older scans, then small data URL.
	if thumbPath != "" {
		if u := imageThumbSrc(thumbPath); u != "" {
			return u
		}
	}
	if a.store != nil {
		out := filepath.Join(a.store.ThumbDir(projectID), fmt.Sprintf("%d.jpg", fileID))
		return imageThumbSrc(out)
	}
	return ""
}

// mediaKindLabel turns the per-group media counts into the label the results
// table shows. Counts come from the SQL aggregate, so the list does not have to
// classify member paths.
func mediaKindLabel(images, videos, texts int) string {
	switch {
	case images > 0 && videos == 0 && texts == 0:
		return "image"
	case videos > 0 && images == 0 && texts == 0:
		return "video"
	case texts > 0 && images == 0 && videos == 0:
		return "text"
	default:
		return "mixed"
	}
}

func (a *App) ListGroups(projectID string, filter GroupFilter) (GroupPageDTO, error) {
	c, err := a.openProjectCache(projectID)
	if err != nil {
		return GroupPageDTO{}, err
	}
	defer c.Close()
	// The client picks the page, but not the ceiling: "0" used to mean "give me
	// everything", which is what made 500+ group projects unloadable.
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultGroupPageSize
	}
	if limit > maxGroupPageSize {
		limit = maxGroupPageSize
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	q := cache.GroupQuery{
		Status:    filter.Status,
		GroupType: filter.GroupType,
		Search:    filter.Search,
		Sort:      filter.Sort,
		Limit:     limit,
		Offset:    offset,
	}
	rows, err := c.LoadGroupSummaries(q)
	if err != nil {
		return GroupPageDTO{}, err
	}
	total, err := c.CountGroupSummaries(q)
	if err != nil {
		return GroupPageDTO{}, err
	}
	out := make([]GroupListDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, GroupListDTO{
			GroupID:           row.GroupID,
			GroupType:         row.GroupType,
			Confidence:        row.Confidence,
			RecommendedFileID: row.RecommendedFileID,
			Status:            row.Status,
			MemberCount:       row.MemberCount,
			CleanupCount:      row.CleanupCount,
			ReclaimableBytes:  row.ReclaimableBytes,
			TypeLabel:         groupTypeLabel(row.GroupType),
			MediaKind:         mediaKindLabel(row.Images, row.Videos, row.Texts),
		})
	}
	return GroupPageDTO{
		Items:   out,
		Total:   total,
		Limit:   limit,
		Offset:  offset,
		HasMore: offset+len(out) < total,
	}, nil
}

// cleanupCandidates lists the members of one group that are safe to remove
// without asking the user again: explicit cleanup candidates that are not the
// recommended file. Review items stay untouched.
func cleanupCandidates(g *model.ReportGroup) []model.ReportItem {
	out := make([]model.ReportItem, 0, len(g.Items))
	for _, it := range g.Items {
		if !it.IsCleanup() || it.FileID == g.RecommendedFileID {
			continue
		}
		out = append(out, it)
	}
	return out
}

// expandCandidatePaths resolves group IDs to their explicit cleanup candidates so
// bulk delete can run without the UI collecting paths first.
func (a *App) expandCandidatePaths(projectID string, groupIDs []int64) ([]string, []int64, error) {
	c, err := a.openProjectCache(projectID)
	if err != nil {
		return nil, nil, err
	}
	defer c.Close()
	groups, err := c.LoadReportGroups()
	if err != nil {
		return nil, nil, err
	}
	want := make(map[int64]bool, len(groupIDs))
	for _, id := range groupIDs {
		want[id] = true
	}
	var paths []string
	var fileIDs []int64
	for _, g := range groups {
		if !want[g.GroupID] {
			continue
		}
		for _, it := range cleanupCandidates(&g) {
			paths = append(paths, it.Path)
			fileIDs = append(fileIDs, it.FileID)
		}
	}
	return paths, fileIDs, nil
}

func (a *App) GetGroup(projectID string, groupID int64) (GroupDetailDTO, error) {
	c, err := a.openProjectCache(projectID)
	if err != nil {
		return GroupDetailDTO{}, err
	}
	defer c.Close()
	g, found, err := c.LoadGroup(groupID)
	if err != nil {
		return GroupDetailDTO{}, err
	}
	if !found {
		return GroupDetailDTO{}, errors.New("组不存在")
	}
	_ = a.media.AllowRoot(a.store.ThumbDir(projectID))
	_ = a.media.AllowRoot(a.store.FrameDir(projectID))

	status := string(g.Status)
	if status == "" {
		status = string(model.GroupPending)
	}
	detail := GroupDetailDTO{
		GroupID:           g.GroupID,
		GroupType:         string(g.GroupType),
		TypeLabel:         groupTypeLabel(string(g.GroupType)),
		Confidence:        g.Confidence,
		RecommendedFileID: g.RecommendedFileID,
		Status:            status,
		Items:             make([]ItemDTO, 0, len(g.Items)),
	}
	var recImageHash string
	if g.RecommendedFileID > 0 {
		if hs, err := c.LoadPerceptualHashes(g.RecommendedFileID, "image_phash"); err == nil && len(hs) > 0 {
			recImageHash = hs[0]
		}
	}
	for _, it := range g.Items {
		item := ItemDTO{
			FileID:       it.FileID,
			Path:         it.Path,
			Action:       string(it.Action),
			Similarity:   it.Similarity,
			QualityScore: it.QualityScore,
			SizeBytes:    it.SizeBytes,
			// Original image via the local media server, with legacy thumb fallback.
			ThumbURL:      a.resolveImageThumb(projectID, it.FileID, it.Path, it.ThumbPath),
			IsRecommended: it.FileID == g.RecommendedFileID,
			Reasons:       it.Reasons,
		}
		if _, err := os.Stat(it.Path); err == nil {
			item.Exists = true
		}
		if m, ok, _ := c.LoadImageMetadata(it.FileID); ok && m.IsReadable {
			item.Width = m.Width
			item.Height = m.Height
			item.MediaType = "image"
			if hs, err := c.LoadPerceptualHashes(it.FileID, "image_phash"); err == nil && len(hs) > 0 {
				item.MediaHashHex = hs[0]
				if !item.IsRecommended && recImageHash != "" {
					if d, err := hashfile.HammingDistance(recImageHash, hs[0]); err == nil {
						item.HammingDistance = &d
					}
				}
			}
		} else if m, ok, _ := c.LoadVideoMetadata(it.FileID); ok && m.IsReadable {
			item.Width = m.Width
			item.Height = m.Height
			item.DurationMs = m.DurationMs
			item.MediaType = "video"
		} else {
			lower := strings.ToLower(it.Path)
			if strings.HasSuffix(lower, ".txt") {
				item.MediaType = "text"
			} else if strings.HasSuffix(lower, ".mp4") || strings.HasSuffix(lower, ".mov") || strings.HasSuffix(lower, ".mkv") {
				item.MediaType = "video"
			} else {
				item.MediaType = "image"
			}
		}
		detail.Items = append(detail.Items, item)
		if it.IsCleanup() {
			detail.ReclaimableBytes += it.SizeBytes
		}
	}
	return detail, nil
}

func (a *App) SetGroupStatus(projectID string, groupID int64, status string) error {
	st := model.GroupStatus(status)
	if st != model.GroupPending && st != model.GroupIgnored && st != model.GroupProcessed {
		return errors.New("invalid status")
	}
	c, err := a.openProjectCache(projectID)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.SetGroupStatus(groupID, st); err != nil {
		return err
	}
	// The status itself is stored; a stale header is a degraded display, not a
	// failed action. Log it instead of turning it into an error the caller would
	// read as "nothing was saved".
	if err := a.refreshLastSummary(projectID); err != nil {
		a.logWarning("刷新项目汇总失败: " + err.Error())
	}
	return nil
}

func (a *App) SetRecommended(projectID string, groupID int64, fileID int64) error {
	c, err := a.openProjectCache(projectID)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.UpdateRecommended(groupID, fileID); err != nil {
		return err
	}
	if err := a.refreshLastSummary(projectID); err != nil {
		a.logWarning("刷新项目汇总失败: " + err.Error())
	}
	return nil
}

func (a *App) GetThumbURL(projectID string, fileID int64) string {
	if a.store == nil || a.media == nil {
		return ""
	}
	_ = a.media.AllowRoot(a.store.ThumbDir(projectID))
	return a.mediaURLFromPath(filepath.Join(a.store.ThumbDir(projectID), fmt.Sprintf("%d.jpg", fileID)))
}

func (a *App) GetFrameURLs(projectID string, fileID int64) []string {
	if a.store == nil || a.media == nil {
		return []string{}
	}
	_ = a.media.AllowRoot(a.store.FrameDir(projectID))
	c, err := a.openProjectCache(projectID)
	if err != nil {
		return []string{}
	}
	defer c.Close()
	paths, err := c.LoadFramePreviews(fileID)
	if err != nil || len(paths) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if u := a.mediaURLFromPath(p); u != "" {
			out = append(out, u)
		}
	}
	return out
}

func (a *App) DeleteFiles(req DeleteRequestDTO) (DeleteResultDTO, error) {
	if a.store == nil {
		return DeleteResultDTO{}, errors.New("app not ready")
	}
	st, err := a.store.LoadSettings()
	if err != nil {
		return DeleteResultDTO{}, err
	}
	mode := req.Mode
	if mode == "" {
		mode = st.DefaultDeleteMode
	}
	if mode == "permanent" && !st.AllowPermanentDelete {
		return DeleteResultDTO{}, errors.New("永久删除未在设置中开启")
	}
	if mode != "recycle" && mode != "permanent" {
		return DeleteResultDTO{}, errors.New("invalid delete mode")
	}
	// The server decides what may be deleted. Targets are resolved from the
	// results database by group and file id, then compared against the snapshot
	// the scan recorded; a path sent by the caller only names a member, it never
	// authorizes a filesystem operation.
	targets, rejected, err := a.resolveDeleteTargets(req)
	if err != nil {
		return DeleteResultDTO{}, err
	}
	if len(targets) == 0 && len(rejected) > 0 {
		// Nothing survived the pre-checks. Report why instead of returning an
		// error: these are per-file outcomes the UI already knows how to show.
		return DeleteResultDTO{Failed: rejected, Mode: mode}, nil
	}
	paths := make([]string, 0, len(targets))
	for _, target := range targets {
		paths = append(paths, target.path)
	}

	res := ops.DeleteFiles(ops.Request{Mode: mode, Paths: paths, GroupIDs: req.GroupIDs})
	out := DeleteResultDTO{
		Succeeded: res.Succeeded,
		Failed:    make([]DeleteFailureDTO, 0, len(res.Failed)+len(rejected)),
		Mode:      res.Mode,
	}
	out.Failed = append(out.Failed, rejected...)
	// Carry the identity back with every failure so the UI can retry exactly the
	// files that are still on disk.
	byPath := make(map[string]deleteTarget, len(targets))
	for _, target := range targets {
		byPath[target.path] = target
	}
	for _, failure := range res.Failed {
		entry := DeleteFailureDTO{Path: failure.Path, Message: failure.Message}
		if target, ok := byPath[failure.Path]; ok {
			entry.FileID = target.fileID
			entry.GroupID = target.groupID
		}
		out.Failed = append(out.Failed, entry)
	}

	if req.ProjectID != "" {
		a.applyDeleteBookkeeping(req, res, mode, &out)
		if err := a.refreshLastSummary(req.ProjectID); err != nil {
			out.MetadataErrors = append(out.MetadataErrors, "刷新项目汇总失败："+err.Error())
		}
	}

	a.emitEvent("files:deleted", map[string]any{
		"paths": out.Succeeded,
		"mode":  out.Mode,
	})
	return out, nil
}

// applyDeleteBookkeeping records what happened to each file and re-derives the
// status of every group the request touched. A group becomes "processed" only
// when nothing it still wanted to delete is left: judging by "some file still
// exists" would let a failed candidate disappear from the pending list together
// with its group.
func (a *App) applyDeleteBookkeeping(req DeleteRequestDTO, res ops.Result, mode string, out *DeleteResultDTO) {
	succeeded := make(map[string]bool, len(res.Succeeded))
	for _, path := range res.Succeeded {
		succeeded[path] = true
	}
	failMsg := make(map[string]string, len(res.Failed))
	for _, failure := range res.Failed {
		failMsg[failure.Path] = failure.Message
	}
	c, err := a.openProjectCache(req.ProjectID)
	if err != nil {
		out.MetadataErrors = append(out.MetadataErrors, "打开结果缓存失败："+err.Error())
		return
	}
	defer c.Close()
	// Deletes write status changes and the operation log; fold them back now
	// rather than on some later read-only request.
	defer func() { _ = c.Checkpoint() }()
	// Only groups named in the request can change; reading just those avoids a
	// full report rebuild per delete and keeps the path matching local.
	groups := make([]model.ReportGroup, 0, len(req.GroupIDs))
	seenGroup := make(map[int64]bool, len(req.GroupIDs))
	for _, id := range req.GroupIDs {
		if seenGroup[id] {
			continue
		}
		seenGroup[id] = true
		group, found, err := c.LoadGroup(id)
		if err != nil {
			out.MetadataErrors = append(out.MetadataErrors, fmt.Sprintf("读取组 #%d 失败：%v", id, err))
			continue
		}
		if found {
			groups = append(groups, group)
		}
	}
	for i := range groups {
		g := &groups[i]
		recs := make([]cache.DeleteOpItem, 0, len(g.Items))
		deleted := 0
		for _, it := range g.Items {
			if succeeded[it.Path] {
				recs = append(recs, cache.DeleteOpItem{FileID: it.FileID, Path: it.Path, OK: true})
				deleted++
				continue
			}
			if msg, failed := failMsg[it.Path]; failed {
				recs = append(recs, cache.DeleteOpItem{FileID: it.FileID, Path: it.Path, Message: msg})
			}
		}
		if len(recs) > 0 {
			if err := c.RecordDeleteOps(mode, g.GroupID, recs); err != nil {
				out.MetadataErrors = append(out.MetadataErrors, fmt.Sprintf("组 #%d 的删除记录写入失败：%v", g.GroupID, err))
			}
		}
		if deleted == 0 {
			continue
		}
		// Keepers that were never touched stay alive; only probe leftovers that
		// might already have been missing before this delete.
		alive, remaining := 0, 0
		for _, it := range g.Items {
			if succeeded[it.Path] {
				continue
			}
			if _, err := os.Stat(it.Path); err != nil {
				continue
			}
			alive++
			if it.IsCleanup() {
				remaining++
			}
		}
		if alive >= 1 && remaining == 0 && (!g.GroupType.IsTextAnalysis() || alive == 1) {
			if err := c.SetGroupStatus(g.GroupID, model.GroupProcessed); err != nil {
				out.MetadataErrors = append(out.MetadataErrors, fmt.Sprintf("组 #%d 的状态更新失败：%v", g.GroupID, err))
			} else {
				out.MarkedProcessed = append(out.MarkedProcessed, g.GroupID)
			}
		}
	}
	if len(res.Succeeded) == 0 {
		return
	}
	// Drop the rows of the files that are gone, in every group they belonged to,
	// and repair the results. Without this the list keeps advertising candidates
	// that were already removed, and a group that lost members keeps a member
	// count nobody can act on.
	if _, err := c.DeleteFilesByPaths(res.Succeeded); err != nil {
		out.MetadataErrors = append(out.MetadataErrors, "文件已删除，但更新结果失败，请重新扫描："+err.Error())
		return
	}
	if err := c.PruneReportGroups(); err != nil {
		out.MetadataErrors = append(out.MetadataErrors, "文件已删除，但修复结果失败，请重新扫描："+err.Error())
	}
}

// deleteTarget is one member the server resolved for deletion. Its path comes
// from the results database rather than from the caller, so a request can only
// address files that the scan actually put into the named group.
type deleteTarget struct {
	groupID int64
	fileID  int64
	path    string
}

// resolveDeleteTargets turns a delete request into concrete files. It is the only
// place that decides which paths may be touched, and it refuses anything the
// results no longer describe:
//
//   - unknown groups (a stale or invented group id must not act as a wildcard),
//   - members that do not belong to the named groups,
//   - deleting a group's recommended file,
//   - removing every file of a group,
//   - files whose size / timestamp / identity differ from the scan snapshot
//     (they may have been replaced by an unrelated file since the scan).
//
// The last two categories differ in blast radius. A group-level problem is an
// error: the request was wrong. A single file whose snapshot no longer matches is
// returned as `rejected` so the other files still get deleted and the UI shows
// exactly which one was skipped and why — aborting a 200-group batch because one
// file was touched since the scan is not safer, only slower.
func (a *App) resolveDeleteTargets(req DeleteRequestDTO) ([]deleteTarget, []DeleteFailureDTO, error) {
	if req.ProjectID == "" {
		return nil, nil, errors.New("删除请求缺少项目标识，请刷新结果页后重试")
	}
	if len(req.GroupIDs) == 0 {
		return nil, nil, errors.New("删除请求缺少组信息，请刷新结果页后重试")
	}
	c, err := a.openProjectCache(req.ProjectID)
	if err != nil {
		return nil, nil, err
	}
	defer c.Close()
	// Only the groups the request names are read: a delete of a handful of groups
	// must not load the whole project's results.
	wanted := make([]*model.ReportGroup, 0, len(req.GroupIDs))
	var rejected []DeleteFailureDTO
	seenGroup := make(map[int64]bool, len(req.GroupIDs))
	for _, id := range req.GroupIDs {
		if seenGroup[id] {
			continue
		}
		seenGroup[id] = true
		g, found, err := c.LoadGroup(id)
		if err != nil {
			return nil, nil, err
		}
		if !found {
			// A retry can outlive the group: a rescan or another delete may have
			// pruned it. Report it as a failure for that group and let the rest of
			// the batch run, instead of blocking every other file behind it.
			rejected = append(rejected, DeleteFailureDTO{
				Path:    fmt.Sprintf("组 #%d", id),
				Message: "该组已不在当前结果中（可能已被删除或已重新扫描），无需再删除",
				GroupID: id,
			})
			continue
		}
		wanted = append(wanted, &g)
	}
	if len(wanted) == 0 {
		return nil, rejected, nil
	}
	targets, unresolved, err := selectDeleteTargets(wanted, req)
	if err != nil {
		return nil, nil, err
	}
	for _, id := range unresolved {
		// Nothing is deleted for an id we cannot place in a named group; report it
		// instead of failing the batch. FileID stays 0 so it is not offered for
		// retry — retrying the same id would fail the same way.
		rejected = append(rejected, DeleteFailureDTO{
			Path:    fmt.Sprintf("文件 #%d", id),
			Message: "该文件不属于所选组，或其所在组已不在当前结果中，请刷新后重新确认",
		})
	}
	if err := checkGroupInvariants(wanted, targets); err != nil {
		return nil, nil, err
	}
	kept := targets[:0]
	for _, target := range targets {
		reject, err := checkDeleteSnapshot(c, target)
		if err != nil {
			return nil, nil, err
		}
		if reject != "" {
			rejected = append(rejected, DeleteFailureDTO{
				Path: target.path, Message: reject, FileID: target.fileID, GroupID: target.groupID,
			})
			continue
		}
		kept = append(kept, target)
	}
	return kept, rejected, nil
}

// selectDeleteTargets picks the members a request addresses. Explicit file ids
// win over paths; paths are only matched against members of the named groups, so
// a path that was never stored cannot become deletable. With neither, the request
// means the groups' explicit cleanup candidates (bulk delete from the list).
func selectDeleteTargets(groups []*model.ReportGroup, req DeleteRequestDTO) ([]deleteTarget, []int64, error) {
	switch {
	case len(req.FileIDs) > 0:
		wanted := make(map[int64]bool, len(req.FileIDs))
		for _, id := range req.FileIDs {
			wanted[id] = true
		}
		used := make(map[int64]bool, len(req.FileIDs))
		var targets []deleteTarget
		for _, g := range groups {
			for _, it := range g.Items {
				if !wanted[it.FileID] || used[it.FileID] {
					continue
				}
				used[it.FileID] = true
				targets = append(targets, deleteTarget{groupID: g.GroupID, fileID: it.FileID, path: it.Path})
			}
		}
		var unresolved []int64
		for _, id := range req.FileIDs {
			if !used[id] {
				unresolved = append(unresolved, id)
			}
		}
		return targets, unresolved, nil
	case len(req.Paths) > 0:
		byPath := make(map[string]deleteTarget, len(req.Paths))
		for _, g := range groups {
			for _, it := range g.Items {
				if it.Path == "" {
					continue
				}
				if _, exists := byPath[it.Path]; exists {
					continue
				}
				byPath[it.Path] = deleteTarget{groupID: g.GroupID, fileID: it.FileID, path: it.Path}
			}
		}
		targets := make([]deleteTarget, 0, len(req.Paths))
		seen := make(map[string]bool, len(req.Paths))
		for _, path := range req.Paths {
			target, ok := byPath[path]
			if !ok {
				return nil, nil, fmt.Errorf("路径不在所选组中，请刷新结果页后重试：%s", path)
			}
			if seen[path] {
				continue
			}
			seen[path] = true
			targets = append(targets, target)
		}
		return targets, nil, nil
	default:
		var targets []deleteTarget
		for _, g := range groups {
			for _, it := range cleanupCandidates(g) {
				targets = append(targets, deleteTarget{groupID: g.GroupID, fileID: it.FileID, path: it.Path})
			}
		}
		if len(targets) == 0 {
			return nil, nil, errors.New("没有选中任何文件（所选组没有明确的清理候选）")
		}
		return targets, nil, nil
	}
}

// checkGroupInvariants enforces the rules that keep a group actionable after the
// delete: the recommendation survives, a comparison reference still exists, and
// removing one representative of an equivalence class does not silently strand
// the copies that were justified by it.
func checkGroupInvariants(groups []*model.ReportGroup, targets []deleteTarget) error {
	chosen := make(map[int64]map[int64]bool, len(groups))
	for _, target := range targets {
		if chosen[target.groupID] == nil {
			chosen[target.groupID] = map[int64]bool{}
		}
		chosen[target.groupID][target.fileID] = true
	}
	for _, g := range groups {
		selected := chosen[g.GroupID]
		if selected[g.RecommendedFileID] {
			return fmt.Errorf("组 #%d 的推荐保留文件不能直接删除，请先把其他文件设为推荐", g.GroupID)
		}
		if g.GroupType.IsTextAnalysis() {
			// A mixed group can contain several different books/editions. A
			// remaining unrelated reference cannot stand in for a missing copy
			// that justified cleaning one of the group's equality classes.
			for _, it := range g.Items {
				if it.IsCleanup() || selected[it.FileID] {
					continue
				}
				if _, err := os.Stat(it.Path); err != nil {
					return fmt.Errorf("组 #%d 的核对文件已不可访问，请重新扫描后再删除：%s", g.GroupID, it.Path)
				}
			}
		}
		alive := 0
		for _, it := range g.Items {
			if selected[it.FileID] {
				continue
			}
			if _, err := os.Stat(it.Path); err == nil {
				alive++
			}
		}
		if alive < 1 {
			return fmt.Errorf("组 #%d 删除后将没有任何保留文件，已阻止", g.GroupID)
		}
	}
	return nil
}

// checkDeleteSnapshot compares the file on disk with what the scan recorded. The
// first return value is a per-file reason to skip this one file; the error is
// reserved for infrastructure problems (the database cannot be read), which do
// abort the request.
func checkDeleteSnapshot(c *cache.Cache, target deleteTarget) (string, error) {
	snap, ok, err := c.LoadFileSnapshot(target.fileID)
	if err != nil {
		return "", err
	}
	if !ok {
		return fmt.Sprintf("组 #%d 的文件不在扫描结果中，请重新扫描后再删除：%s", target.groupID, target.path), nil
	}
	size, mtimeNs, inode, device, err := fsutil.SafeStat(target.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return fmt.Sprintf("无法读取文件状态，请检查权限后重试：%s（%v）", target.path, err), nil
	}
	if size != snap.SizeBytes || mtimeNs != snap.MTimeNs {
		return fmt.Sprintf("文件在扫描后已被修改，请重新扫描后再保留或删除：%s", target.path), nil
	}
	// Identity is only compared when both sides know it: Windows has no stable
	// equivalent here, and caches written before inode/device were recorded
	// carry zeros.
	if snap.Inode != 0 && inode != 0 && uint64(snap.Inode) != inode {
		return fmt.Sprintf("该路径已指向另一个文件，请重新扫描后再删除：%s", target.path), nil
	}
	if snap.Device != 0 && device != 0 && uint64(snap.Device) != device {
		return fmt.Sprintf("该路径已指向另一个文件，请重新扫描后再删除：%s", target.path), nil
	}
	return "", nil
}

func (a *App) ExportReport(projectID, format, scope string) (string, error) {
	c, err := a.openProjectCache(projectID)
	if err != nil {
		return "", err
	}
	defer c.Close()
	groups, err := c.LoadReportGroups()
	if err != nil {
		return "", err
	}
	if scope == "pending" || scope == "" {
		filtered := make([]model.ReportGroup, 0, len(groups))
		for _, g := range groups {
			st := g.Status
			if st == "" {
				st = model.GroupPending
			}
			if st == model.GroupPending {
				filtered = append(filtered, g)
			}
		}
		groups = filtered
	}
	errs, _ := c.LoadErrors(nil)
	meta := model.ScanMeta{Paths: []string{}, CachePath: a.store.CachePath(projectID)}

	dir := a.store.ReportDir(projectID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	stamp := time.Now().Format("20060102_150405")
	var content string
	var name string
	switch strings.ToLower(format) {
	case "json":
		content, err = report.RenderJSON(groups, errs, meta)
		name = fmt.Sprintf("dedupe_report_%s.json", stamp)
	case "html":
		content = report.RenderHTML(groups, errs, meta, a.store.ThumbDir(projectID))
		name = fmt.Sprintf("dedupe_report_%s.html", stamp)
	case "txt", "text":
		content = report.RenderText(groups, errs, meta)
		name = fmt.Sprintf("dedupe_report_%s.txt", stamp)
	default:
		return "", fmt.Errorf("unsupported format: %s", format)
	}
	if err != nil {
		return "", err
	}
	out := filepath.Join(dir, name)
	if err := report.WriteFile(out, content); err != nil {
		return "", err
	}
	return out, nil
}

const (
	eventUpdateAvailable = "update:available"
	eventUpdateProgress  = "update:progress"
	eventUpdateDone      = "update:done"
	eventUpdateFailed    = "update:failed"
)

// checkUpdateOnStartup 启动 2 秒后异步检查更新，若有可用更新则缓存并推送事件。
func (a *App) checkUpdateOnStartup() {
	time.Sleep(2 * time.Second)
	if a.ctx == nil || a.updater == nil {
		return
	}
	info, err := a.updater.CheckUpdate()
	if err != nil {
		wailsruntime.LogDebug(a.ctx, "启动自动检查更新失败: "+err.Error())
		return
	}
	if info != nil && info.HasUpdate {
		a.mu.Lock()
		a.pendingUpdate = info
		a.mu.Unlock()
		a.emitEvent(eventUpdateAvailable, info)
	}
}

// GetAppVersion 返回当前软件版本号。
func (a *App) GetAppVersion() string {
	return version.GetVersion()
}

// CheckUpdate 手动触发检查更新。
func (a *App) CheckUpdate() (*updater.UpdateInfo, error) {
	if a.updater == nil {
		return nil, errors.New("更新器未初始化")
	}
	info, err := a.updater.CheckUpdate()
	if err == nil && info != nil && info.HasUpdate {
		a.mu.Lock()
		a.pendingUpdate = info
		a.mu.Unlock()
	}
	return info, err
}

// GetPendingUpdate 返回启动检查得到的更新信息，避免前端监听事件前错过通知。
func (a *App) GetPendingUpdate() *updater.UpdateInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pendingUpdate == nil {
		return nil
	}
	info := *a.pendingUpdate
	return &info
}

// DownloadUpdate 开始下载更新包，支持可选的国内镜像加速。
func (a *App) DownloadUpdate(useProxy bool) error {
	if a.updater == nil {
		return errors.New("更新器未初始化")
	}
	go func() {
		err := a.updater.StartDownload(useProxy, func(prog updater.DownloadProgress) {
			a.emitEvent(eventUpdateProgress, prog)
		})
		if err != nil {
			a.emitEvent(eventUpdateFailed, map[string]any{"message": err.Error()})
			return
		}
		a.emitEvent(eventUpdateDone, nil)
	}()
	return nil
}

// CancelUpdateDownload 取消进行中的更新下载。
func (a *App) CancelUpdateDownload() {
	if a.updater != nil {
		a.updater.CancelDownload()
	}
}

// ApplyUpdateAndRestart 执行安装更新并重启应用。
func (a *App) ApplyUpdateAndRestart() error {
	a.mu.Lock()
	for _, st := range a.scans {
		if st != nil && st.status.Running {
			a.mu.Unlock()
			return errors.New("当前正在扫描文件，请等待扫描完成后再更新")
		}
	}
	a.mu.Unlock()

	if a.updater == nil {
		return errors.New("更新器未初始化")
	}
	if err := a.updater.ApplyAndRestart(); err != nil {
		return err
	}

	// 延迟 300ms 让后台更新脚本拉起后，退出当前主程序
	go func() {
		time.Sleep(300 * time.Millisecond)
		wailsruntime.Quit(a.ctx)
	}()
	return nil
}

// OpenURL 在系统默认浏览器中打开指定链接。
func (a *App) OpenURL(targetURL string) {
	if a.ctx != nil && targetURL != "" {
		wailsruntime.BrowserOpenURL(a.ctx, targetURL)
	}
}

// CleanupUpdater 在应用退出时取消下载并清理临时目录。
func (a *App) CleanupUpdater() {
	if a.updater == nil {
		return
	}
	a.updater.CancelDownload()
	a.updater.CleanTempDir()
}

func (a *App) OpenPath(path string) error {
	return ops.OpenPath(path)
}

func (a *App) RevealInFolder(path string) error {
	return ops.RevealInFolder(path)
}

func (a *App) CopyToClipboard(text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("empty text")
	}
	switch runtime.GOOS {
	case "darwin":
		return runStdin("pbcopy", text)
	case "windows":
		return runStdin("clip", text)
	default:
		return errors.New("clipboard not supported on this platform")
	}
}

func runStdin(name, text string) error {
	cmd := exec.Command(name)
	cmd.Stdin = strings.NewReader(text)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("%v: %s", err, msg)
		}
		return err
	}
	return nil
}

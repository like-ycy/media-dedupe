package appapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	CreatedAt     string              `json:"createdAt"`
	UpdatedAt     string              `json:"updatedAt"`
	Paths         []string            `json:"paths"`
	Recursive     bool                `json:"recursive"`
	Threshold     float64             `json:"threshold"`
	IncludeImages bool                `json:"includeImages"`
	IncludeVideos bool                `json:"includeVideos"`
	IncludeTexts  bool                `json:"includeTexts"`
	TextThreshold float64             `json:"textThreshold"`
	TextWorkers   int                 `json:"textWorkers"`
	FrameCount    int                 `json:"frameCount"`
	Workers       int                 `json:"workers"`
	VideoWorkers  int                 `json:"videoWorkers"`
	EnableThumbs  bool                `json:"enableThumbs"`
	LastScanAt    *string             `json:"lastScanAt"`
	LastSummary   project.LastSummary `json:"lastSummary"`
	Scanning      bool                `json:"scanning"`
	FFmpegReady   bool                `json:"ffmpegReady"`
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

type GroupListDTO struct {
	GroupID           int64   `json:"groupId"`
	GroupType         string  `json:"groupType"`
	Confidence        float64 `json:"confidence"`
	RecommendedFileID int64   `json:"recommendedFileId"`
	Status            string  `json:"status"`
	MemberCount       int     `json:"memberCount"`
	ReclaimableBytes  int64   `json:"reclaimableBytes"`
	CoverThumbURL     string  `json:"coverThumbUrl"`
	TypeLabel         string  `json:"typeLabel"`
}

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
	DefaultThreshold     float64 `json:"defaultThreshold"`
	DefaultIncludeVideos bool    `json:"defaultIncludeVideos"`
	DefaultIncludeTexts  bool    `json:"defaultIncludeTexts"`
	DefaultTextThreshold float64 `json:"defaultTextThreshold"`
	DefaultTextWorkers   int     `json:"defaultTextWorkers"`
	DefaultFrameCount    int     `json:"defaultFrameCount"`
	DefaultWorkers       int     `json:"defaultWorkers"`
	DefaultVideoWorkers  int     `json:"defaultVideoWorkers"`
	DefaultDeleteMode    string  `json:"defaultDeleteMode"`
	AllowPermanentDelete bool    `json:"allowPermanentDelete"`
	FFmpegPath           string  `json:"ffmpegPath"`
	FFprobePath          string  `json:"ffprobePath"`
	FFmpegAvailable      bool    `json:"ffmpegAvailable"`
	FFprobeAvailable     bool    `json:"ffprobeAvailable"`
}

type DeleteRequestDTO struct {
	ProjectID string   `json:"projectId"`
	Mode      string   `json:"mode"`
	Paths     []string `json:"paths"`
	GroupIDs  []int64  `json:"groupIds"`
	FileIDs   []int64  `json:"fileIds"`
}

type DeleteResultDTO struct {
	Succeeded       []string         `json:"succeeded"`
	Failed          []ops.FailedItem `json:"failed"`
	Mode            string           `json:"mode"`
	MarkedProcessed []int64          `json:"markedProcessed"`
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
	Limit     int    `json:"limit"`
	Offset    int    `json:"offset"`
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
	a.media = mediastore.New()
	_ = a.media.AllowRoot(filepath.Join(store.Root(), "projects"))
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
	_, scanning := a.scans[p.ID]
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
		FrameCount:   p.FrameCount,
		Workers:      p.Workers,
		VideoWorkers: p.VideoWorkers,
		EnableThumbs: p.EnableThumbs,
		LastScanAt:   p.LastScanAt,
		LastSummary:  p.LastSummary,
		Scanning:     scanning,
		FFmpegReady:  ffmpeg,
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
	// EnableThumbs defaults true when omitted zero-value is false;
	// callers should set true explicitly; for create we honor input.
	if !input.EnableThumbs && !input.IncludeImages && !input.IncludeVideos {
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
		DefaultThreshold:     st.DefaultThreshold,
		DefaultIncludeVideos: st.DefaultIncludeVideos,
		DefaultIncludeTexts:  st.DefaultIncludeTexts,
		DefaultTextThreshold: st.DefaultTextThreshold,
		DefaultTextWorkers:   st.DefaultTextWorkers,
		DefaultFrameCount:    st.DefaultFrameCount,
		DefaultWorkers:       st.DefaultWorkers,
		DefaultVideoWorkers:  st.DefaultVideoWorkers,
		DefaultDeleteMode:    st.DefaultDeleteMode,
		AllowPermanentDelete: st.AllowPermanentDelete,
		FFmpegPath:           st.FFmpegPath,
		FFprobePath:          st.FFprobePath,
		FFmpegAvailable:      ffmpeg,
		FFprobeAvailable:     ffprobe,
	}, nil
}

func (a *App) SaveSettings(dto SettingsDTO) error {
	st := project.Settings{
		DefaultThreshold:     dto.DefaultThreshold,
		DefaultIncludeVideos: dto.DefaultIncludeVideos,
		DefaultIncludeTexts:  dto.DefaultIncludeTexts,
		DefaultTextThreshold: dto.DefaultTextThreshold,
		DefaultTextWorkers:   dto.DefaultTextWorkers,
		DefaultFrameCount:    dto.DefaultFrameCount,
		DefaultWorkers:       dto.DefaultWorkers,
		DefaultVideoWorkers:  dto.DefaultVideoWorkers,
		DefaultDeleteMode:    dto.DefaultDeleteMode,
		AllowPermanentDelete: dto.AllowPermanentDelete,
		FFmpegPath:           dto.FFmpegPath,
		FFprobePath:          dto.FFprobePath,
	}
	return a.store.SaveSettings(st)
}

func (a *App) StartScan(projectID string) error {
	p, ok := a.store.Get(projectID)
	if !ok {
		return errors.New("项目不存在")
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
			StartedAt: time.Now().UTC().Format(time.RFC3339),
		},
	}
	a.scans[projectID] = st
	a.mu.Unlock()

	if len(p.Paths) > 0 {
		_ = a.touchRecent(p.Paths[0])
	}
	go a.runScan(ctx, p, st)
	return nil
}

func (a *App) runScan(ctx context.Context, p project.Project, st *scanState) {
	defer close(st.done)
	start := time.Now()

	_ = a.media.AllowRoot(a.store.ThumbDir(p.ID))
	_ = a.media.AllowRoot(a.store.FrameDir(p.ID))

	opts := pipeline.Options{
		Paths:         p.Paths,
		CachePath:     a.store.CachePath(p.ID),
		ThumbDir:      a.store.ThumbDir(p.ID),
		FrameDir:      a.store.FrameDir(p.ID),
		Threshold:     p.Threshold,
		Recursive:     p.Recursive,
		IncludeImages: p.IncludeImages,
		IncludeVideos: p.IncludeVideos,
		IncludeTexts:  p.IncludeTexts,
		TextThreshold: p.TextThreshold,
		TextWorkers:   p.TextWorkers,
		Workers:       p.Workers,
		VideoWorkers:  p.VideoWorkers,
		FrameCount:    p.FrameCount,
		EnableThumbs:  p.EnableThumbs,
		Ctx:           ctx,
		ProjectID:     p.ID,
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
	st.status.FinishedAt = time.Now().UTC().Format(time.RFC3339)
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
	summary.FilesSeen = result.Meta.FilesSeen
	if c, cerr := cache.Open(a.store.CachePath(p.ID)); cerr == nil {
		if groups, lerr := c.LoadReportGroups(); lerr == nil {
			summary = project.ComputeSummary(groups)
			summary.FilesSeen = result.Meta.FilesSeen
		}
		_ = c.Close()
	}
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

func (a *App) openProjectCache(projectID string) (*cache.Cache, error) {
	if a.store == nil {
		return nil, errors.New("app not ready")
	}
	if _, ok := a.store.Get(projectID); !ok {
		return nil, errors.New("项目不存在")
	}
	return cache.Open(a.store.CachePath(projectID))
}

func groupTypeLabel(t string) string {
	switch t {
	case "exact":
		return "精确重复"
	case "similar_image":
		return "视觉近似图片"
	case "similar_video":
		return "视觉近似视频"
	case "similar_text":
		return "文本近似重复"
	default:
		return t
	}
}

func (a *App) mediaURLFromPath(path string) string {
	if path == "" || a.media == nil {
		return ""
	}
	return a.media.URL(path)
}

func (a *App) ListGroups(projectID string, filter GroupFilter) ([]GroupListDTO, error) {
	c, err := a.openProjectCache(projectID)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	groups, err := c.LoadReportGroups()
	if err != nil {
		return nil, err
	}
	_ = a.media.AllowRoot(a.store.ThumbDir(projectID))
	_ = a.media.AllowRoot(a.store.FrameDir(projectID))

	if filter.Status == "" {
		filter.Status = string(model.GroupPending)
	}
	out := make([]GroupListDTO, 0, len(groups))
	for _, g := range groups {
		status := string(g.Status)
		if status == "" {
			status = string(model.GroupPending)
		}
		if filter.Status != "all" && status != filter.Status {
			continue
		}
		if filter.GroupType != "" && string(g.GroupType) != filter.GroupType {
			continue
		}
		if filter.Search != "" {
			hit := false
			for _, it := range g.Items {
				if strings.Contains(strings.ToLower(it.Path), strings.ToLower(filter.Search)) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		var reclaim int64
		var cover string
		for _, it := range g.Items {
			if g.RecommendedFileID > 0 && it.FileID != g.RecommendedFileID {
				reclaim += it.SizeBytes
			}
			if cover == "" && it.ThumbPath != "" {
				cover = a.mediaURLFromPath(it.ThumbPath)
			}
		}
		if cover == "" && len(g.Items) > 0 {
			cover = a.mediaURLFromPath(filepath.Join(a.store.ThumbDir(projectID), fmt.Sprintf("%d.jpg", g.Items[0].FileID)))
		}
		out = append(out, GroupListDTO{
			GroupID:           g.GroupID,
			GroupType:         string(g.GroupType),
			Confidence:        g.Confidence,
			RecommendedFileID: g.RecommendedFileID,
			Status:            status,
			MemberCount:       len(g.Items),
			ReclaimableBytes:  reclaim,
			CoverThumbURL:     cover,
			TypeLabel:         groupTypeLabel(string(g.GroupType)),
		})
	}
	if filter.Offset > 0 {
		if filter.Offset >= len(out) {
			return []GroupListDTO{}, nil
		}
		out = out[filter.Offset:]
	}
	if filter.Limit > 0 && filter.Limit < len(out) {
		out = out[:filter.Limit]
	}
	return out, nil
}

func (a *App) GetGroup(projectID string, groupID int64) (GroupDetailDTO, error) {
	c, err := a.openProjectCache(projectID)
	if err != nil {
		return GroupDetailDTO{}, err
	}
	defer c.Close()
	groups, err := c.LoadReportGroups()
	if err != nil {
		return GroupDetailDTO{}, err
	}
	var g *model.ReportGroup
	for i := range groups {
		if groups[i].GroupID == groupID {
			g = &groups[i]
			break
		}
	}
	if g == nil {
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
			FileID:        it.FileID,
			Path:          it.Path,
			Action:        string(it.Action),
			Similarity:    it.Similarity,
			QualityScore:  it.QualityScore,
			SizeBytes:     it.SizeBytes,
			ThumbURL:      a.mediaURLFromPath(it.ThumbPath),
			IsRecommended: it.FileID == g.RecommendedFileID,
			Reasons:       it.Reasons,
		}
		if item.ThumbURL == "" {
			item.ThumbURL = a.mediaURLFromPath(filepath.Join(a.store.ThumbDir(projectID), fmt.Sprintf("%d.jpg", it.FileID)))
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
		if g.RecommendedFileID > 0 && it.FileID != g.RecommendedFileID {
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
	return c.SetGroupStatus(groupID, st)
}

func (a *App) SetRecommended(projectID string, groupID int64, fileID int64) error {
	c, err := a.openProjectCache(projectID)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.UpdateRecommended(groupID, fileID)
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
		return a.listFrameURLs(projectID, fileID)
	}
	defer c.Close()
	paths, err := c.LoadFramePreviews(fileID)
	if err != nil || len(paths) == 0 {
		return a.listFrameURLs(projectID, fileID)
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if u := a.mediaURLFromPath(p); u != "" {
			out = append(out, u)
		}
	}
	return out
}

func (a *App) listFrameURLs(projectID string, fileID int64) []string {
	dir := a.store.FrameDir(projectID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{}
	}
	prefix := fmt.Sprintf("%d_", fileID)
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) && strings.HasSuffix(strings.ToLower(e.Name()), ".jpg") {
			if u := a.mediaURLFromPath(filepath.Join(dir, e.Name())); u != "" {
				out = append(out, u)
			}
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
	if len(req.Paths) == 0 {
		return DeleteResultDTO{}, errors.New("没有选中任何文件")
	}
	if len(req.GroupIDs) > 0 {
		if err := a.validateGroupDeletes(req); err != nil {
			return DeleteResultDTO{}, err
		}
	}

	res := ops.DeleteFiles(ops.Request{Mode: mode, Paths: req.Paths, GroupIDs: req.GroupIDs})
	out := DeleteResultDTO{
		Succeeded: res.Succeeded,
		Failed:    res.Failed,
		Mode:      res.Mode,
	}

	if req.ProjectID != "" {
		if c, err := a.openProjectCache(req.ProjectID); err == nil {
			okSet := map[string]bool{}
			for _, p := range res.Succeeded {
				okSet[p] = true
			}
			groups, _ := c.LoadReportGroups()
			type delRec struct {
				FileID  int64
				Path    string
				OK      bool
				Message string
			}
			for _, p := range req.Paths {
				rec := delRec{Path: p, OK: okSet[p]}
				if !rec.OK {
					for _, f := range res.Failed {
						if f.Path == p {
							rec.Message = f.Message
							break
						}
					}
				}
				for _, g := range groups {
					for _, it := range g.Items {
						if it.Path == p {
							_ = c.RecordDeleteOps(mode, g.GroupID, []struct {
								FileID  int64
								Path    string
								OK      bool
								Message string
							}{{it.FileID, rec.Path, rec.OK, rec.Message}})
						}
					}
				}
			}
			for _, g := range groups {
				alive := 0
				for _, it := range g.Items {
					if _, err := os.Stat(it.Path); err == nil {
						alive++
					}
				}
				if alive >= 1 && alive < len(g.Items) {
					if err := c.SetGroupStatus(g.GroupID, model.GroupProcessed); err == nil {
						out.MarkedProcessed = append(out.MarkedProcessed, g.GroupID)
					}
				}
			}
			_ = c.Close()
		}
	}

	a.emitEvent("files:deleted", map[string]any{
		"paths": out.Succeeded,
		"mode":  out.Mode,
	})
	return out, nil
}

func (a *App) validateGroupDeletes(req DeleteRequestDTO) error {
	c, err := a.openProjectCache(req.ProjectID)
	if err != nil {
		return err
	}
	defer c.Close()
	groups, err := c.LoadReportGroups()
	if err != nil {
		return err
	}
	del := map[string]bool{}
	for _, p := range req.Paths {
		del[p] = true
	}
	want := map[int64]bool{}
	for _, id := range req.GroupIDs {
		want[id] = true
	}
	for _, g := range groups {
		if !want[g.GroupID] {
			continue
		}
		alive := 0
		for _, it := range g.Items {
			if !del[it.Path] {
				if _, err := os.Stat(it.Path); err == nil {
					alive++
				}
			}
		}
		if alive < 1 {
			return fmt.Errorf("组 #%d 删除后将没有任何保留文件，已阻止", g.GroupID)
		}
	}
	return nil
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

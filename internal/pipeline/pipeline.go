package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"media-dedupe/internal/cache"
	"media-dedupe/internal/candidate"
	"media-dedupe/internal/config"
	"media-dedupe/internal/discovery"
	"media-dedupe/internal/hashfile"
	imgutil "media-dedupe/internal/imagehash"
	"media-dedupe/internal/match"
	"media-dedupe/internal/model"
	"media-dedupe/internal/progress"
	"media-dedupe/internal/score"
	"media-dedupe/internal/video"
)

type Options struct {
	Paths         []string
	CachePath     string
	ThumbDir      string
	FrameDir      string
	Threshold     float64
	Recursive     bool
	IncludeImages bool
	IncludeVideos bool
	Workers       int
	VideoWorkers  int
	FrameCount    int
	EnableThumbs  bool
	OnProgress    func(msg string)
	Ctx           context.Context
	OnEvent       func(ev progress.Event)
	ProjectID     string
}

type Result struct {
	Groups   []model.ReportGroup
	Errors   []model.ReportError
	Meta     model.ScanMeta
	ThumbDir string
	Canceled bool
}

type fileCacheInfo struct {
	fileID    int64
	unchanged bool
}

type imageFacts struct {
	fileID  int64
	path    string
	size    int64
	meta    model.ImageMetadata
	phash   string
	quality float64
}

type videoFacts struct {
	fileID  int64
	path    string
	size    int64
	meta    model.VideoMetadata
	hashes  []string
	quality float64
}

func (o *Options) context() context.Context {
	if o.Ctx != nil {
		return o.Ctx
	}
	return context.Background()
}

func Scan(opts Options) (*Result, error) {
	if opts.Workers < 1 {
		opts.Workers = config.DefaultWorkers
	}
	if opts.VideoWorkers < 1 {
		opts.VideoWorkers = config.DefaultVideoWorkers
	}
	if opts.FrameCount < 1 {
		opts.FrameCount = config.DefaultFrameCount
	}
	if opts.Threshold <= 0 {
		opts.Threshold = config.DefaultSimilarityThreshold
	}
	ctx := opts.context()

	startWall := time.Now()
	startStamp := startWall.UTC().Format("2006-01-02 15:04:05")

	c, err := cache.Open(opts.CachePath)
	if err != nil {
		return nil, fmt.Errorf("open cache: %w", err)
	}
	defer c.Close()

	scanRunID, err := c.StartScanRun(opts.Paths, map[string]any{
		"similarity":    opts.Threshold,
		"recursive":     opts.Recursive,
		"include_image": opts.IncludeImages,
		"include_video": opts.IncludeVideos,
		"workers":       opts.Workers,
		"video_workers": opts.VideoWorkers,
		"frames":        opts.FrameCount,
	})
	if err != nil {
		return nil, err
	}

	var errsMu sync.Mutex
	var reportErrs []model.ReportError
	exactCount := 0
	similarCount := 0
	recordErr := func(path, stage, message string) {
		errsMu.Lock()
		reportErrs = append(reportErrs, model.ReportError{Path: path, Stage: stage, Message: message})
		n := len(reportErrs)
		errsMu.Unlock()
		_ = c.RecordError(scanRunID, path, stage, message)
		opts.emit(progress.Event{
			Stage:    progress.StageError,
			Phase:    "error",
			Message:  message,
			Errors:   n,
			CurrentFile: path,
		})
	}
	progressFn := func(msg string) {
		if opts.OnProgress != nil {
			opts.OnProgress(msg)
		}
	}
	emit := opts.emit

	if err := ctx.Err(); err != nil {
		_ = c.FinishScanRun(scanRunID, 0, 0)
		emit(progress.Event{Stage: progress.StageCanceled, Phase: "canceled", Message: "已取消，缓存已保留", ProjectID: opts.ProjectID})
		return &Result{Canceled: true, Meta: model.ScanMeta{Paths: opts.Paths, StartedAt: startStamp, CachePath: opts.CachePath}}, nil
	}

	emit(progress.Event{Stage: progress.StageDiscover, Phase: "running", Message: "发现文件...", ProjectID: opts.ProjectID})
	progressFn("discovering files...")
	discovered := discovery.Discover(opts.Paths, opts.Recursive, recordErr)
	var files []model.DiscoveredFile
	for _, d := range discovered {
		if d.MediaType == model.MediaImage && !opts.IncludeImages {
			continue
		}
		if d.MediaType == model.MediaVideo && !opts.IncludeVideos {
			continue
		}
		files = append(files, d)
	}
	if err := ctx.Err(); err != nil {
		_ = c.FinishScanRun(scanRunID, len(files), 0)
		emit(progress.Event{Stage: progress.StageCanceled, Phase: "canceled", Message: "已取消，缓存已保留", FilesSeen: len(files), ProjectID: opts.ProjectID})
		return &Result{Canceled: true, Meta: model.ScanMeta{Paths: opts.Paths, StartedAt: startStamp, FilesSeen: len(files), CachePath: opts.CachePath}}, nil
	}
	emit(progress.Event{Stage: progress.StageDiscover, Phase: "done", Message: fmt.Sprintf("发现 %d 个媒体文件", len(files)), Current: len(files), Total: len(files), Percent: 10, FilesSeen: len(files), ProjectID: opts.ProjectID})
	progressFn(fmt.Sprintf("discovered %d media files", len(files)))

	infos := make([]fileCacheInfo, len(files))
	for i, d := range files {
		if err := ctx.Err(); err != nil {
			_ = c.FinishScanRun(scanRunID, len(files), 0)
			emit(progress.Event{Stage: progress.StageCanceled, Phase: "canceled", Message: "已取消，缓存已保留", FilesSeen: len(files), ProjectID: opts.ProjectID})
			return &Result{Canceled: true, Meta: model.ScanMeta{Paths: opts.Paths, StartedAt: startStamp, FilesSeen: len(files), CachePath: opts.CachePath}}, nil
		}
		unchanged := c.IsUnchanged(d)
		id, err := c.UpsertFile(d)
		if err != nil {
			recordErr(d.Path, "cache", err.Error())
			continue
		}
		infos[i] = fileCacheInfo{fileID: id, unchanged: unchanged}
		_ = c.TouchFile(id)
	}
	_ = c.MarkMissingLastSeenBefore(startStamp)

	emit(progress.Event{Stage: progress.StageExact, Phase: "running", Message: "SHA-256 精确哈希...", Percent: 15, FilesSeen: len(files), ProjectID: opts.ProjectID})
	progressFn("exact duplicate pass...")
	exactGroups, usedPaths := exactPass(ctx, c, files, infos, recordErr, opts.Workers, &exactCount, emit)
	if err := ctx.Err(); err != nil {
		_ = c.FinishScanRun(scanRunID, len(files), len(reportErrs))
		emit(progress.Event{Stage: progress.StageCanceled, Phase: "canceled", Message: "已取消，缓存已保留", FilesSeen: len(files), FoundExact: exactCount, ProjectID: opts.ProjectID})
		return &Result{Canceled: true, Errors: reportErrs, Meta: model.ScanMeta{Paths: opts.Paths, StartedAt: startStamp, FilesSeen: len(files), CachePath: opts.CachePath}}, nil
	}
	emit(progress.Event{Stage: progress.StageExact, Phase: "done", Message: fmt.Sprintf("精确重复 %d 组", exactCount), Percent: 35, FilesSeen: len(files), FoundExact: exactCount, ProjectID: opts.ProjectID})

	var similarImageGroups []model.ReportGroup
	var imageFactsList []imageFacts
	if opts.IncludeImages {
		if err := ctx.Err(); err == nil {
			emit(progress.Event{Stage: progress.StageImage, Phase: "running", Message: "图片 pHash 感知哈希...", Percent: 40, FilesSeen: len(files), FoundExact: exactCount, ProjectID: opts.ProjectID})
			progressFn("image similarity pass...")
			imageFactsList = collectImages(ctx, c, files, infos, usedPaths, recordErr, opts)
			similarImageGroups = imageSimilarity(imageFactsList, opts)
			similarCount += len(similarImageGroups)
			emit(progress.Event{Stage: progress.StageImage, Phase: "done", Message: fmt.Sprintf("近似图片 %d 组", len(similarImageGroups)), Percent: 60, FilesSeen: len(files), FoundExact: exactCount, FoundSimilar: similarCount, ProjectID: opts.ProjectID})
		}
	}

	var similarVideoGroups []model.ReportGroup
	if opts.IncludeVideos {
		ffmpeg, _ := video.Available()
		if !ffmpeg {
			emit(progress.Event{Stage: progress.StageVideo, Phase: "skipped", Message: "未检测到 FFmpeg，跳过视频抽帧", Percent: 65, FilesSeen: len(files), FoundExact: exactCount, FoundSimilar: similarCount, ProjectID: opts.ProjectID})
		} else if err := ctx.Err(); err == nil {
			emit(progress.Event{Stage: progress.StageVideo, Phase: "running", Message: "视频 FFmpeg 抽帧多帧 pHash...", Percent: 65, FilesSeen: len(files), FoundExact: exactCount, FoundSimilar: similarCount, ProjectID: opts.ProjectID})
			progressFn("video similarity pass...")
			videoFactsList := collectVideos(ctx, c, files, infos, usedPaths, recordErr, opts)
			similarVideoGroups = videoSimilarity(videoFactsList, opts)
			similarCount += len(similarVideoGroups)
			emit(progress.Event{Stage: progress.StageVideo, Phase: "done", Message: fmt.Sprintf("近似视频 %d 组", len(similarVideoGroups)), Percent: 80, FilesSeen: len(files), FoundExact: exactCount, FoundSimilar: similarCount, ProjectID: opts.ProjectID})
		}
	}

	if err := ctx.Err(); err != nil {
		_ = c.FinishScanRun(scanRunID, len(files), len(reportErrs))
		emit(progress.Event{Stage: progress.StageCanceled, Phase: "canceled", Message: "已取消，缓存已保留", FilesSeen: len(files), FoundExact: exactCount, FoundSimilar: similarCount, ProjectID: opts.ProjectID})
		return &Result{Canceled: true, Errors: reportErrs, Meta: model.ScanMeta{Paths: opts.Paths, StartedAt: startStamp, FilesSeen: len(files), CachePath: opts.CachePath}}, nil
	}

	emit(progress.Event{Stage: progress.StageMatch, Phase: "done", Message: "分组匹配完成", Percent: 85, FilesSeen: len(files), FoundExact: exactCount, FoundSimilar: similarCount, ProjectID: opts.ProjectID})

	groups := make([]model.ReportGroup, 0, len(exactGroups)+len(similarImageGroups)+len(similarVideoGroups))
	nextID := int64(1)
	for _, g := range exactGroups {
		g.GroupID = nextID
		g.StableKey = groupStableKey(g)
		nextID++
		groups = append(groups, g)
	}
	for _, g := range similarImageGroups {
		g.GroupID = nextID
		g.StableKey = groupStableKey(g)
		nextID++
		groups = append(groups, g)
	}
	for _, g := range similarVideoGroups {
		g.GroupID = nextID
		g.StableKey = groupStableKey(g)
		nextID++
		groups = append(groups, g)
	}

	if opts.EnableThumbs && opts.ThumbDir != "" {
		emit(progress.Event{Stage: progress.StageThumb, Phase: "running", Message: "生成缩略图...", Percent: 90, FilesSeen: len(files), FoundExact: exactCount, FoundSimilar: similarCount, ProjectID: opts.ProjectID})
		progressFn("generating thumbnails...")
		attachThumbs(groups, files, infos, opts)
	}

	if opts.FrameDir != "" {
		saveFramePreviews(c, opts.FrameDir)
	}

	if err := c.ReplaceReportGroups(groups, scanRunID); err != nil {
		return nil, err
	}
	errsMu.Lock()
	failed := len(reportErrs)
	finalErrs := reportErrs
	errsMu.Unlock()
	if err := c.FinishScanRun(scanRunID, len(files), failed); err != nil {
		return nil, err
	}

	emit(progress.Event{Stage: progress.StageDone, Phase: "done", Message: "扫描完成", Percent: 100, FilesSeen: len(files), FoundExact: exactCount, FoundSimilar: similarCount, Errors: failed, ProjectID: opts.ProjectID})
	progressFn("done")

	return &Result{
		Groups: groups,
		Errors: finalErrs,
		Meta: model.ScanMeta{
			Paths:       opts.Paths,
			StartedAt:   startStamp,
			FinishedAt:  time.Now().UTC().Format("2006-01-02 15:04:05"),
			FilesSeen:   len(files),
			FilesFailed: failed,
			Threshold:   opts.Threshold,
			CachePath:   opts.CachePath,
		},
		ThumbDir: opts.ThumbDir,
	}, nil
}

func (o Options) emit(ev progress.Event) {
	if o.OnEvent == nil {
		return
	}
	if ev.ProjectID == "" {
		ev.ProjectID = o.ProjectID
	}
	o.OnEvent(ev)
}

func groupStableKey(g model.ReportGroup) string {
	paths := make([]string, 0, len(g.Items))
	for _, it := range g.Items {
		paths = append(paths, it.Path)
	}
	sort.Strings(paths)
	h := sha256.New()
	fmt.Fprintf(h, "%s|", g.GroupType)
	for i, p := range paths {
		if i > 0 {
			h.Write([]byte{'|'})
		}
		h.Write([]byte(strings.ToLower(p)))
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func exactPass(
	ctx context.Context,
	c *cache.Cache,
	files []model.DiscoveredFile,
	infos []fileCacheInfo,
	recordErr func(path, stage, message string),
	workers int,
	exactCount *int,
	emit func(progress.Event),
) ([]model.ReportGroup, map[string]struct{}) {
	used := map[string]struct{}{}
	sizeBuckets := map[int64][]int{}
	for i, f := range files {
		if infos[i].fileID == 0 {
			continue
		}
		sizeBuckets[f.SizeBytes] = append(sizeBuckets[f.SizeBytes], i)
	}

	var groups []model.ReportGroup
	for _, idxs := range sizeBuckets {
		if err := ctx.Err(); err != nil {
			break
		}
		if len(idxs) < 2 {
			continue
		}
		hashes := make([]string, len(idxs))
		g := errgroup.Group{}
		g.SetLimit(workers)
		for j, idx := range idxs {
			j, idx := j, idx
			g.Go(func() error {
				if ctx.Err() != nil {
					return nil
				}
				f := files[idx]
				fileID := infos[idx].fileID
				if infos[idx].unchanged {
					if h, ok, err := c.LoadFileHash(fileID); err == nil && ok {
						hashes[j] = h
						return nil
					}
				}
				h, err := hashfile.SHA256File(f.Path)
				if err != nil {
					recordErr(f.Path, "file_hash", err.Error())
					return nil
				}
				_ = c.SaveFileHash(fileID, f.SizeBytes, h)
				hashes[j] = h
				return nil
			})
		}
		_ = g.Wait()

		byHash := map[string][]int{}
		for j, h := range hashes {
			if h == "" {
				continue
			}
			byHash[h] = append(byHash[h], idxs[j])
		}
		for _, members := range byHash {
			if len(members) < 2 {
				continue
			}
			quality := map[int64]float64{}
			for _, idx := range members {
				f := files[idx]
				fileID := infos[idx].fileID
				used[f.Path] = struct{}{}
				q := qualityFor(c, f, fileID, infos[idx].unchanged, recordErr)
				quality[fileID] = q
			}
			actions := score.ChooseActions(quality, -1.0)
			winner := score.Winner(quality)
			var items []model.ReportItem
			for _, idx := range members {
				fileID := infos[idx].fileID
				items = append(items, model.ReportItem{
					FileID:       fileID,
					Path:         files[idx].Path,
					Action:       actions[fileID],
					Similarity:   1.0,
					QualityScore: quality[fileID],
					SizeBytes:    files[idx].SizeBytes,
					Reasons:      []string{"identical SHA-256 file hash"},
				})
			}
			groups = append(groups, model.ReportGroup{
				GroupType:         model.GroupExact,
				Confidence:        1.0,
				RecommendedFileID: winner,
				Items:             items,
			})
			*exactCount++
		}
	}
	return groups, used
}

func qualityFor(
	c *cache.Cache,
	f model.DiscoveredFile,
	fileID int64,
	unchanged bool,
	recordErr func(path, stage, message string),
) float64 {
	if f.MediaType == model.MediaImage {
		meta := loadImageMeta(c, f, fileID, unchanged, recordErr)
		return score.ImageQuality(meta, f.SizeBytes)
	}
	meta := loadVideoMeta(c, f, fileID, unchanged, recordErr)
	return score.VideoQuality(meta)
}

func loadImageMeta(
	c *cache.Cache,
	f model.DiscoveredFile,
	fileID int64,
	unchanged bool,
	recordErr func(path, stage, message string),
) model.ImageMetadata {
	if unchanged {
		if m, ok, err := c.LoadImageMetadata(fileID); err == nil && ok {
			return m
		}
	}
	m := imgutil.ReadMetadata(f.Path)
	_ = c.SaveImageMetadata(fileID, m)
	if !m.IsReadable {
		recordErr(f.Path, "image_metadata", "unreadable image")
	}
	return m
}

func loadVideoMeta(
	c *cache.Cache,
	f model.DiscoveredFile,
	fileID int64,
	unchanged bool,
	recordErr func(path, stage, message string),
) model.VideoMetadata {
	if unchanged {
		if m, ok, err := c.LoadVideoMetadata(fileID); err == nil && ok {
			return m
		}
	}
	m := video.ProbeMetadata(f.Path)
	_ = c.SaveVideoMetadata(fileID, m)
	if !m.IsReadable {
		recordErr(f.Path, "video_metadata", "unreadable video")
	}
	return m
}

func collectImages(
	ctx context.Context,
	c *cache.Cache,
	files []model.DiscoveredFile,
	infos []fileCacheInfo,
	usedPaths map[string]struct{},
	recordErr func(path, stage, message string),
	opts Options,
) []imageFacts {
	var targets []int
	for i, f := range files {
		if f.MediaType != model.MediaImage || infos[i].fileID == 0 {
			continue
		}
		if _, used := usedPaths[f.Path]; used {
			continue
		}
		if !discovery.SupportsPHash(f.Extension) {
			continue
		}
		targets = append(targets, i)
	}

	out := make([]*imageFacts, len(targets))
	g := errgroup.Group{}
	g.SetLimit(opts.Workers)
	for j, idx := range targets {
		j, idx := j, idx
		g.Go(func() error {
			if ctx.Err() != nil {
				return nil
			}
			f := files[idx]
			fileID := infos[idx].fileID
			unchanged := infos[idx].unchanged

			meta := loadImageMeta(c, f, fileID, unchanged, recordErr)
			if !meta.IsReadable {
				return nil
			}

			var phash string
			if unchanged {
				if hs, err := c.LoadPerceptualHashes(fileID, "image_phash"); err == nil && len(hs) > 0 {
					phash = hs[0]
				}
			}
			if phash == "" {
				h, err := imgutil.ComputePHashFromFile(f.Path)
				if err != nil {
					recordErr(f.Path, "image_phash", err.Error())
					return nil
				}
				phash = h
				_ = c.SavePerceptualHashes(fileID, "image_phash", []string{phash})
			}
			out[j] = &imageFacts{
				fileID:  fileID,
				path:    f.Path,
				size:    f.SizeBytes,
				meta:    meta,
				phash:   phash,
				quality: score.ImageQuality(meta, f.SizeBytes),
			}
			return nil
		})
	}
	_ = g.Wait()

	var facts []imageFacts
	for _, item := range out {
		if item != nil {
			facts = append(facts, *item)
		}
	}
	return facts
}

func imageSimilarity(facts []imageFacts, opts Options) []model.ReportGroup {
	if len(facts) < 2 {
		return nil
	}
	items := make([]candidate.ImageCandidateItem, len(facts))
	for i, f := range facts {
		items[i] = candidate.ImageCandidateItem{
			Width:  f.meta.Width,
			Height: f.meta.Height,
			PHash:  f.phash,
		}
	}
	pairs := candidate.ExpandImageCandidates(items)
	maxDist := candidate.MaxHammingDistance(opts.Threshold, 64)
	pairs = candidate.FilterByPHash(items, pairs, maxDist)

	var edges []model.SimilarityEdge
	edgeSim := map[[2]int64]float64{}
	for _, p := range pairs {
		sim, err := hashfile.Similarity(facts[p[0]].phash, facts[p[1]].phash)
		if err != nil || sim < opts.Threshold {
			continue
		}
		a, b := facts[p[0]].fileID, facts[p[1]].fileID
		edges = append(edges, model.SimilarityEdge{
			LeftFileID:  a,
			RightFileID: b,
			GroupType:   model.GroupSimilarImage,
			Similarity:  sim,
			Reasons:     []string{"visual image hash match"},
		})
		edgeSim[match.EdgeKey(a, b)] = sim
	}
	return groupsFromComponents(factsToPathQuality(facts), edges, edgeSim, model.GroupSimilarImage, "visual image hash match (pHash)")
}

func collectVideos(
	ctx context.Context,
	c *cache.Cache,
	files []model.DiscoveredFile,
	infos []fileCacheInfo,
	usedPaths map[string]struct{},
	recordErr func(path, stage, message string),
	opts Options,
) []videoFacts {
	tmpRoot, err := os.MkdirTemp("", "media-dedupe-frames-")
	if err != nil {
		recordErr(opts.CachePath, "video", err.Error())
		return nil
	}
	defer os.RemoveAll(tmpRoot)

	var targets []int
	for i, f := range files {
		if f.MediaType != model.MediaVideo || infos[i].fileID == 0 {
			continue
		}
		if _, used := usedPaths[f.Path]; used {
			continue
		}
		targets = append(targets, i)
	}

	out := make([]*videoFacts, len(targets))
	var slotMu sync.Mutex
	g := errgroup.Group{}
	g.SetLimit(opts.VideoWorkers)
	for j, idx := range targets {
		j, idx := j, idx
		g.Go(func() error {
			if ctx.Err() != nil {
				return nil
			}
			f := files[idx]
			fileID := infos[idx].fileID
			unchanged := infos[idx].unchanged

			meta := loadVideoMeta(c, f, fileID, unchanged, recordErr)
			if !meta.IsReadable || meta.DurationMs <= 0 {
				return nil
			}

			var hashes []string
			if unchanged {
				if hs, err := c.LoadPerceptualHashes(fileID, "video_frame_phash"); err == nil && len(hs) > 0 {
					hashes = hs
				}
			}
			if len(hashes) == 0 {
				fileTmp := filepath.Join(tmpRoot, fmt.Sprintf("f%d", fileID))
				if err := os.MkdirAll(fileTmp, 0o755); err != nil {
					recordErr(f.Path, "video_frame_phash", err.Error())
					return nil
				}
				hs, err := video.ComputeFrameHashes(f.Path, meta, opts.FrameCount, config.DefaultFrameFailureLimit, fileTmp)
				if err != nil {
					recordErr(f.Path, "video_frame_phash", err.Error())
					return nil
				}
				hashes = hs
				_ = c.SavePerceptualHashes(fileID, "video_frame_phash", hashes)
			}
			if opts.FrameDir != "" {
				exportFramePreviews(f.Path, meta, fileID, opts.FrameDir, opts.FrameCount)
			}
			fact := &videoFacts{
				fileID:  fileID,
				path:    f.Path,
				size:    f.SizeBytes,
				meta:    meta,
				hashes:  hashes,
				quality: score.VideoQuality(meta),
			}
			slotMu.Lock()
			out[j] = fact
			slotMu.Unlock()
			return nil
		})
	}
	_ = g.Wait()

	var facts []videoFacts
	for _, item := range out {
		if item != nil {
			facts = append(facts, *item)
		}
	}
	return facts
}

func exportFramePreviews(path string, meta model.VideoMetadata, fileID int64, frameDir string, frameCount int) {
	_ = os.MkdirAll(frameDir, 0o755)
	timestamps := video.BuildFrameTimestamps(meta.DurationMs, frameCount)
	for i, ts := range timestamps {
		out := filepath.Join(frameDir, fmt.Sprintf("%d_%d.jpg", fileID, i))
		if _, err := os.Stat(out); err == nil {
			continue
		}
		if err := video.ExtractFrame(path, ts, out); err != nil {
			continue
		}
	}
}

func saveFramePreviews(c *cache.Cache, frameDir string) {
	entries, err := os.ReadDir(frameDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".jpg") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".jpg")
		parts := strings.SplitN(base, "_", 2)
		if len(parts) != 2 {
			continue
		}
		var fileID, idx int
		if _, err := fmt.Sscanf(parts[0], "%d", &fileID); err != nil {
			continue
		}
		if _, err := fmt.Sscanf(parts[1], "%d", &idx); err != nil {
			continue
		}
		_ = c.SaveFramePreview(int64(fileID), idx, filepath.Join(frameDir, e.Name()))
	}
}

func videoSimilarity(facts []videoFacts, opts Options) []model.ReportGroup {
	if len(facts) < 2 {
		return nil
	}
	durations := make([]int64, len(facts))
	widths := make([]int, len(facts))
	heights := make([]int, len(facts))
	for i, f := range facts {
		durations[i] = f.meta.DurationMs
		widths[i] = f.meta.Width
		heights[i] = f.meta.Height
	}
	pairs := candidate.VideoBucket(durations, widths, heights)

	var edges []model.SimilarityEdge
	edgeSim := map[[2]int64]float64{}
	for _, p := range pairs {
		if !match.IsVideoMetadataCandidate(facts[p[0]].meta, facts[p[1]].meta) {
			continue
		}
		sim := match.VideoHashSimilarity(facts[p[0]].hashes, facts[p[1]].hashes)
		if sim < opts.Threshold {
			continue
		}
		a, b := facts[p[0]].fileID, facts[p[1]].fileID
		edges = append(edges, model.SimilarityEdge{
			LeftFileID:  a,
			RightFileID: b,
			GroupType:   model.GroupSimilarVideo,
			Similarity:  sim,
			Reasons:     []string{"video frame hash match"},
		})
		edgeSim[match.EdgeKey(a, b)] = sim
	}

	items := make([]pathQuality, len(facts))
	for i, f := range facts {
		items[i] = pathQuality{fileID: f.fileID, path: f.path, size: f.size, quality: f.quality}
	}
	return groupsFromComponents(items, edges, edgeSim, model.GroupSimilarVideo, "video frame hash match (FFmpeg multi-frame pHash)")
}

type pathQuality struct {
	fileID  int64
	path    string
	size    int64
	quality float64
}

func factsToPathQuality(facts []imageFacts) []pathQuality {
	out := make([]pathQuality, len(facts))
	for i, f := range facts {
		out[i] = pathQuality{f.fileID, f.path, f.size, f.quality}
	}
	return out
}

func groupsFromComponents(
	items []pathQuality,
	edges []model.SimilarityEdge,
	edgeSim map[[2]int64]float64,
	gtype model.GroupType,
	reason string,
) []model.ReportGroup {
	byID := map[int64]pathQuality{}
	for _, it := range items {
		byID[it.fileID] = it
	}
	components := match.ConnectedComponents(edges)
	var groups []model.ReportGroup
	for _, comp := range components {
		if len(comp) < 2 {
			continue
		}
		quality := map[int64]float64{}
		for _, id := range comp {
			quality[id] = byID[id].quality
		}
		actions := score.ChooseActions(quality, 0)
		winner := score.Winner(quality)
		conf := componentConfidence(comp, edgeSim)
		var reportItems []model.ReportItem
		for _, id := range comp {
			info := byID[id]
			reportItems = append(reportItems, model.ReportItem{
				FileID:       id,
				Path:         info.path,
				Action:       actions[id],
				Similarity:   itemSimilarity(id, comp, edgeSim),
				QualityScore: info.quality,
				SizeBytes:    info.size,
				Reasons:      []string{reason, "quality score uses media metadata"},
			})
		}
		groups = append(groups, model.ReportGroup{
			GroupType:         gtype,
			Confidence:        conf,
			RecommendedFileID: winner,
			Items:             reportItems,
		})
	}
	return groups
}

func componentConfidence(comp []int64, edgeSim map[[2]int64]float64) float64 {
	if len(comp) == 0 {
		return 0
	}
	minSim := 1.0
	found := false
	for i := 0; i < len(comp); i++ {
		for j := i + 1; j < len(comp); j++ {
			if s, ok := edgeSim[match.EdgeKey(comp[i], comp[j])]; ok {
				found = true
				if s < minSim {
					minSim = s
				}
			}
		}
	}
	if !found {
		return 1.0
	}
	return minSim
}

func itemSimilarity(id int64, comp []int64, edgeSim map[[2]int64]float64) float64 {
	if len(comp) > 0 && id == comp[0] {
		return 1.0
	}
	best := 0.0
	found := false
	for _, other := range comp {
		if other == id {
			continue
		}
		if s, ok := edgeSim[match.EdgeKey(id, other)]; ok {
			found = true
			if s > best {
				best = s
			}
		}
	}
	if !found {
		return 1.0
	}
	return best
}

func attachThumbs(
	groups []model.ReportGroup,
	files []model.DiscoveredFile,
	infos []fileCacheInfo,
	opts Options,
) {
	byID := map[int64]model.DiscoveredFile{}
	for i, f := range files {
		if infos[i].fileID != 0 {
			byID[infos[i].fileID] = f
		}
	}
	_ = os.MkdirAll(opts.ThumbDir, 0o755)

	for gi := range groups {
		for ii := range groups[gi].Items {
			item := &groups[gi].Items[ii]
			f, ok := byID[item.FileID]
			if !ok {
				continue
			}
			out := filepath.Join(opts.ThumbDir, fmt.Sprintf("%d.jpg", item.FileID))
			if _, err := os.Stat(out); err == nil {
				item.ThumbPath = out
				continue
			}
			var err error
			if f.MediaType == model.MediaImage {
				if !discovery.SupportsPHash(f.Extension) {
					continue
				}
				err = imgutil.ThumbnailFromPath(f.Path, out, config.ThumbLongEdge)
			} else {
				meta := video.ProbeMetadata(f.Path)
				if !meta.IsReadable {
					continue
				}
				err = video.ExtractThumbFrame(f.Path, meta, out, config.ThumbLongEdge)
			}
			if err == nil {
				item.ThumbPath = out
			}
		}
	}
}

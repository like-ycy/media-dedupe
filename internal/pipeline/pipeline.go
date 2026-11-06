package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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
	"media-dedupe/internal/textdedupe"
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
	IncludeTexts  bool
	TextThreshold float64
	TextWorkers   int
	Workers       int
	VideoWorkers  int
	FrameCount    int
	// VideoCompareMode selects how videos are deduplicated: precise samples
	// frames and compares perceptual hashes, simple only compares content
	// fingerprints and never invokes FFmpeg.
	VideoCompareMode string
	// VerifySampledHash confirms sampled matches with full SHA-256, reusing
	// valid cached hashes and reading only candidates without one.
	VerifySampledHash bool
	OnProgress        func(msg string)
	Ctx               context.Context
	OnEvent           func(ev progress.Event)
	ProjectID         string
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
	fileID    int64
	path      string
	extension string
	size      int64
	meta      model.ImageMetadata
	phash     string
	quality   float64
}

type videoFacts struct {
	fileID    int64
	path      string
	extension string
	size      int64
	meta      model.VideoMetadata
	hashes    []string
	quality   float64
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
	if opts.TextThreshold <= 0 {
		opts.TextThreshold = config.DefaultTextSimilarityThreshold
	}
	if opts.TextWorkers < 1 {
		opts.TextWorkers = config.DefaultTextWorkers
	}
	opts.VideoCompareMode = config.NormalizeVideoMode(opts.VideoCompareMode)
	ctx := opts.context()

	startWall := time.Now()
	startStamp := startWall.UTC().Format("2006-01-02 15:04:05")

	c, err := cache.Open(opts.CachePath)
	if err != nil {
		return nil, fmt.Errorf("open cache: %w", err)
	}
	defer c.Close()

	scanRunID, err := c.StartScanRun(opts.Paths, map[string]any{
		"similarity":      opts.Threshold,
		"recursive":       opts.Recursive,
		"include_image":   opts.IncludeImages,
		"include_video":   opts.IncludeVideos,
		"include_text":    opts.IncludeTexts,
		"text_similarity": opts.TextThreshold,
		"text_workers":    opts.TextWorkers,
		"workers":         opts.Workers,
		"video_workers":   opts.VideoWorkers,
		"frames":          opts.FrameCount,
		"video_mode":      opts.VideoCompareMode,
		"verify_sampled":  opts.VerifySampledHash,
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
			Stage:       progress.StageError,
			Phase:       "error",
			Message:     message,
			Errors:      n,
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
		if d.MediaType == model.MediaText && !opts.IncludeTexts {
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
	exactGroups, usedPaths := exactPass(ctx, c, files, infos, recordErr, opts, &exactCount)
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
		if opts.VideoCompareMode == config.VideoModeSimple {
			// Videos were already deduplicated by content fingerprint in the
			// exact pass; frame extraction would only add visual near-duplicate
			// detection at the cost of one seek per sampled frame.
			emit(progress.Event{Stage: progress.StageVideo, Phase: "skipped", Message: "简易模式：跳过视频抽帧，仅按内容指纹判重", Percent: 80, FilesSeen: len(files), FoundExact: exactCount, FoundSimilar: similarCount, ProjectID: opts.ProjectID})
		} else if !ffmpeg {
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
	var similarTextGroups []model.ReportGroup
	if opts.IncludeTexts && ctx.Err() == nil {
		emit(progress.Event{Stage: progress.StageText, Phase: "running", Message: "TXT 正文相似度分析...", Percent: 82, FilesSeen: len(files), ProjectID: opts.ProjectID})
		textFiles := make([]model.DiscoveredFile, 0)
		textIDs := make([]int64, 0)
		textUnchanged := make([]bool, 0)
		for i, file := range files {
			if file.MediaType != model.MediaText {
				continue
			}
			if _, used := usedPaths[file.Path]; used {
				continue
			}
			textFiles = append(textFiles, file)
			textIDs = append(textIDs, infos[i].fileID)
			textUnchanged = append(textUnchanged, infos[i].unchanged)
		}
		facts, textErrs := textdedupe.FactsCached(textFiles, textIDs, textUnchanged, opts.TextWorkers, c)
		for _, textErr := range textErrs {
			recordErr(textErr.Path, textErr.Stage, textErr.Message)
		}
		factsByExtension := make(map[string][]textdedupe.Fact)
		for _, fact := range facts {
			extension := strings.ToLower(filepath.Ext(fact.Path))
			factsByExtension[extension] = append(factsByExtension[extension], fact)
		}
		for _, facts := range factsByExtension {
			similarTextGroups = append(similarTextGroups, textdedupe.Groups(facts, opts.TextThreshold)...)
		}
		similarCount += len(similarTextGroups)
		emit(progress.Event{Stage: progress.StageText, Phase: "done", Message: fmt.Sprintf("文本近似重复 %d 组", len(similarTextGroups)), Percent: 84, FilesSeen: len(files), FoundSimilar: similarCount, ProjectID: opts.ProjectID})
	}

	if err := ctx.Err(); err != nil {
		_ = c.FinishScanRun(scanRunID, len(files), len(reportErrs))
		emit(progress.Event{Stage: progress.StageCanceled, Phase: "canceled", Message: "已取消，缓存已保留", FilesSeen: len(files), FoundExact: exactCount, FoundSimilar: similarCount, ProjectID: opts.ProjectID})
		return &Result{Canceled: true, Errors: reportErrs, Meta: model.ScanMeta{Paths: opts.Paths, StartedAt: startStamp, FilesSeen: len(files), CachePath: opts.CachePath}}, nil
	}

	emit(progress.Event{Stage: progress.StageMatch, Phase: "done", Message: "分组匹配完成", Percent: 85, FilesSeen: len(files), FoundExact: exactCount, FoundSimilar: similarCount, ProjectID: opts.ProjectID})

	groups := make([]model.ReportGroup, 0, len(exactGroups)+len(similarImageGroups)+len(similarVideoGroups)+len(similarTextGroups))
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
	for _, g := range similarTextGroups {
		g.GroupID = nextID
		g.StableKey = groupStableKey(g)
		nextID++
		groups = append(groups, g)
	}

	// Do not pre-generate JPEG thumbs: the UI previews original images
	// (Explorer-style) and uses fixed icons for video/text.

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

type exactBucketKey struct {
	size int64
	ext  string
}

// sampledFingerprintConfidence is used for byte-identity claims that were not
// confirmed by a full read.
const sampledFingerprintConfidence = 0.99

func exactPass(
	ctx context.Context,
	c *cache.Cache,
	files []model.DiscoveredFile,
	infos []fileCacheInfo,
	recordErr func(path, stage, message string),
	opts Options,
	exactCount *int,
) ([]model.ReportGroup, map[string]struct{}) {
	used := map[string]struct{}{}
	sizeBuckets := map[exactBucketKey][]int{}
	for i, f := range files {
		if infos[i].fileID == 0 {
			continue
		}
		key := exactBucketKey{size: f.SizeBytes, ext: strings.ToLower(f.Extension)}
		sizeBuckets[key] = append(sizeBuckets[key], i)
	}

	// Exact matching is restricted to files sharing size and extension; other
	// files are never opened in this pass.
	candidate := make([]bool, len(files))
	candidates := 0
	for _, idxs := range sizeBuckets {
		if len(idxs) < 2 {
			continue
		}
		for _, idx := range idxs {
			candidate[idx] = true
			candidates++
		}
	}
	if candidates == 0 {
		return nil, used
	}

	fullHashes := make([]string, len(files))
	quickHashes := make([]string, len(files))

	// Schedule candidates in discovery order to retain directory locality on HDDs.
	g := errgroup.Group{}
	g.SetLimit(opts.Workers)
	for idx := range files {
		if ctx.Err() != nil {
			break
		}
		if !candidate[idx] {
			continue
		}
		g.Go(func() error {
			if ctx.Err() != nil {
				return nil
			}
			f := files[idx]
			fileID := infos[idx].fileID
			// Small files are cheaper to read fully than to fingerprint twice.
			if f.SizeBytes < config.QuickTierMinSize {
				h, err := fullHashFor(c, f, fileID, infos[idx].unchanged)
				if err != nil {
					recordErr(f.Path, "file_hash", err.Error())
					return nil
				}
				fullHashes[idx] = h
				return nil
			}
			if infos[idx].unchanged {
				h, ok, err := c.LoadFileHash(fileID)
				if err != nil {
					recordErr(f.Path, "file_hash", err.Error())
					return nil
				}
				if ok {
					fullHashes[idx] = h
				}
			}
			h, err := quickHashFor(c, f, fileID, infos[idx].unchanged)
			if err != nil {
				recordErr(f.Path, "file_hash", err.Error())
				return nil
			}
			quickHashes[idx] = h
			return nil
		})
	}
	_ = g.Wait()
	if ctx.Err() != nil {
		return nil, used
	}

	var groups []model.ReportGroup
	for _, idxs := range sizeBuckets {
		if ctx.Err() != nil {
			break
		}
		if len(idxs) < 2 {
			continue
		}
		if files[idxs[0]].SizeBytes < config.QuickTierMinSize {
			for _, members := range groupIndices(func(i int) string { return fullHashes[i] }, idxs) {
				if ctx.Err() != nil {
					return groups, used
				}
				if group := buildExactGroup(ctx, c, files, infos, members, 1.0, "identical SHA-256 file hash", used, recordErr); group != nil {
					groups = append(groups, *group)
					*exactCount++
				}
			}
			continue
		}
		for _, members := range groupIndices(func(i int) string { return quickHashes[i] }, idxs) {
			if ctx.Err() != nil {
				return groups, used
			}
			if len(members) < 2 {
				continue
			}
			known := groupIndices(func(i int) string { return fullHashes[i] }, members)
			knownCount := 0
			for _, cached := range known {
				knownCount += len(cached)
			}
			// Conflicting full hashes prove a sample collision. Verify any new
			// members as well so they cannot bridge known-different files.
			verify := needsFullVerification(files[members[0]], opts) || len(known) > 1
			if !verify && knownCount < len(members) {
				if group := buildExactGroup(ctx, c, files, infos, members, sampledFingerprintConfidence, "identical sampled content fingerprint (size + head/mid/tail)", used, recordErr); group != nil {
					groups = append(groups, *group)
					*exactCount++
				}
				continue
			}
			// Reuse full hashes already loaded; read only the missing candidates.
			for _, idx := range members {
				if ctx.Err() != nil {
					return groups, used
				}
				if fullHashes[idx] != "" {
					continue
				}
				h, err := fullHashFor(c, files[idx], infos[idx].fileID, false)
				if err != nil {
					recordErr(files[idx].Path, "file_hash", err.Error())
					continue
				}
				fullHashes[idx] = h
			}
			for _, confirmed := range groupIndices(func(i int) string { return fullHashes[i] }, members) {
				if ctx.Err() != nil {
					return groups, used
				}
				if group := buildExactGroup(ctx, c, files, infos, confirmed, 1.0, "identical SHA-256 file hash", used, recordErr); group != nil {
					groups = append(groups, *group)
					*exactCount++
				}
			}
		}
	}
	return groups, used
}

// needsFullVerification reports whether a sampled fingerprint match must be
// confirmed by reading both files fully.
func needsFullVerification(f model.DiscoveredFile, opts Options) bool {
	if opts.VerifySampledHash {
		return true
	}
	// Simple video mode deliberately trades the full read for speed: videos are
	// exactly what makes a full-library verification scan unaffordable.
	return !(f.MediaType == model.MediaVideo && opts.VideoCompareMode == config.VideoModeSimple)
}

// groupIndices buckets candidate indices by their identity hash, dropping
// entries whose hash could not be computed.
func groupIndices(hashAt func(int) string, idxs []int) map[string][]int {
	byHash := make(map[string][]int, len(idxs))
	for _, idx := range idxs {
		h := hashAt(idx)
		if h == "" {
			continue
		}
		byHash[h] = append(byHash[h], idx)
	}
	return byHash
}

func buildExactGroup(
	ctx context.Context,
	c *cache.Cache,
	files []model.DiscoveredFile,
	infos []fileCacheInfo,
	members []int,
	confidence float64,
	reason string,
	used map[string]struct{},
	recordErr func(path, stage, message string),
) *model.ReportGroup {
	if len(members) < 2 {
		return nil
	}
	quality := map[int64]float64{}
	for _, idx := range members {
		if ctx.Err() != nil {
			return nil
		}
		f := files[idx]
		fileID := infos[idx].fileID
		used[f.Path] = struct{}{}
		quality[fileID] = qualityFor(ctx, c, f, fileID, infos[idx].unchanged, recordErr)
	}
	actions := score.ChooseActions(quality, -1.0)
	winner := score.Winner(quality)
	items := make([]model.ReportItem, 0, len(members))
	for _, idx := range members {
		fileID := infos[idx].fileID
		items = append(items, model.ReportItem{
			FileID:       fileID,
			Path:         files[idx].Path,
			Action:       actions[fileID],
			Similarity:   confidence,
			QualityScore: quality[fileID],
			SizeBytes:    files[idx].SizeBytes,
			Reasons:      []string{reason},
		})
	}
	return &model.ReportGroup{
		GroupType:         model.GroupExact,
		Confidence:        confidence,
		RecommendedFileID: winner,
		Items:             items,
	}
}

func fullHashFor(c *cache.Cache, f model.DiscoveredFile, fileID int64, unchanged bool) (string, error) {
	if unchanged {
		if h, ok, err := c.LoadFileHash(fileID); err == nil && ok {
			return h, nil
		}
	}
	h, err := hashfile.SHA256File(f.Path)
	if err != nil {
		return "", err
	}
	_ = c.SaveFileHash(fileID, f.SizeBytes, h)
	return h, nil
}

func quickHashFor(c *cache.Cache, f model.DiscoveredFile, fileID int64, unchanged bool) (string, error) {
	if unchanged {
		if h, ok, err := c.LoadQuickHash(fileID, f.SizeBytes); err == nil && ok {
			return h, nil
		}
	}
	h, err := hashfile.SampleHashFile(f.Path, f.SizeBytes, config.DefaultSampleSegmentBytes)
	if err != nil {
		return "", err
	}
	_ = c.SaveQuickHash(fileID, f.SizeBytes, h)
	return h, nil
}

func qualityFor(
	ctx context.Context,
	c *cache.Cache,
	f model.DiscoveredFile,
	fileID int64,
	unchanged bool,
	recordErr func(path, stage, message string),
) float64 {
	if f.MediaType == model.MediaText {
		return score.TextQuality(f.SizeBytes)
	}
	if f.MediaType == model.MediaImage {
		meta := loadImageMeta(c, f, fileID, unchanged, recordErr)
		return score.ImageQuality(meta, f.SizeBytes)
	}
	meta := loadVideoMeta(ctx, c, f, fileID, unchanged, recordErr)
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
	ctx context.Context,
	c *cache.Cache,
	f model.DiscoveredFile,
	fileID int64,
	unchanged bool,
	recordErr func(path, stage, message string),
) model.VideoMetadata {
	if unchanged {
		if m, ok, err := c.LoadVideoMetadata(fileID); err == nil && ok {
			if !m.IsReadable {
				recordErr(f.Path, "video_metadata", "unreadable video")
			}
			return m
		}
	}
	m := video.ProbeMetadataContext(ctx, f.Path)
	if ctx.Err() != nil {
		return model.VideoMetadata{}
	}
	if !m.IsReadable {
		recordErr(f.Path, "video_metadata", "unreadable video")
		return m
	}
	if err := c.SaveVideoMetadata(fileID, m); err != nil {
		recordErr(f.Path, "video_metadata", err.Error())
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
				fileID:    fileID,
				path:      f.Path,
				extension: strings.ToLower(f.Extension),
				size:      f.SizeBytes,
				meta:      meta,
				phash:     phash,
				quality:   score.ImageQuality(meta, f.SizeBytes),
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
	byExtension := make(map[string][]imageFacts)
	for _, fact := range facts {
		extension := strings.ToLower(fact.extension)
		byExtension[extension] = append(byExtension[extension], fact)
	}
	var groups []model.ReportGroup
	for _, bucket := range byExtension {
		groups = append(groups, imageSimilarityByExtension(bucket, opts)...)
	}
	return groups
}

func imageSimilarityByExtension(facts []imageFacts, opts Options) []model.ReportGroup {
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
	maxDist := candidate.MaxHammingDistance(opts.Threshold, 64)
	pairs := candidate.ImageCandidatesByPHash(items, maxDist)

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

	// Index old previews once rather than rereading the growing directory per video.
	previewFiles := make(map[string][]string)
	if opts.FrameDir != "" {
		entries, err := os.ReadDir(opts.FrameDir)
		if err != nil && !os.IsNotExist(err) {
			recordErr(opts.FrameDir, "video_preview", err.Error())
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jpg") {
				continue
			}
			id, _, ok := strings.Cut(entry.Name(), "_")
			if ok {
				previewFiles[id] = append(previewFiles[id], filepath.Join(opts.FrameDir, entry.Name()))
			}
		}
	}

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
	g := errgroup.Group{}
	g.SetLimit(opts.VideoWorkers)
	for j, idx := range targets {
		if ctx.Err() != nil {
			break
		}
		j, idx := j, idx
		g.Go(func() error {
			if ctx.Err() != nil {
				return nil
			}
			f := files[idx]
			fileID := infos[idx].fileID
			unchanged := infos[idx].unchanged

			meta := loadVideoMeta(ctx, c, f, fileID, unchanged, recordErr)
			if !meta.IsReadable || meta.DurationMs <= 0 {
				return nil
			}

			var hashes []string
			hashType := video.FrameHashType(opts.FrameCount)
			if unchanged {
				if hs, err := c.LoadPerceptualHashes(fileID, hashType); err != nil {
					recordErr(f.Path, "video_cache", err.Error())
				} else {
					hashes = hs
				}
			}
			if len(hashes) == 0 {
				fileTmp := filepath.Join(tmpRoot, fmt.Sprintf("f%d", fileID))
				if err := os.MkdirAll(fileTmp, 0o755); err != nil {
					recordErr(f.Path, "video_frame_phash", err.Error())
					return nil
				}
				defer os.RemoveAll(fileTmp)
				if err := clearFramePreviews(c, fileID, previewFiles[fmt.Sprint(fileID)]); err != nil {
					recordErr(f.Path, "video_preview", err.Error())
				}
				framePaths, err := video.ExtractFramesContext(ctx, f.Path, meta, opts.FrameCount, fileTmp)
				if err != nil {
					if ctx.Err() == nil {
						recordErr(f.Path, "video_frame_phash", err.Error())
					}
					return nil
				}
				hs, err := video.HashFrames(ctx, framePaths, config.DefaultFrameFailureLimit)
				if err != nil {
					if ctx.Err() == nil {
						recordErr(f.Path, "video_frame_phash", err.Error())
					}
					return nil
				}
				hashes = hs
				if opts.FrameDir != "" {
					if err := copyFramePreviews(ctx, c, framePaths, fileID, opts.FrameDir, hashType); err != nil && ctx.Err() == nil {
						recordErr(f.Path, "video_preview", err.Error())
						if err := c.ReplaceFramePreviews(fileID, nil); err != nil {
							recordErr(f.Path, "video_preview", err.Error())
						}
					}
				}
				if ctx.Err() != nil {
					return nil
				}
				if err := c.SavePerceptualHashes(fileID, hashType, hashes); err != nil {
					recordErr(f.Path, "video_cache", err.Error())
				}
			} else if opts.FrameDir != "" {
				if err := ensureFramePreviews(ctx, c, f.Path, meta, fileID, opts.FrameDir, opts.FrameCount, hashType); err != nil && ctx.Err() == nil {
					recordErr(f.Path, "video_preview", err.Error())
				}
			}
			if ctx.Err() != nil {
				return nil
			}
			fact := &videoFacts{
				fileID:    fileID,
				path:      f.Path,
				extension: strings.ToLower(f.Extension),
				size:      f.SizeBytes,
				meta:      meta,
				hashes:    hashes,
				quality:   score.VideoQuality(meta),
			}
			out[j] = fact
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

func framePreviewPath(frameDir string, fileID int64, hashType string, index int) string {
	return filepath.Join(frameDir, fmt.Sprintf("%d_%s_%d.jpg", fileID, hashType, index))
}

func ensureFramePreviews(ctx context.Context, c *cache.Cache, path string, meta model.VideoMetadata, fileID int64, frameDir string, frameCount int, hashType string) error {
	registered, err := c.LoadFramePreviews(fileID)
	if err != nil {
		return err
	}
	valid := make(map[string]struct{}, len(registered))
	prefix := fmt.Sprintf("%d_%s_", fileID, hashType)
	for _, path := range registered {
		if strings.HasPrefix(filepath.Base(path), prefix) {
			valid[path] = struct{}{}
		}
	}
	timestamps := video.BuildFrameTimestamps(meta.DurationMs, frameCount)
	paths := make([]string, 0, len(timestamps))
	for i, ts := range timestamps {
		if err := ctx.Err(); err != nil {
			return err
		}
		out := framePreviewPath(frameDir, fileID, hashType, i)
		// A committed preview set includes all available timestamps. An omitted
		// position can be past the final frame in a short, low-frame-rate video.
		if _, registered := valid[out]; !registered && len(valid) > 0 {
			continue
		}
		if _, trusted := valid[out]; trusted {
			if info, err := os.Stat(out); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
				paths = append(paths, out)
				continue
			} else if err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		if err := video.ExtractFrameContext(ctx, path, ts, out); err != nil {
			if errors.Is(err, video.ErrNoFrame) {
				continue
			}
			return err
		}
		paths = append(paths, out)
	}
	return c.ReplaceFramePreviews(fileID, paths)
}

func clearFramePreviews(c *cache.Cache, fileID int64, paths []string) error {
	if err := c.ReplaceFramePreviews(fileID, nil); err != nil {
		return err
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func copyFramePreviews(ctx context.Context, c *cache.Cache, framePaths []string, fileID int64, frameDir, hashType string) error {
	if err := os.MkdirAll(frameDir, 0o755); err != nil {
		return err
	}
	paths := make([]string, 0, len(framePaths))
	for index, framePath := range framePaths {
		if err := ctx.Err(); err != nil {
			return err
		}
		if framePath == "" {
			continue
		}
		out := framePreviewPath(frameDir, fileID, hashType, index)
		if err := copyFrame(framePath, out); err != nil {
			return err
		}
		paths = append(paths, out)
	}
	return c.ReplaceFramePreviews(fileID, paths)
}

func copyFrame(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(destination)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(destination)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(destination)
	}
	return closeErr
}

func videoSimilarity(facts []videoFacts, opts Options) []model.ReportGroup {
	byExtension := make(map[string][]videoFacts)
	for _, fact := range facts {
		extension := strings.ToLower(fact.extension)
		byExtension[extension] = append(byExtension[extension], fact)
	}
	var groups []model.ReportGroup
	for _, bucket := range byExtension {
		groups = append(groups, videoSimilarityByExtension(bucket, opts)...)
	}
	return groups
}

func videoSimilarityByExtension(facts []videoFacts, opts Options) []model.ReportGroup {
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

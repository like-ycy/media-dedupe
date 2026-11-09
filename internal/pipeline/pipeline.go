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
	frames    []video.FrameHash
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
	opts.FrameCount = config.NormalizeFrameCount(opts.FrameCount)
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
	parentCtx := opts.context()
	// A fatal storage/program error cancels in-flight workers and stops the scan.
	ctx, cancelScan := context.WithCancel(parentCtx)
	defer cancelScan()

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
	recordErr := func(path, stage, kind, message string) {
		errsMu.Lock()
		reportErrs = append(reportErrs, model.ReportError{Path: path, Stage: stage, Kind: kind, Message: message})
		n := len(reportErrs)
		errsMu.Unlock()
		_ = c.RecordError(scanRunID, path, stage, kind, message)
		opts.emit(progress.Event{
			Stage:       progress.StageError,
			Phase:       "error",
			Message:     message,
			Errors:      n,
			CurrentFile: path,
		})
	}
	errCount := func() int {
		errsMu.Lock()
		defer errsMu.Unlock()
		return len(reportErrs)
	}
	emit := opts.emit
	failures := newFailureTracker()
	hooks := &scanHooks{record: recordErr, failures: failures, cancel: cancelScan}
	// Files that could not be read are skipped and never stored, so drop any
	// row this scan may have created for them, even when it is canceled.
	// Stored groups from the previous scan lose those members too; repair them
	// in the same step so no group is left without a valid recommendation.
	defer func() {
		if deleted, err := c.DeleteFilesByPaths(failures.failedPaths()); err == nil && deleted > 0 {
			_ = c.PruneReportGroups()
		}
	}()
	// abortIfFatal returns the storage/program failure that stopped the scan.
	abortIfFatal := func(filesSeen int) error {
		err := failures.fatalErr()
		if err == nil {
			return nil
		}
		_ = c.FinishScanRun(scanRunID, filesSeen, errCount())
		emit(progress.Event{
			Stage:     progress.StageError,
			Phase:     "error",
			Message:   err.Error(),
			FilesSeen: filesSeen,
			Errors:    errCount(),
			ProjectID: opts.ProjectID,
		})
		return err
	}
	progressFn := func(msg string) {
		if opts.OnProgress != nil {
			opts.OnProgress(msg)
		}
	}

	if err := ctx.Err(); err != nil {
		_ = c.FinishScanRun(scanRunID, 0, 0)
		emit(progress.Event{Stage: progress.StageCanceled, Phase: "canceled", Message: "已取消，缓存已保留", ProjectID: opts.ProjectID})
		return &Result{Canceled: true, Meta: model.ScanMeta{Paths: opts.Paths, StartedAt: startStamp, CachePath: opts.CachePath}}, nil
	}

	emit(progress.Event{Stage: progress.StageDiscover, Phase: "running", Message: "发现文件...", ProjectID: opts.ProjectID})
	progressFn("discovering files...")
	discovered := discovery.Discover(ctx, opts.Paths, opts.Recursive, func(path, stage string, err error) {
		// Discovery failures never create a file row; they are only reported.
		// A storage-level failure still aborts the scan.
		if isFatalIOError(err) {
			hooks.fatal(path, stage, err)
			return
		}
		recordErr(path, stage, model.ErrorSkipped, err.Error())
	})
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
	if err := abortIfFatal(len(files)); err != nil {
		return nil, err
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
		if failures.fatalErr() != nil {
			break
		}
		if err := ctx.Err(); err != nil {
			if ferr := abortIfFatal(len(files)); ferr != nil {
				return nil, ferr
			}
			_ = c.FinishScanRun(scanRunID, len(files), 0)
			emit(progress.Event{Stage: progress.StageCanceled, Phase: "canceled", Message: "已取消，缓存已保留", FilesSeen: len(files), ProjectID: opts.ProjectID})
			return &Result{Canceled: true, Meta: model.ScanMeta{Paths: opts.Paths, StartedAt: startStamp, FilesSeen: len(files), CachePath: opts.CachePath}}, nil
		}
		unchanged := c.IsUnchanged(d)
		id, err := c.UpsertFile(d)
		if err != nil {
			hooks.fatal(d.Path, "cache", err)
			break
		}
		infos[i] = fileCacheInfo{fileID: id, unchanged: unchanged}
		_ = c.TouchFile(id)
	}
	if err := abortIfFatal(len(files)); err != nil {
		return nil, err
	}
	_ = c.MarkMissingLastSeenBefore(startStamp)

	emit(progress.Event{Stage: progress.StageExact, Phase: "running", Message: "SHA-256 精确哈希...", Percent: 15, FilesSeen: len(files), ProjectID: opts.ProjectID})
	progressFn("exact duplicate pass...")
	exact := exactPass(ctx, c, files, infos, hooks, opts, &exactCount)
	exactGroups := exact.groups
	usedPaths := exact.used
	// From here on the stages only look for near-duplicates; a file that already
	// has a byte-identical twin must survive a failure in them.
	hooks.markConfirmed(exactGroups)
	if err := abortIfFatal(len(files)); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = c.FinishScanRun(scanRunID, len(files), errCount())
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
			imageFactsList = collectImages(ctx, c, files, infos, usedPaths, hooks, opts)
			similarImageGroups = imageSimilarity(imageFactsList, opts)
			similarCount += len(similarImageGroups)
			emit(progress.Event{Stage: progress.StageImage, Phase: "done", Message: fmt.Sprintf("近似图片 %d 组", len(similarImageGroups)), Percent: 60, FilesSeen: len(files), FoundExact: exactCount, FoundSimilar: similarCount, ProjectID: opts.ProjectID})
		}
	}
	if err := abortIfFatal(len(files)); err != nil {
		return nil, err
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
			videoFactsList := collectVideos(ctx, c, files, infos, usedPaths, hooks, opts)
			similarVideoGroups = videoSimilarity(videoFactsList, opts)
			similarCount += len(similarVideoGroups)
			emit(progress.Event{Stage: progress.StageVideo, Phase: "done", Message: fmt.Sprintf("近似视频 %d 组", len(similarVideoGroups)), Percent: 80, FilesSeen: len(files), FoundExact: exactCount, FoundSimilar: similarCount, ProjectID: opts.ProjectID})
		}
	}
	if err := abortIfFatal(len(files)); err != nil {
		return nil, err
	}
	var similarTextGroups []model.ReportGroup
	if opts.IncludeTexts && ctx.Err() == nil {
		emit(progress.Event{Stage: progress.StageText, Phase: "running", Message: "TXT 正文相似度分析...", Percent: 82, FilesSeen: len(files), ProjectID: opts.ProjectID})
		textFiles := make([]model.DiscoveredFile, 0)
		textIDs := make([]int64, 0)
		textUnchanged := make([]bool, 0)
		for i, file := range files {
			if file.MediaType != model.MediaText || hooks.failed(file.Path) {
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
			// Cache read/write problems are program-level; read failures are
			// ordinary bad files and keep their errno for classification.
			if textErr.Stage == "text_cache" {
				hooks.fatal(textErr.Path, textErr.Stage, textErr.Err)
				continue
			}
			hooks.file(textErr.Path, textErr.Stage, textErr.Err)
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

	if err := abortIfFatal(len(files)); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = c.FinishScanRun(scanRunID, len(files), errCount())
		emit(progress.Event{Stage: progress.StageCanceled, Phase: "canceled", Message: "已取消，缓存已保留", FilesSeen: len(files), FoundExact: exactCount, FoundSimilar: similarCount, ProjectID: opts.ProjectID})
		return &Result{Canceled: true, Errors: reportErrs, Meta: model.ScanMeta{Paths: opts.Paths, StartedAt: startStamp, FilesSeen: len(files), CachePath: opts.CachePath}}, nil
	}

	emit(progress.Event{Stage: progress.StageMatch, Phase: "done", Message: "分组匹配完成", Percent: 85, FilesSeen: len(files), FoundExact: exactCount, FoundSimilar: similarCount, ProjectID: opts.ProjectID})

	// Same content, plus a byte-identical copy of one member: the exact group's
	// representative took part in the similarity stages, so fold its siblings
	// back in and drop the now-redundant exact group. Otherwise a single extra
	// copy would make a near-duplicate pair disappear.
	similarGroups := make([]model.ReportGroup, 0, len(similarImageGroups)+len(similarVideoGroups)+len(similarTextGroups))
	similarGroups = append(similarGroups, similarImageGroups...)
	similarGroups = append(similarGroups, similarVideoGroups...)
	similarGroups = append(similarGroups, similarTextGroups...)
	fused, absorbedExact := fuseExactGroups(similarGroups, exactGroups, exact.reps, exact.groupOfRep)

	allGroups := make([]model.ReportGroup, 0, len(exactGroups)+len(fused))
	for i, g := range exactGroups {
		if _, absorbed := absorbedExact[i]; absorbed {
			continue
		}
		allGroups = append(allGroups, g)
	}
	allGroups = append(allGroups, fused...)
	// Failed files are never compared; drop them from groups built before the
	// failure surfaced and discard groups that no longer have two members.
	allGroups = dropFailedGroups(allGroups, failures)
	// Belt and braces: a path must never be listed in two groups, otherwise the
	// delete panel counts the same file twice.
	allGroups = dedupeGroupsByPath(allGroups)

	groups := make([]model.ReportGroup, 0, len(allGroups))
	nextID := int64(1)
	for _, g := range allGroups {
		g.GroupID = nextID
		g.StableKey = groupStableKey(g)
		nextID++
		groups = append(groups, g)
	}
	// Stage counters count pre-fusion groups; the final numbers should describe
	// what the report actually contains.
	exactCount, similarCount = 0, 0
	for _, g := range groups {
		if g.GroupType == model.GroupExact {
			exactCount++
		} else {
			similarCount++
		}
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

// dropFailedGroups removes files that could not be processed from final
// groups. A group that no longer has two readable members is discarded, and
// the recommended file is re-picked when it was one of the failed files.
//
// Groups that lost no member keep their actions exactly as scored: close
// quality ties are intentionally reported as "review required", and promoting
// the recommended member to "keep" here would silently drop that signal.
func dropFailedGroups(groups []model.ReportGroup, failures *failureTracker) []model.ReportGroup {
	out := groups[:0]
	for _, g := range groups {
		items := make([]model.ReportItem, 0, len(g.Items))
		for _, it := range g.Items {
			if !failures.failed(it.Path) {
				items = append(items, it)
			}
		}
		if len(items) < 2 {
			continue
		}
		dropped := len(items) != len(g.Items)
		g.Items = items
		// Only a group whose recommendation disappeared needs new actions;
		// everything else stays byte-identical to the normal scoring output.
		if dropped && !recommendedInGroup(g) {
			rescoreGroup(&g)
		}
		out = append(out, g)
	}
	return out
}

func recommendedInGroup(g model.ReportGroup) bool {
	for _, it := range g.Items {
		if it.FileID == g.RecommendedFileID {
			return true
		}
	}
	return false
}

// fuseExactGroups folds the siblings of every exact group whose representative
// matched a similarity group into that similarity group. The absorbed exact
// groups are returned by index so they can be dropped from the report: the
// same path must never show up in two groups.
func fuseExactGroups(similar, exact []model.ReportGroup, reps map[int64]int64, groupOfRep map[int64]int) ([]model.ReportGroup, map[int]struct{}) {
	absorbed := map[int]struct{}{}
	if len(similar) == 0 || len(groupOfRep) == 0 {
		return similar, absorbed
	}

	out := make([]model.ReportGroup, 0, len(similar))
	for _, g := range similar {
		base := make(map[int64]float64, len(g.Items))
		present := make(map[int64]struct{}, len(g.Items))
		items := make([]model.ReportItem, 0, len(g.Items))
		for _, item := range g.Items {
			base[item.FileID] = item.Similarity
			present[item.FileID] = struct{}{}
			items = append(items, item)
		}

		// Only representatives reach this stage, so every matched exact group
		// belongs to this group and contributes all of its members.
		var siblings []model.ReportItem
		mergedReps := map[int64]struct{}{}
		for _, item := range g.Items {
			index, ok := groupOfRep[item.FileID]
			if !ok {
				continue
			}
			if _, already := absorbed[index]; already {
				continue
			}
			absorbed[index] = struct{}{}
			mergedReps[item.FileID] = struct{}{}
			for _, sibling := range exact[index].Items {
				if _, dup := present[sibling.FileID]; dup {
					continue
				}
				present[sibling.FileID] = struct{}{}
				siblings = append(siblings, sibling)
			}
		}
		if len(mergedReps) == 0 {
			out = append(out, g)
			continue
		}

		// Keep the recommendation and the verdicts the group builder already
		// produced. Each builder ranks members with rules that fit its media
		// type — metadata quality for images and videos, file size for text —
		// and a byte-identical copy can never outrank the member it copies, so
		// folding copies in must not re-run a different ranking over the group.
		// Rescoring here is what silently replaced a larger text file, whose
		// quality score is capped, with a smaller one.
		winner := g.RecommendedFileID
		// An exact group's comparison representative may only be a decodable
		// stand-in for its recommendation. Restore the original choice when
		// that representative wins; their byte identity preserves the scores.
		if index, ok := groupOfRep[winner]; ok {
			winner = exact[index].RecommendedFileID
		}
		baseAction := make(map[int64]model.Action, len(g.Items))
		for _, item := range g.Items {
			baseAction[item.FileID] = item.Action
		}
		winnerRep := reps[winner]
		for _, sibling := range siblings {
			similarity := noDirectSimilarity
			if rep := reps[sibling.FileID]; rep != 0 {
				if rep == winnerRep {
					similarity = 1.0
				} else if value, ok := base[rep]; ok {
					similarity = value
				}
			}
			reasons := append([]string{}, sibling.Reasons...)
			reasons = append(reasons, "byte-identical copy merged into this similarity group")
			items = append(items, model.ReportItem{
				FileID:       sibling.FileID,
				Path:         sibling.Path,
				Similarity:   similarity,
				QualityScore: sibling.QualityScore,
				SizeBytes:    sibling.SizeBytes,
				ThumbPath:    sibling.ThumbPath,
				Reasons:      reasons,
			})
		}
		for i := range items {
			action := baseAction[items[i].FileID]
			if rep := reps[items[i].FileID]; rep != 0 {
				// A copy inherits its representative's verdict: a certain
				// duplicate of a duplicate stays certain, and one that still
				// needs review keeps asking for it.
				action = baseAction[rep]
			}
			// Only one file is kept; its byte-identical copies are certain
			// cleanup rather than a second "keep".
			if action == model.ActionKeep && items[i].FileID != winner {
				action = model.ActionCleanup
			}
			items[i].Action = action
			if _, merged := mergedReps[items[i].FileID]; merged {
				items[i].Reasons = append(append([]string{}, items[i].Reasons...), "byte-identical copies of this file are in this group")
			}
		}
		g.Items = items
		g.RecommendedFileID = winner
		out = append(out, g)
	}
	return out, absorbed
}

// dedupeGroupsByPath guarantees a file appears in at most one group. Groups
// that lose members are rescored so their recommendation stays actionable.
//
// Identity is the path string plus the file ID, never a case-folded path: /media/A.jpg
// and /media/a.jpg are two different files on Linux and on case-sensitive APFS
// or Windows directories, so folding case there would drop a real member and
// dissolve a two-member exact group.
func dedupeGroupsByPath(groups []model.ReportGroup) []model.ReportGroup {
	seenPath := make(map[string]struct{}, len(groups))
	seenID := make(map[int64]struct{}, len(groups))
	out := groups[:0]
	for _, g := range groups {
		items := g.Items[:0]
		groupPaths := make(map[string]struct{}, len(g.Items))
		groupIDs := make(map[int64]struct{}, len(g.Items))
		for _, item := range g.Items {
			path := filepath.Clean(item.Path)
			if _, dup := seenPath[path]; dup {
				continue
			}
			if _, dup := seenID[item.FileID]; dup {
				continue
			}
			if _, dup := groupPaths[path]; dup {
				continue
			}
			if _, dup := groupIDs[item.FileID]; dup {
				continue
			}
			groupPaths[path] = struct{}{}
			groupIDs[item.FileID] = struct{}{}
			items = append(items, item)
		}
		if len(items) < 2 {
			continue
		}
		// Discarded singletons must not hide members of a later valid group.
		for path := range groupPaths {
			seenPath[path] = struct{}{}
		}
		for id := range groupIDs {
			seenID[id] = struct{}{}
		}
		dropped := len(items) != len(g.Items)
		g.Items = items
		if dropped && !containsFileID(items, g.RecommendedFileID) {
			rescoreGroup(&g)
		}
		out = append(out, g)
	}
	return out
}

func containsFileID(items []model.ReportItem, id int64) bool {
	for _, item := range items {
		if item.FileID == id {
			return true
		}
	}
	return false
}

// rescoreGroup re-picks the recommended file and its actions with the same
// rules the group was built with: exact groups never ask for review, while
// similarity groups keep the review threshold for close quality ties.
func rescoreGroup(g *model.ReportGroup) {
	quality := make(map[int64]float64, len(g.Items))
	for _, it := range g.Items {
		quality[it.FileID] = it.QualityScore
	}
	reviewDelta := 0.0
	if g.GroupType == model.GroupExact {
		reviewDelta = -1.0
	}
	actions := score.ChooseActions(quality, reviewDelta)
	g.RecommendedFileID = score.Winner(quality)
	for i := range g.Items {
		g.Items[i].Action = actions[g.Items[i].FileID]
		if g.GroupType != model.GroupExact {
			// Pairwise edges are no longer available here. Values measured
			// against the removed recommendation cannot describe the new one.
			g.Items[i].Similarity = noDirectSimilarity
			if g.Items[i].FileID == g.RecommendedFileID {
				g.Items[i].Similarity = 1
			}
		}
	}
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

// exactBucketKey groups files by size and media type only. The extension
// decides which decoder produces a hash, never whether two files may be
// compared: byte-identical copies must meet even when their suffixes differ.
type exactBucketKey struct {
	size  int64
	media model.MediaType
}

// sampledFingerprintConfidence is used for byte-identity claims that were not
// confirmed by a full read.
const sampledFingerprintConfidence = 0.99

// exactIndex is the exact-duplicate result plus the bookkeeping the later
// stages need. One representative per exact group keeps participating in the
// similarity stages; its siblings are skipped there and merged back when the
// representative matches something.
type exactIndex struct {
	groups []model.ReportGroup
	// used holds paths of exact-group members that are not the representative.
	// Comparing them again would report the same file twice.
	used map[string]struct{}
	// reps maps every member file ID to its group's representative.
	reps map[int64]int64
	// groupOfRep maps a representative file ID to the group's index in groups.
	groupOfRep map[int64]int
}

func newExactIndex() *exactIndex {
	return &exactIndex{
		used:       map[string]struct{}{},
		reps:       map[int64]int64{},
		groupOfRep: map[int64]int{},
	}
}

func (x *exactIndex) add(group model.ReportGroup) {
	index := len(x.groups)
	x.groups = append(x.groups, group)
	rep := approximateRepresentative(group)
	x.groupOfRep[rep.FileID] = index
	for _, item := range group.Items {
		x.reps[item.FileID] = rep.FileID
		if item.FileID != rep.FileID {
			x.used[item.Path] = struct{}{}
		}
	}
}

// approximateRepresentative picks the member that carries the group into the
// similarity stages. It is usually the recommended file, but when that file's
// format has no approximate features — a JPEG renamed to .heic, say — another
// identical member is used instead, so the group still gets compared with the
// rest of the library. Holding the whole group back would lose real
// near-duplicates of the copies we can process.
func approximateRepresentative(group model.ReportGroup) model.ReportItem {
	var fallback model.ReportItem
	hasFallback := false
	for _, item := range group.Items {
		if !discovery.SupportsPHash(filepath.Ext(item.Path)) {
			continue
		}
		if item.FileID == group.RecommendedFileID {
			return item
		}
		if !hasFallback || item.FileID < fallback.FileID {
			fallback, hasFallback = item, true
		}
	}
	if hasFallback {
		return fallback
	}
	// No member can produce approximate features: the recommendation stays the
	// representative and simply never reaches those stages.
	for _, item := range group.Items {
		if item.FileID == group.RecommendedFileID {
			return item
		}
	}
	return group.Items[0]
}

func exactPass(
	ctx context.Context,
	c *cache.Cache,
	files []model.DiscoveredFile,
	infos []fileCacheInfo,
	hooks *scanHooks,
	opts Options,
	exactCount *int,
) *exactIndex {
	ex := newExactIndex()
	sizeBuckets := map[exactBucketKey][]int{}
	for i, f := range files {
		if infos[i].fileID == 0 || hooks.failed(f.Path) {
			continue
		}
		key := exactBucketKey{size: f.SizeBytes, media: f.MediaType}
		sizeBuckets[key] = append(sizeBuckets[key], i)
	}

	// Exact matching is restricted to files sharing size and media type; other
	// files are never opened in this pass. Extensions are deliberately ignored:
	// a copy with a renamed extension is still a byte-identical duplicate.
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
		return ex
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
			if hooks.failed(f.Path) {
				return nil
			}
			// Small files are cheaper to read fully than to fingerprint twice.
			if f.SizeBytes < config.QuickTierMinSize {
				h, err := fullHashFor(c, f, fileID, infos[idx].unchanged)
				if err != nil {
					hooks.read(f.Path, "file_hash", err)
					return nil
				}
				fullHashes[idx] = h
				return nil
			}
			if infos[idx].unchanged {
				h, ok, err := c.LoadFileHash(fileID)
				if err != nil {
					hooks.fatal(f.Path, "file_hash", err)
					return nil
				}
				if ok {
					fullHashes[idx] = h
				}
			}
			h, err := quickHashFor(c, f, fileID, infos[idx].unchanged)
			if err != nil {
				hooks.read(f.Path, "file_hash", err)
				return nil
			}
			quickHashes[idx] = h
			return nil
		})
	}
	_ = g.Wait()
	if ctx.Err() != nil {
		return ex
	}

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
					return ex
				}
				if group := buildExactGroup(ctx, c, files, infos, members, 1.0, "identical SHA-256 file hash", hooks); group != nil {
					ex.add(*group)
					*exactCount++
				}
			}
			continue
		}
		for _, members := range groupIndices(func(i int) string { return quickHashes[i] }, idxs) {
			if ctx.Err() != nil {
				return ex
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
				if group := buildExactGroup(ctx, c, files, infos, members, sampledFingerprintConfidence, "identical sampled content fingerprint (size + head/mid/tail)", hooks); group != nil {
					ex.add(*group)
					*exactCount++
				}
				continue
			}
			// Reuse full hashes already loaded; read only the missing candidates.
			for _, idx := range members {
				if ctx.Err() != nil {
					return ex
				}
				if fullHashes[idx] != "" {
					continue
				}
				h, err := fullHashFor(c, files[idx], infos[idx].fileID, false)
				if err != nil {
					hooks.read(files[idx].Path, "file_hash", err)
					continue
				}
				fullHashes[idx] = h
			}
			for _, confirmed := range groupIndices(func(i int) string { return fullHashes[i] }, members) {
				if ctx.Err() != nil {
					return ex
				}
				if group := buildExactGroup(ctx, c, files, infos, confirmed, 1.0, "identical SHA-256 file hash", hooks); group != nil {
					ex.add(*group)
					*exactCount++
				}
			}
		}
	}
	return ex
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
	hooks *scanHooks,
) *model.ReportGroup {
	if len(members) < 2 {
		return nil
	}
	quality := map[int64]float64{}
	valid := make([]int, 0, len(members))
	for _, idx := range members {
		if ctx.Err() != nil {
			return nil
		}
		f := files[idx]
		if hooks.failed(f.Path) {
			continue
		}
		fileID := infos[idx].fileID
		q := qualityFor(ctx, c, f, fileID, infos[idx].unchanged)
		quality[fileID] = q
		valid = append(valid, idx)
	}
	// A group needs at least two readable members; a file that just failed is
	// never reported as a duplicate.
	if len(valid) < 2 {
		return nil
	}
	actions := score.ChooseActions(quality, -1.0)
	winner := score.Winner(quality)
	items := make([]model.ReportItem, 0, len(valid))
	for _, idx := range valid {
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
	if err := c.SaveFileHash(fileID, f.SizeBytes, h); err != nil {
		return "", &cacheWriteError{err: err}
	}
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
	if err := c.SaveQuickHash(fileID, f.SizeBytes, h); err != nil {
		return "", &cacheWriteError{err: err}
	}
	return h, nil
}

func qualityFor(
	ctx context.Context,
	c *cache.Cache,
	f model.DiscoveredFile,
	fileID int64,
	unchanged bool,
) float64 {
	if f.MediaType == model.MediaText {
		return score.TextQuality(f.SizeBytes)
	}
	// Quality only ranks copies that are already known to be duplicates; a
	// failed metadata probe must not disqualify byte-identical files. The
	// comparison stages below still skip files they cannot actually read.
	if f.MediaType == model.MediaImage {
		return score.ImageQuality(imageMetaForQuality(c, f, fileID, unchanged), f.SizeBytes)
	}
	return score.VideoQuality(videoMetaForQuality(ctx, c, f, fileID, unchanged))
}

func imageMetaForQuality(c *cache.Cache, f model.DiscoveredFile, fileID int64, unchanged bool) model.ImageMetadata {
	if unchanged {
		if m, ok, err := c.LoadImageMetadata(fileID); err == nil && ok {
			return m
		}
	}
	m, err := imgutil.ReadMetadata(f.Path)
	if err != nil {
		return m
	}
	_ = c.SaveImageMetadata(fileID, m)
	return m
}

func videoMetaForQuality(ctx context.Context, c *cache.Cache, f model.DiscoveredFile, fileID int64, unchanged bool) model.VideoMetadata {
	if unchanged {
		if m, ok, err := c.LoadVideoMetadata(fileID); err == nil && ok {
			return m
		}
	}
	m, _ := video.ProbeMetadataContextE(ctx, f.Path)
	if m.IsReadable {
		_ = c.SaveVideoMetadata(fileID, m)
	}
	return m
}

func loadImageMeta(
	c *cache.Cache,
	f model.DiscoveredFile,
	fileID int64,
	unchanged bool,
	hooks *scanHooks,
) (model.ImageMetadata, bool) {
	if unchanged {
		if m, ok, err := c.LoadImageMetadata(fileID); err == nil && ok {
			if !m.IsReadable {
				hooks.file(f.Path, "image_metadata", errors.New("unreadable image"))
				return m, false
			}
			return m, true
		}
	}
	m, err := imgutil.ReadMetadata(f.Path)
	if err != nil {
		hooks.file(f.Path, "image_metadata", err)
		return m, false
	}
	if err := c.SaveImageMetadata(fileID, m); err != nil {
		hooks.fatal(f.Path, "image_metadata", err)
		return m, false
	}
	return m, true
}

func loadVideoMeta(
	ctx context.Context,
	c *cache.Cache,
	f model.DiscoveredFile,
	fileID int64,
	unchanged bool,
	hooks *scanHooks,
) (model.VideoMetadata, bool) {
	if unchanged {
		if m, ok, err := c.LoadVideoMetadata(fileID); err == nil && ok {
			if !m.IsReadable {
				hooks.file(f.Path, "video_metadata", errors.New("unreadable video"))
				return m, false
			}
			return m, true
		}
	}
	m, err := video.ProbeMetadataContextE(ctx, f.Path)
	if ctx.Err() != nil {
		return m, false
	}
	if err != nil {
		hooks.file(f.Path, "video_metadata", err)
		return m, false
	}
	if !m.IsReadable {
		hooks.file(f.Path, "video_metadata", errors.New("unreadable video"))
		return m, false
	}
	if err := c.SaveVideoMetadata(fileID, m); err != nil {
		hooks.fatal(f.Path, "video_metadata", err)
		return m, false
	}
	return m, true
}

func collectImages(
	ctx context.Context,
	c *cache.Cache,
	files []model.DiscoveredFile,
	infos []fileCacheInfo,
	usedPaths map[string]struct{},
	hooks *scanHooks,
	opts Options,
) []imageFacts {
	var targets []int
	for i, f := range files {
		if f.MediaType != model.MediaImage || infos[i].fileID == 0 || hooks.failed(f.Path) {
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

			meta, ok := loadImageMeta(c, f, fileID, unchanged, hooks)
			if !ok {
				return nil
			}

			var phash string
			if unchanged {
				hs, err := c.LoadPerceptualHashes(fileID, "image_phash")
				if err != nil {
					hooks.fatal(f.Path, "image_cache", err)
					return nil
				}
				if len(hs) > 0 {
					phash = hs[0]
				}
			}
			if phash == "" {
				h, err := imgutil.ComputePHashFromFile(f.Path)
				if err != nil {
					hooks.file(f.Path, "image_phash", err)
					return nil
				}
				phash = h
				if err := c.SavePerceptualHashes(fileID, "image_phash", []string{phash}); err != nil {
					hooks.fatal(f.Path, "image_cache", err)
					return nil
				}
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

// imageSimilarity selects candidates across extensions using dimensions and
// pHash, so the same picture stored as .jpg and .png can form one group.
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
	hooks *scanHooks,
	opts Options,
) []videoFacts {
	tmpRoot, err := os.MkdirTemp("", "media-dedupe-frames-")
	if err != nil {
		hooks.aux(opts.CachePath, "video", err)
		return nil
	}
	defer os.RemoveAll(tmpRoot)

	// Index old previews once rather than rereading the growing directory per video.
	previewFiles := make(map[string][]string)
	if opts.FrameDir != "" {
		entries, err := os.ReadDir(opts.FrameDir)
		if err != nil && !os.IsNotExist(err) {
			hooks.aux(opts.FrameDir, "video_preview", err)
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
		if f.MediaType != model.MediaVideo || infos[i].fileID == 0 || hooks.failed(f.Path) {
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

			meta, ok := loadVideoMeta(ctx, c, f, fileID, unchanged, hooks)
			if !ok {
				return nil
			}
			if meta.DurationMs <= 0 {
				hooks.file(f.Path, "video_metadata", errors.New("video has no duration"))
				return nil
			}

			var frames []video.FrameHash
			hashType := video.FrameHashType(opts.FrameCount)
			if unchanged {
				rows, err := c.LoadFrameHashes(fileID, hashType)
				if err != nil {
					hooks.fatal(f.Path, "video_cache", err)
					return nil
				}
				frames = frameHashesFromRows(rows)
			}
			if len(frames) == 0 {
				fileTmp := filepath.Join(tmpRoot, fmt.Sprintf("f%d", fileID))
				if err := os.MkdirAll(fileTmp, 0o755); err != nil {
					hooks.aux(f.Path, "video_frame_phash", err)
					return nil
				}
				defer os.RemoveAll(fileTmp)
				if err := clearFramePreviews(c, fileID, previewFiles[fmt.Sprint(fileID)]); err != nil {
					hooks.aux(f.Path, "video_preview", err)
				}
				framePaths, err := video.ExtractFramesContext(ctx, f.Path, meta, opts.FrameCount, fileTmp)
				if err != nil {
					if ctx.Err() == nil {
						hooks.file(f.Path, "video_frame_phash", err)
					}
					return nil
				}
				timestamps := video.BuildFrameTimestamps(meta.DurationMs, opts.FrameCount)
				hs, err := video.HashFrames(ctx, framePaths, timestamps, config.DefaultFrameFailureLimit)
				if err != nil {
					if errors.Is(err, video.ErrNoUsableFrames) {
						// The file decoded fine but every sampled position was
						// blank: no approximate features, not a read failure.
						// Marking it failed would delete an exact duplicate that
						// SHA-256 already confirmed.
						return nil
					}
					if ctx.Err() == nil {
						hooks.file(f.Path, "video_frame_phash", err)
					}
					return nil
				}
				frames = hs
				if opts.FrameDir != "" {
					// Preview names must match the hashed positions: frame N of the
					// strip and hash N have to be the same moment.
					if err := copyFramePreviews(ctx, c, framePaths, frames, fileID, opts.FrameDir, hashType); err != nil && ctx.Err() == nil {
						hooks.aux(f.Path, "video_preview", err)
						if err := c.ReplaceFramePreviews(fileID, nil); err != nil {
							hooks.aux(f.Path, "video_preview", err)
						}
					}
				}
				if ctx.Err() != nil {
					return nil
				}
				if err := c.SaveFrameHashes(fileID, hashType, frameHashRows(frames)); err != nil {
					hooks.fatal(f.Path, "video_cache", err)
					return nil
				}
			} else if opts.FrameDir != "" {
				if err := ensureFramePreviews(ctx, c, f.Path, meta, fileID, opts.FrameDir, opts.FrameCount, hashType, frames); err != nil && ctx.Err() == nil {
					hooks.aux(f.Path, "video_preview", err)
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
				frames:    frames,
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

func ensureFramePreviews(ctx context.Context, c *cache.Cache, path string, meta model.VideoMetadata, fileID int64, frameDir string, frameCount int, hashType string, frames []video.FrameHash) error {
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
	// Only positions that produced a hash get a preview, so the strip and the
	// comparison always describe the same moments.
	hashed := make(map[int]struct{}, len(frames))
	for _, frame := range frames {
		hashed[frame.Index] = struct{}{}
	}
	timestamps := video.BuildFrameTimestamps(meta.DurationMs, frameCount)
	paths := make([]string, 0, len(timestamps))
	for i, ts := range timestamps {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(hashed) > 0 {
			if _, ok := hashed[i]; !ok {
				continue
			}
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

// frameHashRows and frameHashesFromRows convert between the pipeline's frame
// hashes and the storage rows, keeping the cache free of video-package imports.
func frameHashRows(frames []video.FrameHash) []cache.FrameHashRow {
	rows := make([]cache.FrameHashRow, len(frames))
	for i, frame := range frames {
		rows[i] = cache.FrameHashRow{Index: frame.Index, TimestampMs: frame.TimestampMs, Hash: frame.Hash}
	}
	return rows
}

func frameHashesFromRows(rows []cache.FrameHashRow) []video.FrameHash {
	frames := make([]video.FrameHash, len(rows))
	for i, row := range rows {
		frames[i] = video.FrameHash{Index: row.Index, TimestampMs: row.TimestampMs, Hash: row.Hash}
	}
	return frames
}

func copyFramePreviews(ctx context.Context, c *cache.Cache, framePaths []string, frames []video.FrameHash, fileID int64, frameDir, hashType string) error {
	if err := os.MkdirAll(frameDir, 0o755); err != nil {
		return err
	}
	paths := make([]string, 0, len(frames))
	for _, frame := range frames {
		if err := ctx.Err(); err != nil {
			return err
		}
		if frame.Index < 0 || frame.Index >= len(framePaths) {
			continue
		}
		framePath := framePaths[frame.Index]
		if framePath == "" {
			continue
		}
		out := framePreviewPath(frameDir, fileID, hashType, frame.Index)
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

// videoSimilarity selects candidates by duration and dimensions regardless of
// extension, so footage remuxed into another container can still be compared.
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
		sim := match.VideoHashSimilarity(facts[p[0]].frames, facts[p[1]].frames, opts.FrameCount)
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
				Similarity:   itemSimilarity(id, winner, edgeSim),
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

// noDirectSimilarity marks a member that has no comparison edge to the
// recommended file. Reporting it as 100% would invite deleting a file that was
// never actually compared with what is being kept.
const noDirectSimilarity = -1.0

// componentConfidence reports the weakest pairwise similarity in the group.
// Pairs without a direct edge count as 0: a chain A≈B≈C (but A≉C) must not
// advertise the strength of its strongest link.
func componentConfidence(comp []int64, edgeSim map[[2]int64]float64) float64 {
	if len(comp) < 2 {
		return 0
	}
	minSim := 1.0
	for i := 0; i < len(comp); i++ {
		for j := i + 1; j < len(comp); j++ {
			sim := 0.0
			if s, ok := edgeSim[match.EdgeKey(comp[i], comp[j])]; ok {
				sim = s
			}
			if sim < minSim {
				minSim = sim
			}
		}
	}
	return minSim
}

// itemSimilarity returns how similar id is to the file that would be kept.
// Members without a direct edge to the recommendation return
// noDirectSimilarity so callers can show "no direct relation" instead of a
// fabricated 1.0.
func itemSimilarity(id, winner int64, edgeSim map[[2]int64]float64) float64 {
	if id == winner {
		return 1.0
	}
	if sim, ok := edgeSim[match.EdgeKey(id, winner)]; ok {
		return sim
	}
	return noDirectSimilarity
}

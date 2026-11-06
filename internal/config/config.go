package config

import (
	"path/filepath"
	"strings"
)

const (
	DefaultSimilarityThreshold     = 0.80
	DefaultWorkers                 = 1
	DefaultVideoWorkers            = 1
	DefaultFrameCount              = 8
	DefaultTextWorkers             = 2
	DefaultTextSimilarityThreshold = 0.92
	DefaultFrameFailureLimit       = 3
	DefaultReviewDelta             = 0.02
	MaxImagePixels                 = 100_000_000
	FFmpegTimeoutSeconds           = 30
	FFmpegThreads                  = 2

	// VideoModePrecise samples frames and compares perceptual hashes. It detects
	// re-encoded or trimmed copies but costs one FFmpeg seek per sampled frame.
	VideoModePrecise = "precise"
	// VideoModeSimple skips frame extraction entirely: videos are deduplicated by
	// content fingerprint only (byte-identical copies).
	VideoModeSimple = "simple"
	// DefaultVideoMode keeps the previous behaviour for existing projects.
	DefaultVideoMode = VideoModePrecise

	// DefaultSampleSegmentBytes is the size of each sampled content segment used
	// by the quick fingerprint (head, middle, tail).
	DefaultSampleSegmentBytes = 1 << 20
	// QuickTierMinSize is the file size below which the quick fingerprint is
	// skipped because a full hash is already cheap.
	QuickTierMinSize = 4 << 20
)

// NormalizeVideoMode maps arbitrary input to a supported mode.
func NormalizeVideoMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), VideoModeSimple) {
		return VideoModeSimple
	}
	return VideoModePrecise
}

var ImageExtensions = map[string]struct{}{
	".jpg": {}, ".jpeg": {}, ".png": {}, ".webp": {},
	".heic": {}, ".heif": {}, ".tiff": {}, ".tif": {},
	".bmp": {}, ".gif": {},
}

var VideoExtensions = map[string]struct{}{
	".mp4": {}, ".mov": {}, ".mkv": {}, ".avi": {},
	".webm": {}, ".m4v": {}, ".flv": {}, ".wmv": {},
	".mpeg": {}, ".mpg": {},
}

var TextExtensions = map[string]struct{}{".txt": {}}

var SkippedDirNames = map[string]struct{}{
	".git": {}, ".venv": {}, "__pycache__": {}, ".DS_Store": {},
	"node_modules": {},
}

// PHashExtensions are formats we can decode for perceptual hash / thumbs.
// HEIC/HEIF are exact-only in v1.
var PHashSkippedExtensions = map[string]struct{}{
	".heic": {}, ".heif": {},
}

// DefaultCacheDir matches AppRoot: Windows uses <program_dir>/media-dedupe-cache
// (visible, deletable after a project); macOS/other use ~/.media-dedupe.
func DefaultCacheDir() string {
	return AppRoot()
}

func DefaultCachePath() string {
	return filepath.Join(DefaultCacheDir(), "cache.sqlite")
}

func DefaultReportDir() string {
	return filepath.Join(DefaultCacheDir(), "reports")
}

func DefaultThumbDir() string {
	return filepath.Join(DefaultCacheDir(), "thumbs")
}

package config

import "path/filepath"

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
)

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

// DefaultCacheDir is the portable data root next to the program
// (<program_dir>/data). Scan caches stay visible and can be deleted
// after a project is finished.
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

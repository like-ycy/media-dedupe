package config

import (
	"os"
	"path/filepath"
	"runtime"
)

const (
	DefaultSimilarityThreshold = 0.80
	DefaultWorkers             = 2
	DefaultVideoWorkers        = 1
	DefaultFrameCount          = 8
	DefaultFrameFailureLimit   = 3
	DefaultReviewDelta         = 0.02
	MaxImagePixels             = 100_000_000
	FFmpegTimeoutSeconds       = 30
	ThumbLongEdge              = 480
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

var SkippedDirNames = map[string]struct{}{
	".git": {}, ".venv": {}, "__pycache__": {}, ".DS_Store": {},
	"node_modules": {},
}

// PHashExtensions are formats we can decode for perceptual hash / thumbs.
// HEIC/HEIF are exact-only in v1.
var PHashSkippedExtensions = map[string]struct{}{
	".heic": {}, ".heif": {},
}

func DefaultCacheDir() string {
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return ".media-dedupe"
		}
		return filepath.Join(home, "Library", "Caches", "media-dedupe")
	default:
		home, err := os.UserHomeDir()
		if err != nil {
			return ".media-dedupe"
		}
		if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
			return filepath.Join(xdg, "media-dedupe")
		}
		return filepath.Join(home, ".cache", "media-dedupe")
	}
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

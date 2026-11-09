package video

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"media-dedupe/internal/config"
	imgutil "media-dedupe/internal/imagehash"
	"media-dedupe/internal/model"
)

type probePayload struct {
	Streams []struct {
		CodecType    string `json:"codec_type"`
		CodecName    string `json:"codec_name"`
		Width        int    `json:"width"`
		Height       int    `json:"height"`
		AvgFrameRate string `json:"avg_frame_rate"`
		BitRate      string `json:"bit_rate"`
		Duration     string `json:"duration"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
		BitRate  string `json:"bit_rate"`
	} `json:"format"`
}

var ErrNoFrame = errors.New("no video frame at timestamp")

// ErrStorageIO marks a subprocess failure whose diagnostic points at the
// storage layer (failing volume, full disk, read-only mount) rather than at
// the media file. Callers treat it as a fatal scan error.
var ErrStorageIO = errors.New("storage i/o error")

// storageDiagnostics lists what ffmpeg/ffprobe print when the volume itself is
// failing. A process only reports a generic exit status, so the message text
// is the only signal available.
var storageDiagnostics = []string{
	"input/output error",
	"i/o error",
	"no space left on device",
	"disk full",
	"read-only file system",
	"too many open files",
	"quota exceeded",
	"device not configured",
	"device is not connected",
	"device not ready",
	"media is write protected",
}

// toolFailure keeps the diagnostic of a failed subprocess and marks
// storage-level problems so a dying disk is never reported as bad media.
func toolFailure(tool, stderr string, cause error) error {
	detail := diagnosticLine(stderr)
	if detail == "" {
		return fmt.Errorf("%s: %w", tool, cause)
	}
	if storage := storageDiagnosticLine(stderr); storage != "" {
		return fmt.Errorf("%s: %w: %s", tool, ErrStorageIO, storage)
	}
	return fmt.Errorf("%s: %w: %s", tool, cause, detail)
}

// diagnosticLine returns the first meaningful line of tool stderr.
func diagnosticLine(stderr string) string {
	for _, line := range strings.Split(stderr, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return truncateDiagnostic(trimmed)
		}
	}
	return ""
}

// storageDiagnosticLine returns the first stderr line that points at the
// storage layer, if any.
func storageDiagnosticLine(stderr string) string {
	for _, line := range strings.Split(stderr, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		for _, needle := range storageDiagnostics {
			if strings.Contains(lower, needle) {
				return truncateDiagnostic(trimmed)
			}
		}
	}
	return ""
}

func truncateDiagnostic(line string) string {
	const limit = 300
	runes := []rune(line)
	if len(runes) <= limit {
		return line
	}
	return string(runes[:limit]) + "…"
}

// ProbeMetadata runs ffprobe and parses video metadata.
func ProbeMetadata(path string) model.VideoMetadata {
	return ProbeMetadataContext(context.Background(), path)
}

func ProbeMetadataContext(parent context.Context, path string) model.VideoMetadata {
	meta, _ := ProbeMetadataContextE(parent, path)
	return meta
}

// ProbeMetadataContextE is ProbeMetadataContext with the underlying error, so
// callers can tell a corrupt file from a failing disk.
func ProbeMetadataContextE(parent context.Context, path string) (model.VideoMetadata, error) {
	ctx, cancel := context.WithTimeout(parent, config.FFmpegTimeoutSeconds*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffprobeBin(),
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path,
	)
	hideConsole(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return model.VideoMetadata{IsReadable: false}, ctx.Err()
		}
		return model.VideoMetadata{IsReadable: false}, toolFailure("ffprobe", stderr.String(), err)
	}
	var payload probePayload
	if err := json.Unmarshal(out, &payload); err != nil {
		return model.VideoMetadata{IsReadable: false}, fmt.Errorf("parse ffprobe output: %w", err)
	}

	var videoStream *struct {
		CodecType    string `json:"codec_type"`
		CodecName    string `json:"codec_name"`
		Width        int    `json:"width"`
		Height       int    `json:"height"`
		AvgFrameRate string `json:"avg_frame_rate"`
		BitRate      string `json:"bit_rate"`
		Duration     string `json:"duration"`
	}
	hasAudio := false
	for i := range payload.Streams {
		s := payload.Streams[i]
		switch s.CodecType {
		case "video":
			if videoStream == nil {
				videoStream = &payload.Streams[i]
			}
		case "audio":
			hasAudio = true
		}
	}
	if videoStream == nil {
		return model.VideoMetadata{IsReadable: false}, fmt.Errorf("no video stream")
	}

	durationSec := parseFloat(videoStream.Duration)
	if durationSec == 0 {
		durationSec = parseFloat(payload.Format.Duration)
	}
	var bitRate *int64
	if br := parseInt64(videoStream.BitRate); br != nil {
		bitRate = br
	} else if br := parseInt64(payload.Format.BitRate); br != nil {
		bitRate = br
	}
	var frameRate *float64
	if fr := parseFrameRate(videoStream.AvgFrameRate); fr != nil {
		frameRate = fr
	}

	return model.VideoMetadata{
		DurationMs: int64(durationSec * 1000),
		Width:      videoStream.Width,
		Height:     videoStream.Height,
		FrameRate:  frameRate,
		BitRate:    bitRate,
		Codec:      videoStream.CodecName,
		HasAudio:   hasAudio,
		IsReadable: true,
	}, nil
}

// BuildFrameTimestamps returns evenly spaced ms timestamps between 5%–95%.
func BuildFrameTimestamps(durationMs int64, frameCount int) []int64 {
	if durationMs <= 0 || frameCount <= 0 {
		return nil
	}
	if frameCount == 1 {
		return []int64{durationMs / 2}
	}
	start := durationMs * 5 / 100
	end := durationMs * 95 / 100
	if start >= end {
		return []int64{durationMs / 2}
	}
	step := float64(end-start) / float64(frameCount-1)
	timestamps := make([]int64, 0, frameCount)
	for i := 0; i < frameCount; i++ {
		timestamps = append(timestamps, start+int64(step*float64(i)+0.5))
	}
	return timestamps
}

// ExtractFrame writes a single JPEG frame at timestampMs.
func ExtractFrame(path string, timestampMs int64, outPath string) error {
	return ExtractFrameContext(context.Background(), path, timestampMs, outPath)
}

// ExtractFrameContext writes one frame using a fast input seek and honors cancellation.
func ExtractFrameContext(parent context.Context, path string, timestampMs int64, outPath string) error {
	if err := parent.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	// A successful seek past the last frame can write nothing. Do not mistake an
	// existing JPEG for output from this invocation.
	if err := os.Remove(outPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, config.FFmpegTimeoutSeconds*time.Second)
	defer cancel()
	sec := float64(timestampMs) / 1000.0
	cmd := exec.CommandContext(ctx, ffmpegBin(),
		"-y",
		"-v", "error",
		"-threads", strconv.Itoa(config.FFmpegThreads),
		"-ss", fmt.Sprintf("%.3f", sec),
		"-i", path,
		"-map", "0:v:0",
		"-an", "-sn", "-dn",
		"-filter_threads", "1",
		"-threads", strconv.Itoa(config.FFmpegThreads),
		"-frames:v", "1",
		"-pix_fmt", "yuvj420p",
		"-q:v", "2",
		outPath,
	)
	hideConsole(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(outPath)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return toolFailure("ffmpeg", stderr.String(), err)
	}
	if info, err := os.Stat(outPath); err != nil {
		if os.IsNotExist(err) {
			return ErrNoFrame
		}
		return fmt.Errorf("frame not written")
	} else if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("frame is not a nonempty regular file")
	}
	return nil
}

const frameHashTypePrefix = "video_frame_phash_v2_"

// FrameHashType returns the cache key for the current extraction algorithm and frame count.
func FrameHashType(frameCount int) string {
	return fmt.Sprintf("%s%d", frameHashTypePrefix, frameCount)
}

// ComputeFrameHashes extracts frames, skips blanks, returns pHashes.
func ComputeFrameHashes(path string, meta model.VideoMetadata, frameCount int, failureLimit int, tmpDir string) ([]string, error) {
	framePaths, err := ExtractFrames(path, meta, frameCount, tmpDir)
	if err != nil {
		return nil, err
	}
	defer func() {
		for _, path := range framePaths {
			_ = os.Remove(path)
		}
	}()
	return HashFrames(context.Background(), framePaths, failureLimit)
}

// HashFrames hashes the extracted images without removing them, so previews can reuse them.
func HashFrames(ctx context.Context, framePaths []string, failureLimit int) ([]string, error) {
	var hashes []string
	failed := 0
	for _, framePath := range framePaths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if framePath == "" {
			continue
		}
		img, err := imgutil.LoadImage(framePath)
		if err != nil {
			failed++
			if failed >= failureLimit {
				break
			}
			continue
		}
		if imgutil.IsBlankFrame(img) {
			continue
		}
		hash, err := imgutil.PerceptualHash(img)
		if err != nil {
			failed++
			if failed >= failureLimit {
				break
			}
			continue
		}
		hashes = append(hashes, hash)
	}
	if len(hashes) == 0 {
		return nil, fmt.Errorf("no usable frames extracted")
	}
	return hashes, nil
}

// ExtractFrames writes evenly spaced JPEG frames using independent fast seeks.
func ExtractFrames(path string, meta model.VideoMetadata, frameCount int, tmpDir string) ([]string, error) {
	return ExtractFramesContext(context.Background(), path, meta, frameCount, tmpDir)
}

// ExtractFramesContext preserves timestamp positions; empty entries mean no frame exists there.
func ExtractFramesContext(parent context.Context, path string, meta model.VideoMetadata, frameCount int, tmpDir string) ([]string, error) {
	if meta.DurationMs <= 0 || frameCount <= 0 {
		return nil, fmt.Errorf("invalid frame extraction parameters")
	}
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, err
	}
	timestamps := BuildFrameTimestamps(meta.DurationMs, frameCount)
	if len(timestamps) == 0 {
		return nil, fmt.Errorf("no frame timestamps")
	}
	paths := make([]string, 0, len(timestamps))
	for i, timestamp := range timestamps {
		if err := parent.Err(); err != nil {
			return nil, err
		}
		framePath := filepath.Join(tmpDir, fmt.Sprintf("frame-%03d.jpg", i+1))
		if err := ExtractFrameContext(parent, path, timestamp, framePath); err != nil {
			if errors.Is(err, ErrNoFrame) {
				paths = append(paths, "")
				continue
			}
			return nil, fmt.Errorf("extract frame %d: %w", i+1, err)
		}
		paths = append(paths, framePath)
	}
	return paths, nil
}

func parseFrameRate(value string) *float64 {
	if value == "" || value == "0/0" {
		return nil
	}
	if strings.Contains(value, "/") {
		parts := strings.SplitN(value, "/", 2)
		num, err1 := strconv.ParseFloat(parts[0], 64)
		den, err2 := strconv.ParseFloat(parts[1], 64)
		if err1 != nil || err2 != nil || den == 0 {
			return nil
		}
		v := num / den
		return &v
	}
	v, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return nil
	}
	return &v
}

func parseFloat(s string) float64 {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

func parseInt64(s string) *int64 {
	if s == "" {
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil
	}
	return &v
}

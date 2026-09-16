package video

import (
	"context"
	"encoding/json"
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

// ProbeMetadata runs ffprobe and parses video metadata.
func ProbeMetadata(path string) model.VideoMetadata {
	ctx, cancel := context.WithTimeout(context.Background(), config.FFmpegTimeoutSeconds*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffprobeBin(),
		"-v", "error",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return model.VideoMetadata{IsReadable: false}
	}
	var payload probePayload
	if err := json.Unmarshal(out, &payload); err != nil {
		return model.VideoMetadata{IsReadable: false}
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
		return model.VideoMetadata{IsReadable: false}
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
	}
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
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), config.FFmpegTimeoutSeconds*time.Second)
	defer cancel()
	sec := float64(timestampMs) / 1000.0
	cmd := exec.CommandContext(ctx, ffmpegBin(),
		"-y",
		"-ss", fmt.Sprintf("%.3f", sec),
		"-i", path,
		"-frames:v", "1",
		"-q:v", "2",
		outPath,
	)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("extract frame: %w", err)
	}
	if _, err := os.Stat(outPath); err != nil {
		return fmt.Errorf("frame not written")
	}
	return nil
}

// FrameHashResult holds one frame hash outcome.
type FrameHashResult struct {
	Hash string
	Skip bool
}

// ComputeFrameHashes extracts frames, skips blanks, returns pHashes.
func ComputeFrameHashes(path string, meta model.VideoMetadata, frameCount int, failureLimit int, tmpDir string) ([]string, error) {
	if meta.DurationMs <= 0 {
		return nil, fmt.Errorf("invalid duration")
	}
	framePaths, err := ExtractFrames(path, meta, frameCount, tmpDir)
	if err != nil {
		return nil, err
	}
	var hashes []string
	failed := 0
	for _, framePath := range framePaths {
		img, err := imgutil.LoadImage(framePath)
		if err != nil {
			failed++
			_ = os.Remove(framePath)
			if failed >= failureLimit {
				break
			}
			continue
		}
		if imgutil.IsBlankFrame(img) {
			_ = os.Remove(framePath)
			continue
		}
		hash, err := imgutil.PerceptualHash(img)
		_ = os.Remove(framePath)
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

// ExtractFrames writes evenly spaced JPEG frames with one FFmpeg process.
func ExtractFrames(path string, meta model.VideoMetadata, frameCount int, tmpDir string) ([]string, error) {
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
	args := []string{"-y", "-ss", fmt.Sprintf("%.3f", float64(timestamps[0])/1000), "-i", path}
	if len(timestamps) > 1 {
		span := float64(timestamps[len(timestamps)-1]-timestamps[0]) / 1000
		fps := float64(len(timestamps)) / span
		args = append(args, "-t", fmt.Sprintf("%.3f", span), "-vf", fmt.Sprintf("fps=%.6f", fps))
	}
	args = append(args, "-an", "-frames:v", strconv.Itoa(len(timestamps)), "-q:v", "2", filepath.Join(tmpDir, "frame-%03d.jpg"))
	ctx, cancel := context.WithTimeout(context.Background(), config.FFmpegTimeoutSeconds*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, ffmpegBin(), args...).Run(); err != nil {
		return nil, fmt.Errorf("extract frames: %w", err)
	}
	paths := make([]string, 0, len(timestamps))
	for i := 1; i <= len(timestamps); i++ {
		framePath := filepath.Join(tmpDir, fmt.Sprintf("frame-%03d.jpg", i))
		if _, err := os.Stat(framePath); err == nil {
			paths = append(paths, framePath)
		}
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no frames extracted")
	}
	return paths, nil
}

// ExtractThumbFrame extracts one mid frame and writes a thumbnail.
func ExtractThumbFrame(path string, meta model.VideoMetadata, outPath string, longEdge int) error {
	ts := meta.DurationMs / 2
	tmp, err := os.MkdirTemp("", "media-dedupe-thumb-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	framePath := filepath.Join(tmp, "thumb.jpg")
	if err := ExtractFrame(path, ts, framePath); err != nil {
		return err
	}
	img, err := imgutil.LoadImage(framePath)
	if err != nil {
		return err
	}
	return imgutil.WriteThumb(img, outPath, longEdge)
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

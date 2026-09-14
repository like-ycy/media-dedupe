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

// Available reports whether ffmpeg and ffprobe exist on PATH.
func Available() (ffmpeg, ffprobe bool) {
	_, errFfmpeg := exec.LookPath("ffmpeg")
	_, errFfprobe := exec.LookPath("ffprobe")
	return errFfmpeg == nil, errFfprobe == nil
}

// ProbeMetadata runs ffprobe and parses video metadata.
func ProbeMetadata(path string) model.VideoMetadata {
	ctx, cancel := context.WithTimeout(context.Background(), config.FFmpegTimeoutSeconds*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffprobe",
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
	cmd := exec.CommandContext(ctx, "ffmpeg",
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
	timestamps := BuildFrameTimestamps(meta.DurationMs, frameCount)
	var hashes []string
	failed := 0
	for i, ts := range timestamps {
		framePath := filepath.Join(tmpDir, fmt.Sprintf("frame-%d.jpg", i))
		if err := ExtractFrame(path, ts, framePath); err != nil {
			failed++
			if failed >= failureLimit {
				break
			}
			continue
		}
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

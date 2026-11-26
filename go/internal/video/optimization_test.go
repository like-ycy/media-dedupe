package video

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
)

func BenchmarkSparseVideoExtraction(b *testing.B) {
	ffmpeg, ffprobe := Tools()
	if ffmpeg == "" || ffprobe == "" {
		b.Skip("FFmpeg is required")
	}
	input := filepath.Join(b.TempDir(), "sample.mp4")
	cmd := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=duration=240:size=640x360:rate=24", "-c:v", "libx264", "-preset", "ultrafast", "-threads", "2", "-g", "48", input)
	if out, err := cmd.CombinedOutput(); err != nil {
		b.Fatalf("generate video: %v %s", err, out)
	}
	meta := ProbeMetadata(input)
	timestamps := BuildFrameTimestamps(meta.DurationMs, 8)
	span := float64(timestamps[len(timestamps)-1]-timestamps[0]) / 1000
	for _, name := range []string{"continuous_fps", "sparse_seek"} {
		b.Run(name, func(b *testing.B) {
			dir := b.TempDir()
			for b.Loop() {
				if name == "sparse_seek" {
					if _, err := ExtractFramesContext(context.Background(), input, meta, 8, dir); err != nil {
						b.Fatal(err)
					}
					continue
				}
				cmd := exec.Command(ffmpeg, "-v", "error", "-y", "-ss", fmt.Sprintf("%.3f", float64(timestamps[0])/1000), "-i", input,
					"-t", fmt.Sprintf("%.3f", span), "-vf", fmt.Sprintf("fps=%.6f", 8/span), "-an", "-frames:v", "8", "-q:v", "2", filepath.Join(dir, "frame-%03d.jpg"))
				if out, err := cmd.CombinedOutput(); err != nil {
					b.Fatalf("old extraction: %v %s", err, out)
				}
			}
		})
	}
}

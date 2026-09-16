// Command generate_testdata creates a mixed media corpus for manual scans.
// Usage: go run ./scripts/generate_testdata.go [-count 3000] [-out ./testdata]
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

func main() {
	count := flag.Int("count", 3000, "total files, approximately")
	out := flag.String("out", "./testdata", "output directory")
	flag.Parse()
	if *count < 30 {
		*count = 30
	}
	if err := os.RemoveAll(*out); err != nil {
		fail(err)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail(err)
	}

	images := *count * 45 / 100
	texts := *count * 45 / 100
	videos := *count - images - texts
	if err := makeImages(filepath.Join(*out, "images"), images); err != nil {
		fail(err)
	}
	if err := makeTexts(filepath.Join(*out, "texts"), texts); err != nil {
		fail(err)
	}
	if err := makeVideos(filepath.Join(*out, "videos"), videos); err != nil {
		fail(err)
	}
	fmt.Printf("generated %d files in %s (images=%d texts=%d videos=%d)\n", *count, *out, images, texts, videos)
}

func makeImages(dir string, n int) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	base := image.NewRGBA(image.Rect(0, 0, 320, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 320; x++ {
			base.SetRGBA(x, y, color.RGBA{uint8(x * 255 / 319), uint8(y * 180 / 199), 110, 255})
		}
	}
	for i := 0; i < n; i++ {
		path := filepath.Join(dir, fmt.Sprintf("image-%04d.png", i))
		if i%10 == 0 {
			if err := writePNG(path, base); err != nil {
				return err
			}
			continue
		}
		if i%10 == 1 {
			if err := copyFile(filepath.Join(dir, fmt.Sprintf("image-%04d.png", i-1)), path); err != nil {
				return err
			}
			continue
		}
		img := base
		if i%10 == 2 {
			img = image.NewRGBA(base.Bounds())
			img.Set(10, 10, color.RGBA{255, 0, 0, 255})
		}
		if i%10 == 2 {
			for y := 0; y < 200; y++ {
				for x := 0; x < 320; x++ {
					if x != 10 || y != 10 {
						img.Set(x, y, base.At(x, y))
					}
				}
			}
		}
		if err := writePNG(path, img); err != nil {
			return err
		}
	}
	return nil
}

func makeTexts(dir string, n int) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for i := 0; i < n; i++ {
		path := filepath.Join(dir, fmt.Sprintf("note-%04d.txt", i))
		group := i / 10
		content := fmt.Sprintf("Media dedupe test document %d.\nThis shared paragraph belongs to duplicate group %d.\n", group, group)
		if i%10 == 1 {
			content = fmt.Sprintf("Media dedupe test document %d.\nThis shared paragraph belongs to duplicate group %d.\n", group, group)
		}
		if i%10 == 2 {
			content += "One small extra sentence tests near-duplicate text matching.\n"
		}
		content += fmt.Sprintf("Group marker: %04d\n", group)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func makeVideos(dir string, n int) error {
	if n == 0 {
		return nil
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("ffmpeg is required to generate videos: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	unique := (n + 9) / 10
	for i := 0; i < unique; i++ {
		path := filepath.Join(dir, fmt.Sprintf("video-%04d.mp4", i))
		cmd := exec.Command("ffmpeg", "-y", "-loglevel", "error", "-f", "lavfi", "-i", fmt.Sprintf("testsrc=size=320x200:rate=10:duration=1.2"), "-pix_fmt", "yuv420p", "-c:v", "libx264", path)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("ffmpeg: %s: %w", out, err)
		}
	}
	for i := unique; i < n; i++ {
		if err := copyFile(filepath.Join(dir, fmt.Sprintf("video-%04d.mp4", i%unique)), filepath.Join(dir, fmt.Sprintf("video-%04d.mp4", i))); err != nil {
			return err
		}
	}
	return nil
}

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }

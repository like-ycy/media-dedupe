package pipeline

import (
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"media-dedupe/internal/model"
	"media-dedupe/internal/video"
)

// blobImage builds a deterministic low-frequency picture. Its pHash survives
// JPEG/PNG re-encodings unchanged, while a different seed stays far away, so the
// tests exercise format-independent comparison rather than decoder noise.
func blobImage(width, height int, seed int64) image.Image {
	random := rand.New(rand.NewSource(seed))
	const gridWidth, gridHeight = 6, 5
	grid := make([][]float64, gridHeight)
	for y := range grid {
		grid[y] = make([]float64, gridWidth)
		for x := range grid[y] {
			grid[y][x] = random.Float64() * 255
		}
	}
	sample := func(fx, fy float64) float64 {
		gx, gy := fx*float64(gridWidth-1), fy*float64(gridHeight-1)
		x0, y0 := int(gx), int(gy)
		if x0 > gridWidth-2 {
			x0 = gridWidth - 2
		}
		if y0 > gridHeight-2 {
			y0 = gridHeight - 2
		}
		tx, ty := gx-float64(x0), gy-float64(y0)
		top := grid[y0][x0]*(1-tx) + grid[y0][x0+1]*tx
		bottom := grid[y0+1][x0]*(1-tx) + grid[y0+1][x0+1]*tx
		return top*(1-ty) + bottom*ty
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			value := sample(float64(x)/float64(width), float64(y)/float64(height))
			img.Set(x, y, color.RGBA{R: uint8(value), G: uint8(255 - value), B: uint8(value/2 + 60), A: 255})
		}
	}
	return img
}

func writePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, img); err != nil {
		t.Fatal(err)
	}
}

func writeJPEG(t *testing.T, path string, img image.Image, quality int) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := jpeg.Encode(file, img, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatal(err)
	}
}

func scanDir(t *testing.T, dir string, opts Options) *Result {
	t.Helper()
	opts.Paths = []string{dir}
	if opts.CachePath == "" {
		opts.CachePath = filepath.Join(t.TempDir(), "cache.sqlite")
	}
	opts.Workers = 1
	opts.VideoWorkers = 1
	if opts.FrameCount == 0 {
		opts.FrameCount = 8
	}
	result, err := Scan(opts)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return result
}

// TestExactDuplicateAcrossImageExtensions covers the rename case: the same
// bytes stored as .jpg and .jpeg must land in one exact group.
func TestExactDuplicateAcrossImageExtensions(t *testing.T) {
	root := t.TempDir()
	writeJPEG(t, filepath.Join(root, "a.jpg"), blobImage(160, 120, 7), 90)
	// Copy the exact bytes to a second extension.
	data, err := os.ReadFile(filepath.Join(root, "a.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.jpeg"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	result := scanDir(t, root, Options{IncludeImages: true})
	if len(result.Groups) != 1 {
		t.Fatalf("expected one group, got %+v", result.Groups)
	}
	group := result.Groups[0]
	if group.GroupType != model.GroupExact || len(group.Items) != 2 {
		t.Fatalf("expected one exact group with two members, got %+v", group)
	}
}

// TestSimilarImageAcrossExtensions covers the re-encode case: the same picture
// as .jpg and .png must be compared and grouped.
func TestSimilarImageAcrossExtensions(t *testing.T) {
	root := t.TempDir()
	img := blobImage(240, 180, 7)
	writeJPEG(t, filepath.Join(root, "b.jpg"), img, 92)
	writePNG(t, filepath.Join(root, "b.png"), img)

	result := scanDir(t, root, Options{IncludeImages: true})
	if len(result.Groups) != 1 {
		t.Fatalf("expected one group, got %+v", result.Groups)
	}
	group := result.Groups[0]
	if group.GroupType != model.GroupSimilarImage || len(group.Items) != 2 {
		t.Fatalf("expected one similar-image group with two members, got %+v", group)
	}
}

// TestUnrelatedSameSizeImagesAcrossExtensions is the reverse case: sharing
// dimensions and media type is not evidence of a duplicate.
func TestUnrelatedSameSizeImagesAcrossExtensions(t *testing.T) {
	root := t.TempDir()
	writeJPEG(t, filepath.Join(root, "x.jpg"), blobImage(200, 150, 7), 90)
	writePNG(t, filepath.Join(root, "y.png"), blobImage(200, 150, 99))

	result := scanDir(t, root, Options{IncludeImages: true})
	if len(result.Groups) != 0 {
		t.Fatalf("unrelated images must not group, got %+v", result.Groups)
	}
}

// TestExactCopyDoesNotHideSimilarity covers P0-2: A≈B, plus a byte-identical
// copy A′ of A, must still report one group holding all three files.
func TestExactCopyDoesNotHideSimilarity(t *testing.T) {
	root := t.TempDir()
	img := blobImage(240, 180, 7)
	original := filepath.Join(root, "a.jpg")
	writeJPEG(t, original, img, 92)
	data, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	// Byte-identical copy with the same extension, so size+type match.
	if err := os.WriteFile(filepath.Join(root, "a-copy.jpg"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	// Visually near-identical but re-encoded, so it only matches by pHash.
	writeJPEG(t, filepath.Join(root, "b.jpg"), img, 55)

	result := scanDir(t, root, Options{IncludeImages: true})
	if len(result.Groups) != 1 {
		t.Fatalf("expected exactly one fused group, got %+v", result.Groups)
	}
	group := result.Groups[0]
	if len(group.Items) != 3 {
		t.Fatalf("expected three members, got %+v", group.Items)
	}
	if group.GroupType != model.GroupSimilarImage {
		t.Fatalf("fused group should keep the similarity type, got %s", group.GroupType)
	}
	reasons := map[string]string{}
	for _, item := range group.Items {
		reasons[filepath.Base(item.Path)] = strings.Join(item.Reasons, "; ")
	}
	if !strings.Contains(reasons["a-copy.jpg"], "identical") {
		t.Fatalf("byte-identical copy lost its exact reason: %q", reasons["a-copy.jpg"])
	}
	if !strings.Contains(reasons["b.jpg"], "visual image hash") {
		t.Fatalf("similar member lost its similarity reason: %q", reasons["b.jpg"])
	}
	keeps := 0
	for _, item := range group.Items {
		if item.FileID == group.RecommendedFileID {
			keeps++
		}
	}
	if keeps != 1 {
		t.Fatalf("expected exactly one recommended file, got %d", keeps)
	}
}

// TestExactPairWithoutSimilarityKeepsExactGroup is the control case: no
// near-duplicate means the exact group is reported unchanged.
func TestExactPairWithoutSimilarityKeepsExactGroup(t *testing.T) {
	root := t.TempDir()
	img := blobImage(200, 150, 7)
	original := filepath.Join(root, "only.jpg")
	writeJPEG(t, original, img, 90)
	data, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "only-copy.jpg"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	result := scanDir(t, root, Options{IncludeImages: true})
	if len(result.Groups) != 1 {
		t.Fatalf("expected one group, got %+v", result.Groups)
	}
	group := result.Groups[0]
	if group.GroupType != model.GroupExact || len(group.Items) != 2 {
		t.Fatalf("expected a pure exact group, got %+v", group)
	}
}

// TestVideoContainerChangeReachesComparison covers the remux case: the same
// stream in another container must survive candidate filtering.
func TestVideoContainerChangeReachesComparison(t *testing.T) {
	ffmpeg, _ := video.Tools()
	if ffmpeg == "" {
		t.Skip("FFmpeg is required")
	}
	root := t.TempDir()
	input := filepath.Join(root, "c.mp4")
	cmd := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=duration=8:size=320x180:rate=12", "-c:v", "libx264", "-preset", "ultrafast", "-threads", "2", "-g", "24", input)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate video: %v %s", err, out)
	}
	remuxed := filepath.Join(root, "c.mkv")
	cmd = exec.Command(ffmpeg, "-v", "error", "-i", input, "-c", "copy", remuxed)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("remux video: %v %s", err, out)
	}

	result := scanDir(t, root, Options{IncludeVideos: true})
	if len(result.Groups) != 1 {
		t.Fatalf("expected one video group, got %+v", result.Groups)
	}
	group := result.Groups[0]
	if group.GroupType != model.GroupSimilarVideo || len(group.Items) != 2 {
		t.Fatalf("expected one similar-video group with two members, got %+v", group)
	}
}

// TestBlankVideoKeepsExactGroup covers a video whose sampled frames are all
// blank: the extractor produces nothing hashable, but SHA-256 already proved the
// two files are identical. Having no approximate features must not turn that
// into a processing failure that removes the recommended file and dissolves the
// group.
func TestBlankVideoKeepsExactGroup(t *testing.T) {
	ffmpeg, _ := video.Tools()
	if ffmpeg == "" {
		t.Skip("FFmpeg is required")
	}
	root := t.TempDir()
	input := filepath.Join(root, "black.mp4")
	cmd := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "color=c=black:duration=4:size=320x180:rate=10", "-c:v", "libx264", "-preset", "ultrafast", "-threads", "2", input)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate video: %v %s", err, out)
	}
	data, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "black-copy.mp4"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	result := scanDir(t, root, Options{IncludeVideos: true})
	if len(result.Groups) != 1 {
		t.Fatalf("expected one group, got %+v", result.Groups)
	}
	group := result.Groups[0]
	if group.GroupType != model.GroupExact || len(group.Items) != 2 {
		t.Fatalf("expected a pure exact group with two members, got %+v", group)
	}
}

// TestShortVideoKeepsExactGroup covers a video shorter than the sampling window:
// every sampled timestamp lands past its only frame, so no frame can be
// compared. That is not a read failure and must not dissolve the exact group
// SHA-256 already confirmed.
func TestShortVideoKeepsExactGroup(t *testing.T) {
	ffmpeg, _ := video.Tools()
	if ffmpeg == "" {
		t.Skip("FFmpeg is required")
	}
	root := t.TempDir()
	input := filepath.Join(root, "single.mp4")
	cmd := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=1", "-frames:v", "1", "-c:v", "libx264", "-preset", "ultrafast", "-threads", "2", input)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate video: %v %s", err, out)
	}
	data, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "single-copy.mp4"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	result := scanDir(t, root, Options{IncludeVideos: true, FrameCount: 8})
	if len(result.Groups) != 1 {
		t.Fatalf("expected one group, got %+v", result.Groups)
	}
	group := result.Groups[0]
	if group.GroupType != model.GroupExact || len(group.Items) != 2 {
		t.Fatalf("expected a pure exact group with two members, got %+v", group)
	}
}

// TestUnsupportedFormatGroupKeepsSimilarity covers a group whose recommended
// file cannot be hashed: the same JPEG bytes stored as .heic is exact-only, but
// its identical .jpg copy has to keep looking for near-duplicates. Holding the
// whole group back would silently lose the similar .jpg next to it.
func TestUnsupportedFormatGroupKeepsSimilarity(t *testing.T) {
	root := t.TempDir()
	writeJPEG(t, filepath.Join(root, "a.jpg"), blobImage(200, 150, 11), 90)
	data, err := os.ReadFile(filepath.Join(root, "a.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	// Same bytes under a format we cannot compute pHash for, and a re-encoded
	// near-duplicate that only the approximate stage can find.
	if err := os.WriteFile(filepath.Join(root, "a.heic"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	writeJPEG(t, filepath.Join(root, "c.jpg"), blobImage(200, 150, 11), 60)

	result := scanDir(t, root, Options{IncludeImages: true})
	if len(result.Groups) != 1 {
		t.Fatalf("expected one group, got %+v", result.Groups)
	}
	group := result.Groups[0]
	if len(group.Items) != 3 {
		t.Fatalf("expected three members, got %+v", group.Items)
	}
	names := map[string]bool{}
	for _, item := range group.Items {
		names[filepath.Base(item.Path)] = true
	}
	if !names["c.jpg"] {
		t.Fatalf("the similar jpg must stay reported, got %+v", names)
	}
}

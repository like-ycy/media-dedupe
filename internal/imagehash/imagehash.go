package imagehash

import (
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/corona10/goimagehash"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"

	"media-dedupe/internal/config"
	"media-dedupe/internal/model"
)

// ReadMetadata loads image dimensions and format.
func ReadMetadata(path string) model.ImageMetadata {
	f, err := os.Open(path)
	if err != nil {
		return model.ImageMetadata{IsReadable: false}
	}
	defer f.Close()

	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		return model.ImageMetadata{
			FormatName: strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), "."),
			IsReadable: false,
		}
	}
	if cfg.Width*cfg.Height > config.MaxImagePixels {
		return model.ImageMetadata{IsReadable: false}
	}
	return model.ImageMetadata{
		Width:      cfg.Width,
		Height:     cfg.Height,
		FormatName: strings.ToUpper(format),
		IsReadable: true,
	}
}

// LoadImage decodes a full image with pixel budget check.
func LoadImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	b := img.Bounds()
	if b.Dx()*b.Dy() > config.MaxImagePixels {
		return nil, fmt.Errorf("image exceeds pixel limit")
	}
	return img, nil
}

// PerceptualHash computes pHash as 16 lowercase hex chars (64-bit).
func PerceptualHash(img image.Image) (string, error) {
	hash, err := goimagehash.PerceptionHash(img)
	if err != nil {
		return "", err
	}
	// goimagehash.ToString() prefixes "p:"; store pure hex for distance math.
	// Bits() is the bit-width (always 64), GetHash() is the actual value.
	return fmt.Sprintf("%016x", hash.GetHash()), nil
}

// ComputePHashFromFile loads and hashes an image file.
func ComputePHashFromFile(path string) (string, error) {
	img, err := LoadImage(path)
	if err != nil {
		return "", err
	}
	return PerceptualHash(img)
}

// WriteThumb writes a long-edge thumbnail as JPEG.
func WriteThumb(img image.Image, outPath string, longEdge int) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	thumb := resizeLongEdge(img, longEdge)
	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer out.Close()
	// JPEG keeps thumbs small and is widely supported in HTML.
	return encodeJPEG(out, thumb)
}

// ThumbnailFromPath writes a thumbnail for a source image path.
func ThumbnailFromPath(srcPath, outPath string, longEdge int) error {
	img, err := LoadImage(srcPath)
	if err != nil {
		return err
	}
	return WriteThumb(img, outPath, longEdge)
}

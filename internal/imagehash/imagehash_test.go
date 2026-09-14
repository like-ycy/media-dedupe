package imagehash

import (
	"image"
	"image/color"
	"testing"

	"media-dedupe/internal/hashfile"
)

func gradientImage(w, h int, seed int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8((x*3 + seed) % 256),
				G: uint8((y*5 + seed*2) % 256),
				B: uint8((x + y + seed*3) % 256),
				A: 255,
			})
		}
	}
	return img
}

func TestPerceptualHashNotConstant(t *testing.T) {
	a, err := PerceptualHash(gradientImage(64, 64, 1))
	if err != nil {
		t.Fatal(err)
	}
	b, err := PerceptualHash(gradientImage(64, 64, 2))
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("different gradients produced identical phash %s", a)
	}
	if a == "0000000000000040" || b == "0000000000000040" {
		t.Fatalf("phash looks like Bits() misuse: a=%s b=%s", a, b)
	}
}

func TestPerceptualHashStableAndSimilar(t *testing.T) {
	base := blockImage(80, 60)
	h1, err := PerceptualHash(base)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := PerceptualHash(base)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("same image phash not stable: %s vs %s", h1, h2)
	}

	// Mild brightness change on a low-frequency image should stay close.
	bright := image.NewRGBA(base.Bounds())
	for y := 0; y < 60; y++ {
		for x := 0; x < 80; x++ {
			r, g, b, _ := base.At(x, y).RGBA()
			bright.SetRGBA(x, y, color.RGBA{
				R: uint8(min255(int(r>>8) + 8)),
				G: uint8(min255(int(g>>8) + 8)),
				B: uint8(min255(int(b>>8) + 8)),
				A: 255,
			})
		}
	}
	h3, err := PerceptualHash(bright)
	if err != nil {
		t.Fatal(err)
	}
	sim, err := hashfile.Similarity(h1, h3)
	if err != nil {
		t.Fatal(err)
	}
	if sim < 0.8 {
		t.Fatalf("brightness tweak too dissimilar: sim=%v h1=%s h3=%s", sim, h1, h3)
	}

	other, err := PerceptualHash(gradientImage(80, 60, 99))
	if err != nil {
		t.Fatal(err)
	}
	otherSim, err := hashfile.Similarity(h1, other)
	if err != nil {
		t.Fatal(err)
	}
	if otherSim >= sim {
		t.Fatalf("unrelated image should be less similar: other=%v near=%v", otherSim, sim)
	}
}

// blockImage is low-frequency so pHash is stable under mild tone shifts.
func blockImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var c color.RGBA
			switch {
			case x < w/2 && y < h/2:
				c = color.RGBA{R: 200, G: 40, B: 40, A: 255}
			case x >= w/2 && y < h/2:
				c = color.RGBA{R: 40, G: 200, B: 40, A: 255}
			case x < w/2:
				c = color.RGBA{R: 40, G: 40, B: 200, A: 255}
			default:
				c = color.RGBA{R: 220, G: 220, B: 40, A: 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func min255(v int) int {
	if v > 255 {
		return 255
	}
	if v < 0 {
		return 0
	}
	return v
}

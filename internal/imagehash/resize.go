package imagehash

import (
	"image"
	"image/color"
	"image/draw"
	"math"
)

// MeanLuma returns average 0..255 luminance and variance-ish spread.
func MeanLuma(img image.Image) (mean float64, stddev float64) {
	b := img.Bounds()
	var sum, sumSq float64
	var n float64
	// Sample up to ~40k pixels for speed.
	stepX, stepY := 1, 1
	if b.Dx() > 200 {
		stepX = b.Dx() / 200
	}
	if b.Dy() > 200 {
		stepY = b.Dy() / 200
	}
	if stepX < 1 {
		stepX = 1
	}
	if stepY < 1 {
		stepY = 1
	}
	for y := b.Min.Y; y < b.Max.Y; y += stepY {
		for x := b.Min.X; x < b.Max.X; x += stepX {
			r, g, bl, _ := img.At(x, y).RGBA()
			luma := 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(bl>>8)
			sum += luma
			sumSq += luma * luma
			n++
		}
	}
	if n == 0 {
		return 0, 0
	}
	mean = sum / n
	variance := sumSq/n - mean*mean
	if variance < 0 {
		variance = 0
	}
	return mean, math.Sqrt(variance)
}

// IsBlankFrame reports near-black or near-constant frames.
func IsBlankFrame(img image.Image) bool {
	mean, stddev := MeanLuma(img)
	return mean < 18 || stddev < 8
}

// Ensure RGB helper used by tests.
func toRGBA(img image.Image) *image.RGBA {
	if rgba, ok := img.(*image.RGBA); ok {
		return rgba
	}
	b := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
	return dst
}

var _ = color.RGBA{}

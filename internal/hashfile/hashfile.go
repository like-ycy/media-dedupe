package hashfile

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// SHA256File streams a file through SHA-256.
func SHA256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// HammingDistance returns bit difference of two equal-length hex hashes.
func HammingDistance(leftHex, rightHex string) (int, error) {
	if len(leftHex) != len(rightHex) {
		return 0, fmt.Errorf("hashes must have the same length")
	}
	distance := 0
	for i := 0; i < len(leftHex); i++ {
		a := unhex(leftHex[i])
		b := unhex(rightHex[i])
		if a < 0 || b < 0 {
			return 0, fmt.Errorf("invalid hex hash")
		}
		x := a ^ b
		for x != 0 {
			distance += x & 1
			x >>= 1
		}
	}
	return distance, nil
}

// Similarity converts hamming distance to 0..1 similarity.
func Similarity(leftHex, rightHex string) (float64, error) {
	if len(leftHex) == 0 {
		return 0, fmt.Errorf("hashes must not be empty")
	}
	distance, err := HammingDistance(leftHex, rightHex)
	if err != nil {
		return 0, err
	}
	bitCount := len(leftHex) * 4
	sim := 1.0 - float64(distance)/float64(bitCount)
	if sim < 0 {
		sim = 0
	}
	return sim, nil
}

func unhex(b byte) int {
	switch {
	case b >= '0' && b <= '9':
		return int(b - '0')
	case b >= 'a' && b <= 'f':
		return int(b-'a') + 10
	case b >= 'A' && b <= 'F':
		return int(b-'A') + 10
	default:
		return -1
	}
}

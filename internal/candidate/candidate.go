package candidate

import (
	"sort"

	"media-dedupe/internal/hashfile"
)

// ImageBucketKey quantizes width/height for candidate bucketing (±8px).
func ImageBucketKey(width, height int) (int, int) {
	const q = 8
	w := (width + q/2) / q * q
	h := (height + q/2) / q * q
	if w < q {
		w = q
	}
	if h < q {
		h = q
	}
	return w, h
}

// ImageCandidate pairs images that share a dimension bucket.
func ImageCandidates(items []ImageCandidateItem) [][2]int {
	type bucket struct {
		idxs []int
	}
	buckets := map[[2]int]*bucket{}
	for i, item := range items {
		bw, bh := ImageBucketKey(item.Width, item.Height)
		key := [2]int{bw, bh}
		b := buckets[key]
		if b == nil {
			b = &bucket{}
			buckets[key] = b
		}
		b.idxs = append(b.idxs, i)
	}
	var pairs [][2]int
	for _, b := range buckets {
		if len(b.idxs) < 2 {
			continue
		}
		// Also compare neighboring quantized sizes by expanding search
		// within the same bucket first (cheap).
		for i := 0; i < len(b.idxs); i++ {
			for j := i + 1; j < len(b.idxs); j++ {
				pairs = append(pairs, [2]int{b.idxs[i], b.idxs[j]})
			}
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i][0] != pairs[j][0] {
			return pairs[i][0] < pairs[j][0]
		}
		return pairs[i][1] < pairs[j][1]
	})
	return pairs
}

// ExpandImageCandidates also compares neighboring buckets (width/height ±1 quantum).
func ExpandImageCandidates(items []ImageCandidateItem) [][2]int {
	const q = 8
	type key struct{ w, h int }
	buckets := map[key][]int{}
	for i, item := range items {
		bw, bh := ImageBucketKey(item.Width, item.Height)
		k := key{bw, bh}
		buckets[k] = append(buckets[k], i)
	}
	seen := map[[2]int]struct{}{}
	var pairs [][2]int
	// Collect unique bucket keys for neighbor walk.
	keys := make([]key, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].w != keys[j].w {
			return keys[i].w < keys[j].w
		}
		return keys[i].h < keys[j].h
	})
	for _, k := range keys {
		for dw := -1; dw <= 1; dw++ {
			for dh := -1; dh <= 1; dh++ {
				nk := key{k.w + dw*q, k.h + dh*q}
				other, ok := buckets[nk]
				if !ok {
					continue
				}
				for _, i := range buckets[k] {
					for _, j := range other {
						if i >= j {
							continue
						}
						p := [2]int{i, j}
						if _, dup := seen[p]; dup {
							continue
						}
						seen[p] = struct{}{}
						pairs = append(pairs, p)
					}
				}
			}
		}
	}
	return pairs
}

// ImageCandidateItem is metadata used for image bucketing.
type ImageCandidateItem struct {
	Width  int
	Height int
	// Optional pHash for multi-index filter; empty skips hash filter.
	PHash string
}

// FilterByPHash keeps pairs whose hamming distance is within maxDistance.
func FilterByPHash(items []ImageCandidateItem, pairs [][2]int, maxDistance int) [][2]int {
	if maxDistance < 0 {
		return pairs
	}
	out := make([][2]int, 0, len(pairs))
	for _, p := range pairs {
		a, b := items[p[0]].PHash, items[p[1]].PHash
		if a == "" || b == "" {
			out = append(out, p)
			continue
		}
		d, err := hashfile.HammingDistance(a, b)
		if err != nil {
			continue
		}
		if d <= maxDistance {
			out = append(out, p)
		}
	}
	return out
}

// MaxHammingDistance converts similarity threshold to max bit distance for 64-bit hash.
func MaxHammingDistance(threshold float64, bitCount int) int {
	if threshold < 0 {
		threshold = 0
	}
	if threshold > 1 {
		threshold = 1
	}
	// similarity = 1 - d/bitCount >= threshold => d <= bitCount*(1-threshold)
	maxD := int(float64(bitCount) * (1.0 - threshold))
	return maxD
}

// VideoBucket groups video indices by duration/aspect for pairwise compare.
func VideoBucket(durationsMs []int64, widths, heights []int) [][2]int {
	n := len(durationsMs)
	var pairs [][2]int
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if durationClose(durationsMs[i], durationsMs[j]) && aspectClose(widths[i], heights[i], widths[j], heights[j]) {
				pairs = append(pairs, [2]int{i, j})
			}
		}
	}
	return pairs
}

func durationClose(a, b int64) bool {
	if a <= 0 || b <= 0 {
		return false
	}
	delta := a - b
	if delta < 0 {
		delta = -delta
	}
	maxDur := a
	if b > maxDur {
		maxDur = b
	}
	allowed := int64(3000)
	if r := maxDur * 5 / 100; r > allowed {
		allowed = r
	}
	return delta <= allowed
}

func aspectClose(lw, lh, rw, rh int) bool {
	if min4(lw, lh, rw, rh) <= 0 {
		return false
	}
	lr := float64(lw) / float64(lh)
	rr := float64(rw) / float64(rh)
	diff := lr - rr
	if diff < 0 {
		diff = -diff
	}
	return diff <= 0.05
}

func min4(a, b, c, d int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	if d < m {
		m = d
	}
	return m
}

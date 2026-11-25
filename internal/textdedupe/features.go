package textdedupe

import (
	"container/heap"
	"context"
	"sort"
)

const (
	featureNgram = 5
	maxFeatures  = 1024
	minHashBands = 8
	minHashRows  = 2
)

func features(content string) []uint64 {
	return featuresContext(context.Background(), content)
}

func featuresContext(ctx context.Context, content string) []uint64 {
	// Keep only the smallest unique hashes. This preserves the old sorted-prefix
	// result without allocating and sorting every overlapping n-gram.
	hashes := &largestHashHeap{}
	seen := make(map[uint64]struct{}, maxFeatures)
	var window [featureNgram]rune
	filled := 0
	processed := 0
	for _, r := range content {
		if processed%4096 == 0 && ctx.Err() != nil {
			return nil
		}
		processed++
		copy(window[:], window[1:])
		window[featureNgram-1] = r
		if filled < featureNgram {
			filled++
			if filled < featureNgram {
				continue
			}
		}
		value := mix64(fnv64Runes(window[:]))
		if hashes.Len() == maxFeatures && value >= (*hashes)[0] {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		if hashes.Len() < maxFeatures {
			heap.Push(hashes, value)
			seen[value] = struct{}{}
			continue
		}
		old := (*hashes)[0]
		delete(seen, old)
		(*hashes)[0] = value
		heap.Fix(hashes, 0)
		seen[value] = struct{}{}
	}
	if filled < featureNgram {
		return nil
	}
	unique := []uint64(*hashes)
	sort.Slice(unique, func(i, j int) bool { return unique[i] < unique[j] })
	return unique
}

type largestHashHeap []uint64

func (h largestHashHeap) Len() int           { return len(h) }
func (h largestHashHeap) Less(i, j int) bool { return h[i] > h[j] }
func (h largestHashHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *largestHashHeap) Push(value any)    { *h = append(*h, value.(uint64)) }
func (h *largestHashHeap) Pop() any {
	old := *h
	n := len(old)
	value := old[n-1]
	*h = old[:n-1]
	return value
}

func fnv64Runes(value []rune) uint64 {
	const offset64 = uint64(14695981039346656037)
	const prime64 = uint64(1099511628211)
	h := offset64
	for _, r := range value {
		h ^= uint64(uint32(r))
		h *= prime64
	}
	return h
}

func minHashSignature(features []uint64) [minHashBands][minHashRows]uint64 {
	var signature [minHashBands][minHashRows]uint64
	if len(features) == 0 {
		return signature
	}
	for band := range signature {
		for row := range signature[band] {
			signature[band][row] = ^uint64(0)
		}
	}
	for _, feature := range features {
		for index := 0; index < minHashBands*minHashRows; index++ {
			value := mix64(feature + uint64(index)*0x9e3779b97f4a7c15)
			band, row := index/minHashRows, index%minHashRows
			if value < signature[band][row] {
				signature[band][row] = value
			}
		}
	}
	return signature
}

func mix64(value uint64) uint64 {
	value = (value ^ (value >> 30)) * 0xbf58476d1ce4e5b9
	value = (value ^ (value >> 27)) * 0x94d049bb133111eb
	return value ^ (value >> 31)
}

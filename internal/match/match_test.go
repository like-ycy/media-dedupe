package match

import (
	"testing"

	"media-dedupe/internal/model"
	"media-dedupe/internal/video"
)

func TestConnectedComponents(t *testing.T) {
	edges := []model.SimilarityEdge{
		{LeftFileID: 1, RightFileID: 2},
		{LeftFileID: 2, RightFileID: 3},
		{LeftFileID: 10, RightFileID: 11},
	}
	comps := ConnectedComponents(edges)
	if len(comps) != 2 {
		t.Fatalf("expected 2 components, got %d: %v", len(comps), comps)
	}
	if len(comps[0]) != 3 || len(comps[1]) != 2 {
		t.Fatalf("unexpected sizes: %v", comps)
	}
}

// frameSet builds evenly spaced frame hashes, skipping the indices in skip so
// the result mirrors extraction that dropped blank or undecodable frames.
func frameSet(frameCount int, skip map[int]bool, hash func(index int) string) []video.FrameHash {
	frames := make([]video.FrameHash, 0, frameCount)
	for index := 0; index < frameCount; index++ {
		if skip[index] {
			continue
		}
		frames = append(frames, video.FrameHash{
			Index:       index,
			TimestampMs: int64(index) * 1000,
			Hash:        hash(index),
		})
	}
	return frames
}

func TestVideoHashSimilarityIdentical(t *testing.T) {
	// 16 hex chars = 64 bits
	hash := func(index int) string {
		if index%2 == 0 {
			return "0000000000000000"
		}
		return "ffffffffffffffff"
	}
	a := frameSet(8, nil, hash)
	b := frameSet(8, nil, hash)
	if sim := VideoHashSimilarity(a, b, 8); sim != 1.0 {
		t.Fatalf("expected 1.0, got %v", sim)
	}
}

// TestVideoHashSimilarityAlignsByTimestamp pins the fix for compacted frame
// lists: a video that lost its first frames must still be compared against the
// matching moments, not against whatever now sits at the same slice index.
func TestVideoHashSimilarityAlignsByTimestamp(t *testing.T) {
	hash := func(index int) string {
		if index%2 == 0 {
			return "0000000000000000"
		}
		return "ffffffffffffffff"
	}
	left := frameSet(8, nil, hash)
	// The right video lost frames 0-2; its remaining frames keep their slots and
	// timestamps, so the surviving frames still match exactly.
	right := frameSet(8, map[int]bool{0: true, 1: true, 2: true}, hash)
	sim := VideoHashSimilarity(left, right, 8)
	if sim != 1.0 {
		t.Fatalf("timestamp alignment lost: got %v", sim)
	}
	if pairs := len(right); pairs < 3 {
		t.Fatalf("test fixture too small: %d frames", pairs)
	}
}

// TestVideoHashSimilarityRejectsSingleFrame guards the silent false positive:
// one usable frame out of eight is not evidence of a duplicate.
func TestVideoHashSimilarityRejectsSingleFrame(t *testing.T) {
	hash := func(index int) string { return "0000000000000000" }
	left := frameSet(8, nil, hash)
	right := frameSet(8, map[int]bool{1: true, 2: true, 3: true, 4: true, 5: true, 6: true, 7: true}, hash)
	if len(right) != 1 {
		t.Fatalf("fixture should keep one frame, got %d", len(right))
	}
	if sim := VideoHashSimilarity(left, right, 8); sim != 0 {
		t.Fatalf("single frame must not match, got %v", sim)
	}
}

// TestVideoHashSimilarityLowCoverage checks the half-coverage floor: three
// matching frames out of eight are not enough for a verdict.
func TestVideoHashSimilarityLowCoverage(t *testing.T) {
	hash := func(index int) string { return "0000000000000000" }
	left := frameSet(8, nil, hash)
	right := frameSet(8, map[int]bool{3: true, 4: true, 5: true, 6: true, 7: true}, hash)
	if len(right) != 3 {
		t.Fatalf("fixture should keep three frames, got %d", len(right))
	}
	if sim := VideoHashSimilarity(left, right, 8); sim != 0 {
		t.Fatalf("coverage below half must not match, got %v", sim)
	}
}

// sampledFrames mirrors real extraction: timestamps come from the sampling
// schedule of the given duration, and skip drops positions that produced no
// usable picture.
func sampledFrames(durationMs int64, frameCount int, skip map[int]bool) []video.FrameHash {
	timestamps := video.BuildFrameTimestamps(durationMs, frameCount)
	frames := make([]video.FrameHash, 0, len(timestamps))
	for index, timestamp := range timestamps {
		if skip[index] {
			continue
		}
		frames = append(frames, video.FrameHash{
			Index:       index,
			TimestampMs: timestamp,
			Hash:        "0000000000000000",
		})
	}
	return frames
}

// TestVideoHashSimilarityPairsFramesOneToOne covers videos sampled at different
// intervals: one frame of the short video falls inside the tolerance window of
// several frames of the long one. Reusing that frame for every partner used to
// clear the minimum-pair floor and report a two-frame overlap as identical.
func TestVideoHashSimilarityPairsFramesOneToOne(t *testing.T) {
	left := sampledFrames(1000, 8, nil)
	right := sampledFrames(4000, 8, map[int]bool{2: true, 3: true, 4: true, 5: true, 6: true, 7: true})
	if len(right) != 2 {
		t.Fatalf("fixture should keep two frames, got %d", len(right))
	}
	if sim := VideoHashSimilarity(left, right, 8); sim != 0 {
		t.Fatalf("two-frame overlap must not match, got %v", sim)
	}
	if sim := VideoHashSimilarity(right, left, 8); sim != 0 {
		t.Fatalf("overlap must be symmetric, got %v", sim)
	}
}

// TestVideoHashSimilarityComparesSameTimestamp proves frames are paired by
// timeline: identical timestamps but different pictures stay dissimilar.
func TestVideoHashSimilarityComparesSameTimestamp(t *testing.T) {
	left := frameSet(8, nil, func(index int) string {
		if index%2 == 0 {
			return "0000000000000000"
		}
		return "ffffffffffffffff"
	})
	right := frameSet(8, nil, func(index int) string {
		if index%2 == 0 {
			return "ffffffffffffffff"
		}
		return "0000000000000000"
	})
	if sim := VideoHashSimilarity(left, right, 8); sim != 0 {
		t.Fatalf("inverted frames must not match, got %v", sim)
	}
}

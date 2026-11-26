package match

import (
	"sort"

	"media-dedupe/internal/hashfile"
	"media-dedupe/internal/model"
	"media-dedupe/internal/video"
)

// UnionFind groups file IDs connected by similarity edges.
type UnionFind struct {
	parent map[int64]int64
}

func NewUnionFind() *UnionFind {
	return &UnionFind{parent: make(map[int64]int64)}
}

func (uf *UnionFind) Find(x int64) int64 {
	if _, ok := uf.parent[x]; !ok {
		uf.parent[x] = x
		return x
	}
	if uf.parent[x] != x {
		uf.parent[x] = uf.Find(uf.parent[x])
	}
	return uf.parent[x]
}

func (uf *UnionFind) Union(a, b int64) {
	ra, rb := uf.Find(a), uf.Find(b)
	if ra != rb {
		uf.parent[rb] = ra
	}
}

// ConnectedComponents returns groups of file IDs.
func ConnectedComponents(edges []model.SimilarityEdge) [][]int64 {
	uf := NewUnionFind()
	for _, e := range edges {
		uf.Union(e.LeftFileID, e.RightFileID)
	}
	buckets := map[int64][]int64{}
	for id := range uf.parent {
		root := uf.Find(id)
		buckets[root] = append(buckets[root], id)
	}
	groups := make([][]int64, 0, len(buckets))
	for _, ids := range buckets {
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		groups = append(groups, ids)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i][0] < groups[j][0] })
	return groups
}

// IsVideoMetadataCandidate applies duration and aspect-ratio prefilter.
func IsVideoMetadataCandidate(left, right model.VideoMetadata) bool {
	if !left.IsReadable || !right.IsReadable {
		return false
	}
	if left.DurationMs <= 0 || right.DurationMs <= 0 {
		return false
	}
	delta := abs64(left.DurationMs - right.DurationMs)
	maxDur := left.DurationMs
	if right.DurationMs > maxDur {
		maxDur = right.DurationMs
	}
	allowed := int64(3000)
	if ratioAllowed := maxDur * 5 / 100; ratioAllowed > allowed {
		allowed = ratioAllowed
	}
	if delta > allowed {
		return false
	}
	return aspectRatioClose(left.Width, left.Height, right.Width, right.Height, 0.05)
}

func aspectRatioClose(lw, lh, rw, rh int, tolerance float64) bool {
	if minInt(lw, lh, rw, rh) <= 0 {
		return false
	}
	lr := float64(lw) / float64(lh)
	rr := float64(rw) / float64(rh)
	diff := lr - rr
	if diff < 0 {
		diff = -diff
	}
	return diff <= tolerance
}

// VideoHashSimilarity compares sampled frame hashes by timestamp.
//
// Extracted frames are sparse: blank or undecodable positions are dropped
// before they ever reach this function. Aligning by slice index would compare
// different moments whenever the two videos skipped a different number of
// frames, so each frame is paired with the nearest sample of the other video
// within half a sampling interval.
//
// Pairing is one-to-one: when sampling intervals differ, a single frame of the
// short video would otherwise be claimed by every frame of the long one and
// inflate the coverage past the minimum-pair requirement. Matching runs
// nearest-in-time first, so the pairing stays the temporally correct one.
//
// The result is the median of the paired similarities, and it is 0 when too few
// frames could be paired: a single surviving frame must never be enough to call
// two videos duplicates.
func VideoHashSimilarity(left, right []video.FrameHash, frameCount int) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	tolerance := frameTolerance(left, right)
	if tolerance <= 0 {
		return 0
	}
	sims := make([]float64, 0, minInt(len(left), len(right)))
	for _, pair := range alignFrames(left, right, tolerance) {
		sims = append(sims, pair.similarity)
	}
	if len(sims) < requiredPairs(frameCount, len(left)) {
		return 0
	}
	return median(sims)
}

// framePair is one accepted timestamp alignment between two videos.
type framePair struct {
	left       int
	right      int
	delta      int64
	similarity float64
}

// alignFrames resolves the feasible pairs into a one-to-one matching, closest
// timestamps first, and returns them in that order. Both inputs are small (one
// entry per sampled frame), so the exhaustive pair list stays cheap.
func alignFrames(left, right []video.FrameHash, tolerance int64) []framePair {
	pairs := make([]framePair, 0, len(left)*len(right))
	for i, l := range left {
		for j, r := range right {
			delta := abs64(l.TimestampMs - r.TimestampMs)
			if delta > tolerance {
				continue
			}
			similarity, err := hashfile.Similarity(l.Hash, r.Hash)
			if err != nil {
				continue
			}
			pairs = append(pairs, framePair{left: i, right: j, delta: delta, similarity: similarity})
		}
	}
	sort.SliceStable(pairs, func(a, b int) bool {
		if pairs[a].delta != pairs[b].delta {
			return pairs[a].delta < pairs[b].delta
		}
		return pairs[a].similarity > pairs[b].similarity
	})
	leftUsed := make([]bool, len(left))
	rightUsed := make([]bool, len(right))
	matched := pairs[:0]
	for _, pair := range pairs {
		if leftUsed[pair.left] || rightUsed[pair.right] {
			continue
		}
		leftUsed[pair.left] = true
		rightUsed[pair.right] = true
		matched = append(matched, pair)
	}
	return matched
}

// requiredPairs is the minimum number of aligned frames needed to judge two
// videos: at least half of the sampled positions, and never fewer than three.
func requiredPairs(frameCount, available int) int {
	if frameCount <= 0 {
		frameCount = available
	}
	required := (frameCount + 1) / 2
	if required < 3 {
		required = 3
	}
	return required
}

// frameTolerance is half the sampling interval, so neighbouring samples cannot
// both claim the same frame.
func frameTolerance(left, right []video.FrameHash) int64 {
	step := frameStepMs(left)
	if other := frameStepMs(right); other > step {
		step = other
	}
	return step / 2
}

// frameStepMs estimates the sampling interval from index/timestamp distances,
// which stays correct when most samples in between were skipped.
func frameStepMs(frames []video.FrameHash) int64 {
	var step int64
	for i := 1; i < len(frames); i++ {
		indexDelta := frames[i].Index - frames[i-1].Index
		timeDelta := frames[i].TimestampMs - frames[i-1].TimestampMs
		if indexDelta <= 0 || timeDelta <= 0 {
			continue
		}
		candidate := timeDelta / int64(indexDelta)
		if candidate > 0 && (step == 0 || candidate < step) {
			step = candidate
		}
	}
	return step
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sort.Float64s(values)
	mid := len(values) / 2
	if len(values)%2 == 0 {
		return (values[mid-1] + values[mid]) / 2
	}
	return values[mid]
}

// EdgeKey normalizes pair key.
func EdgeKey(a, b int64) [2]int64 {
	if a > b {
		return [2]int64{b, a}
	}
	return [2]int64{a, b}
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func minInt(vals ...int) int {
	m := vals[0]
	for _, v := range vals[1:] {
		if v < m {
			m = v
		}
	}
	return m
}

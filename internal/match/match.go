package match

import (
	"sort"

	"media-dedupe/internal/hashfile"
	"media-dedupe/internal/model"
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

// VideoHashSimilarity compares frame hash lists with a small temporal offset.
func VideoHashSimilarity(left, right []string) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	best := 0.0
	for shift := -1; shift <= 1; shift++ {
		sims := make([]float64, 0, minInt(len(left), len(right)))
		for i, leftHash := range left {
			j := i + shift
			if j < 0 || j >= len(right) {
				continue
			}
			similarity, err := hashfile.Similarity(leftHash, right[j])
			if err == nil {
				sims = append(sims, similarity)
			}
		}
		if similarity := median(sims); similarity > best {
			best = similarity
		}
	}
	return best
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

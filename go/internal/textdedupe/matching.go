package textdedupe

import (
	"context"
	"fmt"
	"math"
	"sort"

	"media-dedupe/internal/match"
	"media-dedupe/internal/model"
	"media-dedupe/internal/novelname"
)

// Cap on per-file relation lines, so a 500-edition group does not write 500
// reasons into every member's row.
const maxEvidencePerMember = 8

type relation struct {
	kind                        model.GroupType
	score                       float64
	leftCoverage, rightCoverage float64
	partial                     bool
}

func Groups(facts []Fact, threshold float64) []model.ReportGroup {
	return GroupsContext(context.Background(), facts, threshold)
}

func GroupsContext(ctx context.Context, facts []Fact, threshold float64) []model.ReportGroup {
	return GroupsProgress(ctx, facts, threshold, nil)
}

func GroupsProgress(ctx context.Context, facts []Fact, threshold float64, progress ProgressFunc) []model.ReportGroup {
	if ctx.Err() != nil {
		return nil
	}
	if threshold <= 0 || threshold > 1 {
		threshold = .92
	}
	// Collapse normalized equality before candidate generation; retain all names
	// and restore the equal members with their own evidence afterward.
	var reps []Fact
	var members [][]Fact
	byDigest := map[string]int{}
	for _, f := range facts {
		if idx, ok := byDigest[f.Digest]; ok && f.Digest != "" {
			reps[idx].Names = append(reps[idx].Names, f.Names...)
			members[idx] = append(members[idx], f)
			continue
		}
		if f.Digest != "" {
			byDigest[f.Digest] = len(reps)
		}
		reps = append(reps, f)
		members = append(members, []Fact{f})
	}
	links := map[[2]int]relation{}
	uf := match.NewUnionFind()
	pairs := candidatePairs(ctx, reps)
	for index, p := range pairs {
		if ctx.Err() != nil {
			return nil
		}
		if progress != nil && index%1024 == 0 {
			progress(index, len(pairs), "")
		}
		r, ok := compare(reps[p[0]], reps[p[1]], threshold)
		if !ok {
			continue
		}
		links[p] = r
		uf.Union(int64(p[0]), int64(p[1]))
	}
	if progress != nil && ctx.Err() == nil {
		progress(len(pairs), len(pairs), "")
	}
	if ctx.Err() != nil {
		return nil
	}
	components := map[int64][]int{}
	for i := range reps {
		root := uf.Find(int64(i))
		components[root] = append(components[root], i)
	}
	var groups []model.ReportGroup
	for _, component := range components {
		if ctx.Err() != nil {
			return nil
		}
		count := 0
		for _, i := range component {
			count += len(members[i])
		}
		if count < 2 {
			continue
		}
		winner := component[0]
		for _, i := range component {
			if reps[i].Length > reps[winner].Length || (reps[i].Length == reps[winner].Length && reps[i].Path < reps[winner].Path) {
				winner = i
			}
		}
		g := model.ReportGroup{GroupType: model.GroupTextEqual, Confidence: 1, RecommendedFileID: reps[winner].FileID}
		if len(component) > 1 {
			g.GroupType = model.GroupTextName
			g.Confidence = 1
			for x, i := range component {
				for _, j := range component[x+1:] {
					r, ok := links[pairKey(i, j)]
					if !ok || r.kind == model.GroupTextName {
						g.Confidence = 0
					} else {
						g.Confidence = math.Min(g.Confidence, r.score)
					}
					if r.kind == model.GroupTextContained {
						g.GroupType = model.GroupTextContained
					} else if r.kind == model.GroupSimilarText && g.GroupType == model.GroupTextName {
						g.GroupType = model.GroupSimilarText
					}
				}
			}
		}
		for _, i := range component {
			var evidence []string
			// Reasons are stored per file in the results database. One line per
			// relation is enough to justify a decision; listing every relation of a
			// large group would write megabytes of near-identical JSON.
			omitted := 0
			for _, j := range component {
				if i == j {
					continue
				}
				r, ok := links[pairKey(i, j)]
				if !ok {
					continue
				}
				if len(evidence) >= maxEvidencePerMember {
					omitted++
					continue
				}
				description := "仅书名关联，正文未证实重复"
				switch r.kind {
				case model.GroupSimilarText:
					description = fmt.Sprintf("正文近似（指纹 Dice %.1f%%），需核对差异", r.score*100)
				case model.GroupTextContained:
					lc, rc := r.leftCoverage, r.rightCoverage
					if i > j {
						lc, rc = rc, lc
					}
					description = fmt.Sprintf("正文可能包含/被包含（本文件覆盖率 %.1f%%，对方 %.1f%%），需核对完整性", lc*100, rc*100)
					if r.partial {
						description = fmt.Sprintf("正文部分重叠（本文件覆盖率 %.1f%%，对方 %.1f%%），两份可能各有独有内容", lc*100, rc*100)
					}
				}
				evidence = append(evidence, fmt.Sprintf("与「%s」：%s", reps[j].Path, description))
				if len(reps[i].Names) > 0 && len(reps[j].Names) > 0 {
					for _, difference := range novelname.Differences(reps[i].Names[0], reps[j].Names[0]) {
						if len(evidence) >= maxEvidencePerMember {
							omitted++
							break
						}
						evidence = append(evidence, fmt.Sprintf("与「%s」：%s", reps[j].Path, difference))
					}
				}
			}
			if omitted > 0 {
				evidence = append(evidence, fmt.Sprintf("组内另有 %d 条与其它成员的关系未逐条列出，需要时请打开这些文件核对", omitted))
			}
			for k, f := range members[i] {
				action := model.ActionReview
				sim := NoDirectSimilarity
				reasons := []string{}
				if len(f.Names) > 0 {
					reasons = append(reasons, f.Names[0].Reasons()...)
				}
				if f.DecodeError != "" {
					reasons = append(reasons, "正文未参与比较，保留书名线索："+f.DecodeError)
				}
				reasons = append(reasons, evidence...)
				if len(component) == 1 {
					action = model.ActionKeep
					sim = 1
				}
				if k > 0 {
					action = model.ActionCleanup
					reasons = append(reasons, "与「"+reps[i].Path+"」：解码并统一换行后的全文 SHA-256 一致")
				}
				if len(component) == 1 {
					reasons = append(reasons, "仅编码、BOM 或换行存储形式不同，文本内容一致")
				} else if r, ok := links[pairKey(i, winner)]; ok && r.kind != model.GroupTextName {
					sim = r.score
				}
				if i == winner && g.GroupType != model.GroupTextName {
					sim = 1
				}
				if len(component) > 1 && k == 0 {
					reasons = append(reasons, "关联组逐对核对；对照文件不保证覆盖组内所有版本")
				}
				g.Items = append(g.Items, model.ReportItem{FileID: f.FileID, Path: f.Path, SizeBytes: f.Size, QualityScore: f.Quality, Action: action, Similarity: sim, Reasons: reasons})
			}
		}
		sort.Slice(g.Items, func(i, j int) bool { return g.Items[i].FileID < g.Items[j].FileID })
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Items[0].FileID < groups[j].Items[0].FileID })
	return groups
}

func pairKey(a, b int) [2]int {
	if a > b {
		a, b = b, a
	}
	return [2]int{a, b}
}

func compare(a, b Fact, threshold float64) (relation, bool) {
	nameScore := 0.0
	for _, an := range a.Names {
		for _, bn := range b.Names {
			nameScore = math.Max(nameScore, novelname.Match(an, bn))
		}
	}
	left, right, common := sharedSample(a, b, threshold, nameScore)
	if common >= 0 {
		lc, rc := float64(common)/float64(left), float64(common)/float64(right)
		dice := 2 * float64(common) / float64(left+right)
		if dice >= threshold {
			return relation{kind: model.GroupSimilarText, score: dice, leftCoverage: lc, rightCoverage: rc}, true
		}
		if common >= 32 && ((a.Length < b.Length && lc >= math.Max(.97, threshold)) || (b.Length < a.Length && rc >= math.Max(.97, threshold))) {
			return relation{kind: model.GroupTextContained, score: dice, leftCoverage: lc, rightCoverage: rc}, true
		}
		if nameScore == 1 && common >= 32 && math.Min(lc, rc) >= .10 {
			return relation{kind: model.GroupTextContained, score: dice, leftCoverage: lc, rightCoverage: rc, partial: true}, true
		}
	}
	if nameScore >= .9 {
		return relation{kind: model.GroupTextName, score: NoDirectSimilarity}, true
	}
	return relation{}, false
}

// Compare bottom-k sketches under a shared cutoff. A negative intersection
// means no content relation can qualify, even if all remaining hashes match.
func sharedSample(left, right Fact, threshold, nameScore float64) (int, int, int) {
	a, b := left.Features, right.Features
	if left.Length < 200 || right.Length < 200 || min(len(a), len(b)) < 64 {
		return 0, 0, -1
	}
	cutoff := uint64(math.MaxUint64)
	if len(a) == maxFeatures {
		cutoff = min(cutoff, a[len(a)-1])
	}
	if len(b) == maxFeatures {
		cutoff = min(cutoff, b[len(b)-1])
	}
	na := sort.Search(len(a), func(i int) bool { return a[i] > cutoff })
	nb := sort.Search(len(b), func(i int) bool { return b[i] > cutoff })
	if na < 32 || nb < 32 {
		return na, nb, -1
	}
	// Round down intentionally: the bound may admit one extra hash, but cannot
	// reject a match at a floating-point threshold boundary.
	needed := int(threshold * float64(na+nb) / 2)
	if left.Length < right.Length {
		needed = min(needed, max(32, int(math.Max(.97, threshold)*float64(na))))
	}
	if right.Length < left.Length {
		needed = min(needed, max(32, int(math.Max(.97, threshold)*float64(nb))))
	}
	if nameScore == 1 {
		needed = min(needed, max(32, int(.10*float64(max(na, nb)))))
	}
	i, j, common := 0, 0, 0
	for i < na && j < nb {
		if common+min(na-i, nb-j) < needed {
			return na, nb, -1
		}
		if a[i] < b[j] {
			i++
		} else if a[i] > b[j] {
			j++
		} else {
			common++
			i++
			j++
		}
	}
	return na, nb, common
}

func candidatePairs(ctx context.Context, facts []Fact) [][2]int {
	seen := map[[2]int]struct{}{}
	addBucket := func(bucket []int) {
		for i, a := range bucket {
			if ctx.Err() != nil {
				return
			}
			for _, b := range bucket[i+1:] {
				if a != b {
					seen[pairKey(a, b)] = struct{}{}
				}
			}
		}
	}
	titles := map[string][]int{}
	grams := map[string][]int{}
	content := map[uint64][]int{}
	bands := map[[3]uint64][]int{}
	for i, f := range facts {
		if ctx.Err() != nil {
			return nil
		}
		titleSeen, gramSeen := map[string]bool{}, map[string]bool{}
		for _, name := range f.Names {
			for _, key := range name.Keys {
				if !titleSeen[key] {
					titles[key] = append(titles[key], i)
					titleSeen[key] = true
				}
			}
			runes := []rune(name.Title)
			if len(runes) >= 6 {
				for j := 0; j+2 < len(runes); j++ {
					gram := string(runes[j : j+3])
					if !gramSeen[gram] {
						grams[gram] = append(grams[gram], i)
						gramSeen[gram] = true
					}
				}
			}
		}
		// Rare shared shingles improve recall of partial novels even when their
		// title differs. Common boilerplate shingles do not generate all pairs.
		for _, v := range f.Features[:min(128, len(f.Features))] {
			content[v] = append(content[v], i)
		}
		if len(f.Features) > 0 {
			for band, sig := range minHashSignature(f.Features) {
				key := [3]uint64{uint64(band), sig[0], sig[1]}
				bands[key] = append(bands[key], i)
			}
		}
	}
	for _, bucket := range titles {
		addBucket(bucket)
	}
	for _, bucket := range grams {
		if len(bucket) <= 256 {
			addBucket(bucket)
		}
	}
	for _, bucket := range content {
		if len(bucket) <= 256 {
			addBucket(bucket)
		}
	}
	for _, bucket := range bands {
		addBucket(bucket)
	}
	pairs := make([][2]int, 0, len(seen))
	for p := range seen {
		pairs = append(pairs, p)
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i][0] != pairs[j][0] {
			return pairs[i][0] < pairs[j][0]
		}
		return pairs[i][1] < pairs[j][1]
	})
	return pairs
}

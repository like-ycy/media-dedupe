package score

import (
	"math"
	"sort"
	"strings"

	"media-dedupe/internal/config"
	"media-dedupe/internal/model"
)

// ImageQuality scores image 0..1 from metadata + size.
func ImageQuality(meta model.ImageMetadata, sizeBytes int64) float64 {
	if !meta.IsReadable {
		return 0
	}
	megapixels := float64(meta.Width*meta.Height) / 1_000_000
	resolution := math.Min(0.70, math.Log1p(megapixels)/math.Log1p(12)*0.70)
	formatScore := imageFormatScore(meta.FormatName)
	sizeScore := math.Min(0.10, math.Log1p(float64(max64(sizeBytes, 0)))/math.Log1p(10_000_000)*0.10)
	total := resolution + formatScore + sizeScore + 0.10
	return math.Min(1.0, total)
}

// VideoQuality scores video 0..1 from metadata.
func VideoQuality(meta model.VideoMetadata) float64 {
	if !meta.IsReadable {
		return 0
	}
	pixels := float64(meta.Width * meta.Height)
	resolution := math.Min(0.55, math.Log1p(pixels)/math.Log1p(3840*2160)*0.55)
	bitRateScore := 0.0
	if meta.BitRate != nil && *meta.BitRate > 0 {
		bitRateScore = math.Min(0.20, math.Log1p(float64(*meta.BitRate))/math.Log1p(20_000_000)*0.20)
	}
	codecScore := 0.05
	codec := strings.ToLower(meta.Codec)
	if codec == "h264" || codec == "hevc" || codec == "h265" {
		codecScore = 0.10
	}
	audioScore := 0.0
	if meta.HasAudio {
		audioScore = 0.05
	}
	durationScore := 0.0
	if meta.DurationMs > 0 {
		durationScore = 0.10
	}
	total := resolution + bitRateScore + codecScore + audioScore + durationScore
	return math.Min(1.0, total)
}

// ChooseActions assigns keep/cleanup/review by quality scores keyed by file ID.
func ChooseActions(quality map[int64]float64, reviewDelta float64) map[int64]model.Action {
	if reviewDelta == 0 {
		reviewDelta = config.DefaultReviewDelta
	}
	if len(quality) == 0 {
		return map[int64]model.Action{}
	}
	type pair struct {
		id    int64
		score float64
	}
	items := make([]pair, 0, len(quality))
	for id, s := range quality {
		items = append(items, pair{id, s})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].score != items[j].score {
			return items[i].score > items[j].score
		}
		return items[i].id < items[j].id
	})
	actions := make(map[int64]model.Action, len(quality))
	if len(items) > 1 && math.Abs(items[0].score-items[1].score) <= reviewDelta {
		for id := range quality {
			actions[id] = model.ActionReview
		}
		return actions
	}
	winner := items[0].id
	for id := range quality {
		if id == winner {
			actions[id] = model.ActionKeep
		} else {
			actions[id] = model.ActionCleanup
		}
	}
	return actions
}

// Winner returns the recommended file ID from quality map.
func Winner(quality map[int64]float64) int64 {
	var bestID int64
	bestScore := -1.0
	first := true
	for id, s := range quality {
		if first || s > bestScore || (s == bestScore && id < bestID) {
			bestID = id
			bestScore = s
			first = false
		}
	}
	return bestID
}

func imageFormatScore(formatName string) float64 {
	switch strings.ToUpper(formatName) {
	case "TIFF", "PNG", "HEIC", "HEIF":
		return 0.10
	case "JPEG", "JPG", "WEBP":
		return 0.07
	default:
		return 0.03
	}
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

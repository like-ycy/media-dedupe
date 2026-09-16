package textdedupe

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	xunicode "golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"media-dedupe/internal/match"
	"media-dedupe/internal/model"
	"media-dedupe/internal/score"
)

const (
	featureWindow = 7
	maxFeatures   = 8192
	maxBucketSize = 256
	minHashBands  = 8
	minHashRows   = 2
)

type Fact struct {
	FileID   int64
	Path     string
	Size     int64
	Quality  float64
	Name     string
	Length   int
	Features []uint64
}

type FactCache interface {
	LoadTextFact(fileID, sizeBytes int64) (name string, length int, features []uint64, ok bool, err error)
	SaveTextFact(fileID, sizeBytes int64, length int, name string, features []uint64) error
}

func Facts(files []model.DiscoveredFile, ids []int64, workers int) ([]Fact, []model.ReportError) {
	return FactsCached(files, ids, nil, workers, nil)
}

func FactsCached(files []model.DiscoveredFile, ids []int64, unchanged []bool, workers int, cache FactCache) ([]Fact, []model.ReportError) {
	if workers < 1 {
		workers = 1
	}
	out := make([]Fact, len(files))
	errs := make([]model.ReportError, 0)
	var mu sync.Mutex
	var wg sync.WaitGroup
	type job struct {
		index int
		file  model.DiscoveredFile
	}
	jobs := make(chan job)
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				if cache != nil && job.index < len(unchanged) && unchanged[job.index] {
					name, length, cachedFeatures, ok, err := cache.LoadTextFact(ids[job.index], job.file.SizeBytes)
					if err != nil {
						mu.Lock()
						errs = append(errs, model.ReportError{Path: job.file.Path, Stage: "text_cache", Message: err.Error()})
						mu.Unlock()
					} else if ok {
						out[job.index] = Fact{
							FileID: ids[job.index], Path: job.file.Path, Size: job.file.SizeBytes,
							Quality: score.TextQuality(job.file.SizeBytes), Name: name,
							Length: length, Features: cachedFeatures,
						}
						continue
					}
				}
				content, err := readText(job.file.Path)
				if err != nil {
					mu.Lock()
					errs = append(errs, model.ReportError{Path: job.file.Path, Stage: "text", Message: err.Error()})
					mu.Unlock()
					continue
				}
				out[job.index] = Fact{
					FileID:   ids[job.index],
					Path:     job.file.Path,
					Size:     job.file.SizeBytes,
					Quality:  score.TextQuality(job.file.SizeBytes),
					Name:     normalizeName(filepath.Base(job.file.Path)),
					Length:   len(content),
					Features: features(content),
				}
				if cache != nil {
					fact := out[job.index]
					if err := cache.SaveTextFact(fact.FileID, fact.Size, fact.Length, fact.Name, fact.Features); err != nil {
						mu.Lock()
						errs = append(errs, model.ReportError{Path: job.file.Path, Stage: "text_cache", Message: err.Error()})
						mu.Unlock()
					}
				}
			}
		}()
	}
	for i, file := range files {
		if file.MediaType == model.MediaText && i < len(ids) && ids[i] != 0 {
			jobs <- job{index: i, file: file}
		}
	}
	close(jobs)
	wg.Wait()
	compact := out[:0]
	for _, fact := range out {
		if fact.FileID != 0 {
			compact = append(compact, fact)
		}
	}
	return compact, errs
}

func Groups(facts []Fact, threshold float64) []model.ReportGroup {
	if len(facts) < 2 {
		return nil
	}
	if threshold <= 0 || threshold > 1 {
		threshold = 0.92
	}
	pairs := candidatePairs(facts)
	edges := make([]model.SimilarityEdge, 0, len(pairs))
	similarities := make(map[[2]int64]float64, len(pairs))
	for _, pair := range pairs {
		a, b := facts[pair[0]], facts[pair[1]]
		if !lengthCandidate(a.Length, b.Length) {
			continue
		}
		similarity := similarity(a, b)
		if similarity < threshold && (nameSimilarity(a.Name, b.Name) < 0.85 || similarity < threshold-0.04) {
			continue
		}
		key := match.EdgeKey(a.FileID, b.FileID)
		similarities[key] = similarity
		edges = append(edges, model.SimilarityEdge{
			LeftFileID:  a.FileID,
			RightFileID: b.FileID,
			GroupType:   model.GroupSimilarText,
			Similarity:  similarity,
			Reasons:     []string{"正文规范化指纹相似"},
		})
	}
	components := match.ConnectedComponents(edges)
	byID := make(map[int64]Fact, len(facts))
	for _, fact := range facts {
		byID[fact.FileID] = fact
	}
	groups := make([]model.ReportGroup, 0, len(components))
	for _, component := range components {
		if len(component) < 2 {
			continue
		}
		winner := component[0]
		confidence := 1.0
		for _, id := range component {
			if byID[id].Size > byID[winner].Size {
				winner = id
			}
		}
		for i := 0; i < len(component); i++ {
			for j := i + 1; j < len(component); j++ {
				if value, ok := similarities[match.EdgeKey(component[i], component[j])]; ok && value < confidence {
					confidence = value
				}
			}
		}
		items := make([]model.ReportItem, 0, len(component))
		for _, id := range component {
			similarity := bestSimilarity(id, component, similarities)
			if similarity < confidence {
				confidence = similarity
			}
			action := model.ActionReview
			if id == winner {
				action = model.ActionKeep
			}
			items = append(items, model.ReportItem{
				FileID:       id,
				Path:         byID[id].Path,
				Action:       action,
				Similarity:   similarity,
				SizeBytes:    byID[id].Size,
				QualityScore: byID[id].Quality,
				Reasons:      []string{"正文规范化指纹相似", "文件大小较大者优先保留"},
			})
		}
		groups = append(groups, model.ReportGroup{
			GroupType:         model.GroupSimilarText,
			Confidence:        confidence,
			RecommendedFileID: winner,
			Items:             items,
		})
	}
	return groups
}

func readText(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 1<<20)
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	if len(data) >= 2 && data[0] == 0xff && data[1] == 0xfe {
		decoded, err := io.ReadAll(transform.NewReader(strings.NewReader(string(data[2:])), xunicode.UTF16(xunicode.LittleEndian, xunicode.IgnoreBOM).NewDecoder()))
		if err != nil {
			return "", err
		}
		data = decoded
	} else if len(data) >= 2 && data[0] == 0xfe && data[1] == 0xff {
		decoded, err := io.ReadAll(transform.NewReader(strings.NewReader(string(data[2:])), xunicode.UTF16(xunicode.BigEndian, xunicode.IgnoreBOM).NewDecoder()))
		if err != nil {
			return "", err
		}
		data = decoded
	} else {
		data = bytesTrimBOM(data)
		if !utf8.Valid(data) {
			decoded, err := io.ReadAll(transform.NewReader(strings.NewReader(string(data)), simplifiedchinese.GB18030.NewDecoder()))
			if err != nil {
				return "", fmt.Errorf("decode text: %w", err)
			}
			data = decoded
		}
	}
	return normalize(string(data)), nil
}

func bytesTrimBOM(data []byte) []byte {
	if len(data) >= 3 && data[0] == 0xef && data[1] == 0xbb && data[2] == 0xbf {
		return data[3:]
	}
	return data
}

func normalize(value string) string {
	value = norm.NFKC.String(value)
	var b strings.Builder
	b.Grow(len(value))
	space := false
	for _, r := range value {
		if r == '\ufeff' || r == '\u200b' {
			continue
		}
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

func features(content string) []uint64 {
	if len(content) < featureWindow {
		return nil
	}
	stride := (len(content)-featureWindow)/maxFeatures + 1
	if stride < 3 {
		stride = 3
	}
	values := make([]uint64, 0, maxFeatures)
	perOffset := maxFeatures / 8
	for offset := 0; offset < 8 && len(values) < maxFeatures; offset++ {
		start := offset * stride / 8
		for i, count := start, 0; i+featureWindow <= len(content) && count < perOffset; i, count = i+stride, count+1 {
			values = append(values, fnv64(content[i:i+featureWindow]))
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	unique := make([]uint64, 0, len(values))
	for _, value := range values {
		if len(unique) == 0 || unique[len(unique)-1] != value {
			unique = append(unique, value)
		}
	}
	return unique
}

func fnv64(value string) uint64 {
	const offset64 = uint64(14695981039346656037)
	const prime64 = uint64(1099511628211)
	h := offset64
	for i := 0; i < len(value); i++ {
		h ^= uint64(value[i])
		h *= prime64
	}
	return h
}

func candidatePairs(facts []Fact) [][2]int {
	seen := make(map[[2]int]struct{})
	byBand := make(map[[3]uint64][]int)
	byNameToken := make(map[string][]int)
	add := func(a, b int) {
		if a == b {
			return
		}
		if a > b {
			a, b = b, a
		}
		seen[[2]int{a, b}] = struct{}{}
	}
	addToBucket := func(bucket []int, index int) []int {
		if len(bucket) < maxBucketSize {
			return append(bucket, index)
		}
		add(bucket[len(bucket)-1], index)
		bucket[len(bucket)-1] = index
		return bucket
	}
	for i, fact := range facts {
		if len(fact.Features) > 0 {
			for band, signature := range minHashSignature(fact.Features) {
				key := [3]uint64{uint64(band), signature[0], signature[1]}
				bucket := byBand[key]
				byBand[key] = addToBucket(bucket, i)
			}
		}
		for _, token := range strings.Fields(fact.Name) {
			bucket := byNameToken[token]
			byNameToken[token] = addToBucket(bucket, i)
		}
	}
	for _, bucket := range byBand {
		for i := 0; i < len(bucket); i++ {
			for j := i + 1; j < len(bucket); j++ {
				add(bucket[i], bucket[j])
			}
		}
	}
	for _, bucket := range byNameToken {
		for i := 0; i < len(bucket); i++ {
			for j := i + 1; j < len(bucket); j++ {
				add(bucket[i], bucket[j])
			}
		}
	}
	pairs := make([][2]int, 0, len(seen))
	for pair := range seen {
		pairs = append(pairs, pair)
	}
	return pairs
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

func lengthCandidate(a, b int) bool {
	if a == 0 || b == 0 {
		return false
	}
	if a > b {
		a, b = b, a
	}
	return float64(a)/float64(b) >= 0.70
}

func similarity(a, b Fact) float64 {
	if len(a.Features) == 0 || len(b.Features) == 0 {
		return 0
	}
	i, j, common := 0, 0, 0
	for i < len(a.Features) && j < len(b.Features) {
		switch {
		case a.Features[i] < b.Features[j]:
			i++
		case a.Features[i] > b.Features[j]:
			j++
		default:
			common++
			i++
			j++
		}
	}
	return 2 * float64(common) / float64(len(a.Features)+len(b.Features))
}

func normalizeName(name string) string {
	name = strings.TrimSuffix(name, filepath.Ext(name))
	name = norm.NFKC.String(strings.ToLower(name))
	var b strings.Builder
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			continue
		}
		b.WriteByte(' ')
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func nameSimilarity(a, b string) float64 {
	left := strings.Fields(a)
	right := strings.Fields(b)
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	set := make(map[string]struct{}, len(left))
	for _, token := range left {
		set[token] = struct{}{}
	}
	common := 0
	seen := make(map[string]struct{}, len(right))
	for _, token := range right {
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		if _, ok := set[token]; ok {
			common++
		}
	}
	return float64(common) / float64(len(set)+len(seen)-common)
}

func bestSimilarity(id int64, component []int64, similarities map[[2]int64]float64) float64 {
	best := 0.0
	for _, other := range component {
		if other == id {
			continue
		}
		if value, ok := similarities[match.EdgeKey(id, other)]; ok && value > best {
			best = value
		}
	}
	return best
}

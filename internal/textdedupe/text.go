package textdedupe

import (
	"context"
	"media-dedupe/internal/model"
	"media-dedupe/internal/novelname"
	"media-dedupe/internal/score"
	"path/filepath"
	"sync"
	"unicode/utf8"
)

type Fact struct {
	FileID  int64
	Path    string
	Size    int64
	Quality float64
	Names   []novelname.Name
	model.TextFingerprint
}

type FactCache interface {
	LoadTextFact(fileID, sizeBytes int64) (model.TextFingerprint, bool, error)
	SaveTextFact(fileID, sizeBytes int64, fact model.TextFingerprint) error
}

type FileError struct {
	Path  string
	Stage string
	Err   error
}

func Facts(files []model.DiscoveredFile, ids []int64, workers int) ([]Fact, []FileError) {
	return FactsCached(files, ids, nil, workers, nil)
}
func FactsCached(files []model.DiscoveredFile, ids []int64, unchanged []bool, workers int, cache FactCache) ([]Fact, []FileError) {
	return FactsCachedContext(context.Background(), files, ids, unchanged, workers, cache)
}
func FactsCachedContext(ctx context.Context, files []model.DiscoveredFile, ids []int64, unchanged []bool, workers int, cache FactCache) ([]Fact, []FileError) {
	workers = max(workers, 1)
	out := make([]Fact, len(files))
	var errs []FileError
	var mu sync.Mutex
	record := func(i int, stage string, err error) {
		mu.Lock()
		defer mu.Unlock()
		errs = append(errs, FileError{files[i].Path, stage, err})
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					continue
				}
				f := files[i]
				fact := Fact{FileID: ids[i], Path: f.Path, Size: f.SizeBytes, Quality: score.TextQuality(f.SizeBytes), Names: []novelname.Name{novelname.Parse(filepath.Base(f.Path))}}
				if cache != nil && i < len(unchanged) && unchanged[i] {
					cached, ok, err := cache.LoadTextFact(ids[i], f.SizeBytes)
					if err != nil {
						record(i, "text_cache", err)
						continue
					}
					if ok {
						fact.TextFingerprint = cached
						out[i] = fact
						continue
					}
				}
				content, err := readText(f.Path)
				if err != nil {
					record(i, "text", err)
					continue
				}
				if ctx.Err() != nil {
					continue
				}
				fact.TextFingerprint = model.TextFingerprint{Digest: textDigest(content), Length: utf8.RuneCountInString(content), Features: featuresContext(ctx, content)}
				if ctx.Err() != nil {
					continue
				}
				if cache != nil {
					if err := cache.SaveTextFact(fact.FileID, f.SizeBytes, fact.TextFingerprint); err != nil {
						record(i, "text_cache", err)
						continue
					}
				}
				out[i] = fact
			}
		}()
	}
send:
	for i, f := range files {
		if f.MediaType != model.MediaText || i >= len(ids) || ids[i] == 0 {
			continue
		}
		select {
		case jobs <- i:
		case <-ctx.Done():
			break send
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

// A title score must never be presented as a content similarity.
const NoDirectSimilarity = -1.0

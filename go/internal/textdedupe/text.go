package textdedupe

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"unicode/utf8"

	"media-dedupe/internal/model"
	"media-dedupe/internal/novelname"
	"media-dedupe/internal/score"
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

// ProgressFunc is called serially with completed work and the current path.
type ProgressFunc func(done, total int, path string)

func Facts(files []model.DiscoveredFile, ids []int64, workers int) ([]Fact, []FileError) {
	return FactsCached(files, ids, nil, workers, nil)
}
func FactsCached(files []model.DiscoveredFile, ids []int64, unchanged []bool, workers int, cache FactCache) ([]Fact, []FileError) {
	return FactsCachedContext(context.Background(), files, ids, unchanged, workers, cache)
}
func FactsCachedContext(ctx context.Context, files []model.DiscoveredFile, ids []int64, unchanged []bool, workers int, cache FactCache) ([]Fact, []FileError) {
	return FactsCachedProgress(ctx, files, ids, unchanged, workers, cache, nil)
}

func FactsCachedProgress(ctx context.Context, files []model.DiscoveredFile, ids []int64, unchanged []bool, workers int, cache FactCache, progress ProgressFunc) ([]Fact, []FileError) {
	workers = max(workers, 1)
	out := make([]Fact, len(files))
	var errs []FileError
	var mu sync.Mutex
	var indexes []int
	for i, f := range files {
		if f.MediaType == model.MediaText && i < len(ids) && ids[i] != 0 {
			indexes = append(indexes, i)
		}
	}
	done := 0
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
				fact, fileErr := cachedFact(ctx, files[i], ids[i], i < len(unchanged) && unchanged[i], cache)
				mu.Lock()
				out[i] = fact
				if fileErr != nil {
					errs = append(errs, *fileErr)
				}
				done++
				if progress != nil && ctx.Err() == nil {
					progress(done, len(indexes), files[i].Path)
				}
				mu.Unlock()
			}
		}()
	}
send:
	for _, i := range indexes {
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

func cachedFact(ctx context.Context, f model.DiscoveredFile, id int64, unchanged bool, cache FactCache) (Fact, *FileError) {
	fact := Fact{FileID: id, Path: f.Path, Size: f.SizeBytes, Quality: score.TextQuality(f.SizeBytes), Names: []novelname.Name{novelname.Parse(filepath.Base(f.Path))}}
	if cache != nil && unchanged {
		cached, ok, err := cache.LoadTextFact(id, f.SizeBytes)
		if err != nil {
			return Fact{}, &FileError{f.Path, "text_cache", err}
		}
		if ok {
			fact.TextFingerprint = cached
			return factWithDecodeWarning(fact)
		}
	}
	content, err := readText(f.Path)
	if err != nil {
		var decoding *decodeError
		if !errors.As(err, &decoding) {
			return Fact{}, &FileError{f.Path, "text", err}
		}
		fact.DecodeError = err.Error()
	} else {
		fact.TextFingerprint = model.TextFingerprint{Digest: textDigest(content), Length: utf8.RuneCountInString(content), Features: featuresContext(ctx, content)}
	}
	if ctx.Err() != nil {
		return Fact{}, nil
	}
	if cache != nil {
		if err := cache.SaveTextFact(id, f.SizeBytes, fact.TextFingerprint); err != nil {
			return Fact{}, &FileError{f.Path, "text_cache", err}
		}
	}
	return factWithDecodeWarning(fact)
}

func factWithDecodeWarning(fact Fact) (Fact, *FileError) {
	if fact.DecodeError != "" {
		return fact, &FileError{fact.Path, "text_decode", errors.New(fact.DecodeError)}
	}
	return fact, nil
}

// A title score must never be presented as a content similarity.
const NoDirectSimilarity = -1.0

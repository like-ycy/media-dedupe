package discovery

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"media-dedupe/internal/config"
	"media-dedupe/internal/fsutil"
	"media-dedupe/internal/model"
)

type ErrorHandler func(path, stage string, err error)

// Discover walks paths and returns media files sorted by directory for HDD
// locality. Walking stops as soon as ctx is canceled, which is how a fatal
// storage error stops a scan instead of traversing the rest of a dying disk.
func Discover(ctx context.Context, paths []string, recursive bool, onError ErrorHandler) []model.DiscoveredFile {
	var found []model.DiscoveredFile
	seen := map[string]struct{}{}

	for _, raw := range paths {
		if ctx.Err() != nil {
			break
		}
		root := fsutil.ExpandPath(raw)
		info, err := os.Stat(root)
		if err != nil {
			record(onError, root, "discovery", err)
			continue
		}
		if info.Mode().IsRegular() {
			if item, ok := collectFile(root, seen, onError); ok {
				found = append(found, item)
			}
			continue
		}
		if !info.IsDir() {
			continue
		}
		if recursive {
			found = append(found, walkDir(ctx, root, seen, onError)...)
		} else {
			entries, err := os.ReadDir(root)
			if err != nil {
				record(onError, root, "discovery", err)
				continue
			}
			for _, entry := range entries {
				if ctx.Err() != nil {
					return found
				}
				if entry.IsDir() {
					continue
				}
				child := filepath.Join(root, entry.Name())
				if item, ok := collectFile(child, seen, onError); ok {
					found = append(found, item)
				}
			}
		}
	}

	sort.Slice(found, func(i, j int) bool {
		di, dj := filepath.Dir(found[i].Path), filepath.Dir(found[j].Path)
		if di != dj {
			return di < dj
		}
		return found[i].Path < found[j].Path
	})
	return found
}

func walkDir(ctx context.Context, root string, seen map[string]struct{}, onError ErrorHandler) []model.DiscoveredFile {
	var found []model.DiscoveredFile
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		// A canceled scan (fatal storage error or user cancel) must stop the
		// walk instead of enumerating the remaining tree.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			record(onError, path, "discovery", err)
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if path == root {
				return nil
			}
			if strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			if _, skip := config.SkippedDirNames[name]; skip {
				return fs.SkipDir
			}
			// Skip symlink directories to avoid cycles.
			if info, statErr := os.Lstat(path); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			// Follow only regular files; skip devices/fifos.
			if d.Type()&os.ModeSymlink != 0 {
				if item, ok := collectFile(path, seen, onError); ok {
					found = append(found, item)
				}
			}
			return nil
		}
		if item, ok := collectFile(path, seen, onError); ok {
			found = append(found, item)
		}
		return nil
	})
	return found
}

func collectFile(path string, seen map[string]struct{}, onError ErrorHandler) (model.DiscoveredFile, bool) {
	mediaType, ok := Classify(path)
	if !ok {
		return model.DiscoveredFile{}, false
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		realPath = path
	}
	if _, dup := seen[realPath]; dup {
		return model.DiscoveredFile{}, false
	}
	seen[realPath] = struct{}{}

	size, mtimeNs, inode, device, err := fsutil.SafeStat(path)
	if err != nil {
		record(onError, path, "discovery", err)
		return model.DiscoveredFile{}, false
	}
	return model.DiscoveredFile{
		Path:      path,
		MediaType: mediaType,
		Extension: strings.ToLower(filepath.Ext(path)),
		SizeBytes: size,
		MTimeNs:   mtimeNs,
		Inode:     inode,
		Device:    device,
	}, true
}

// Classify returns media type from extension.
func Classify(path string) (model.MediaType, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	if _, ok := config.ImageExtensions[ext]; ok {
		return model.MediaImage, true
	}
	if _, ok := config.VideoExtensions[ext]; ok {
		return model.MediaVideo, true
	}
	if _, ok := config.TextExtensions[ext]; ok {
		return model.MediaText, true
	}
	return "", false
}

// SupportsPHash reports whether we can compute perceptual hashes for this extension.
func SupportsPHash(ext string) bool {
	_, skipped := config.PHashSkippedExtensions[strings.ToLower(ext)]
	return !skipped
}

func record(handler ErrorHandler, path, stage string, err error) {
	if handler != nil {
		handler(path, stage, err)
	}
}

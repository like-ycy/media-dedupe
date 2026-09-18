package mediastore

import (
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Store serves thumbs/frames from project data dirs, plus explicitly
// allowed individual files (e.g. original image previews).
// Path traversal is rejected; only allowed roots/files can be served.
type Store struct {
	mu    sync.RWMutex
	roots map[string]struct{}
	files map[string]struct{}
	base  string // e.g. http://127.0.0.1:52341/media
}

func New() *Store {
	return &Store{
		roots: map[string]struct{}{},
		files: map[string]struct{}{},
	}
}

// SetBase sets absolute base URL prefix used when building media URLs.
func (s *Store) SetBase(base string) {
	s.mu.Lock()
	s.base = strings.TrimRight(base, "/")
	s.mu.Unlock()
}

// AllowRoot registers a directory that may be served.
func (s *Store) AllowRoot(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return err
	}
	s.mu.Lock()
	s.roots[abs] = struct{}{}
	s.mu.Unlock()
	return nil
}

// AllowFile registers a single file path that may be served (no dir create).
func (s *Store) AllowFile(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	abs = filepath.Clean(abs)
	fi, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return ErrNotAllowed
	}
	s.mu.Lock()
	s.files[abs] = struct{}{}
	s.mu.Unlock()
	return nil
}

func (s *Store) rootsSnapshot() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.roots))
	for r := range s.roots {
		out = append(out, r)
	}
	return out
}

func (s *Store) resolve(abs string) bool {
	abs = filepath.Clean(abs)
	s.mu.RLock()
	_, fileOK := s.files[abs]
	s.mu.RUnlock()
	if fileOK {
		return true
	}
	for _, root := range s.rootsSnapshot() {
		root = filepath.Clean(root)
		if abs == root || strings.HasPrefix(abs, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func encodeAbs(abs string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(filepath.ToSlash(abs)))
}

func decodeAbs(token string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", err
	}
	return filepath.FromSlash(string(b)), nil
}

// URL returns an absolute http URL for a file under an allowed root.
func (s *Store) URL(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	abs = filepath.Clean(abs)
	if !s.resolve(abs) {
		return ""
	}
	s.mu.RLock()
	base := s.base
	s.mu.RUnlock()
	if base == "" {
		return ""
	}
	// Trailing slash keeps Go ServeMux "/media/" match without a 307 redirect.
	return base + "/?f=" + encodeAbs(abs)
}

// Handler serves media files by absolute path token (f=).
func (s *Store) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("f")
		if token == "" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		abs, err := decodeAbs(token)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		abs = filepath.Clean(abs)
		if !s.resolve(abs) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		fi, err := os.Stat(abs)
		if err != nil || fi.IsDir() {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.ServeFile(w, r, abs)
	})
}

var ErrNotAllowed = errors.New("path not allowed")

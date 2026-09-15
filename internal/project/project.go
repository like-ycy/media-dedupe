package project

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"media-dedupe/internal/config"
	"media-dedupe/internal/model"
)

type LastSummary struct {
	FilesSeen         int   `json:"filesSeen"`
	Groups            int   `json:"groups"`
	ReclaimableBytes  int64 `json:"reclaimableBytes"`
	ExactGroups       int   `json:"exactGroups"`
	SimilarGroups     int   `json:"similarGroups"`
	PendingGroups     int   `json:"pendingGroups"`
}

type Project struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	CreatedAt     string      `json:"createdAt"`
	UpdatedAt     string      `json:"updatedAt"`
	Paths         []string    `json:"paths"`
	Recursive     bool        `json:"recursive"`
	Threshold     float64     `json:"threshold"`
	IncludeImages bool        `json:"includeImages"`
	IncludeVideos bool        `json:"includeVideos"`
	FrameCount    int         `json:"frameCount"`
	Workers       int         `json:"workers"`
	VideoWorkers  int         `json:"videoWorkers"`
	EnableThumbs  bool        `json:"enableThumbs"`
	LastScanAt    *string     `json:"lastScanAt"`
	LastSummary   LastSummary `json:"lastSummary"`
}

type Settings struct {
	DefaultThreshold      float64 `json:"defaultThreshold"`
	DefaultIncludeVideos  bool    `json:"defaultIncludeVideos"`
	DefaultFrameCount     int     `json:"defaultFrameCount"`
	DefaultWorkers        int     `json:"defaultWorkers"`
	DefaultVideoWorkers   int     `json:"defaultVideoWorkers"`
	DefaultDeleteMode     string  `json:"defaultDeleteMode"`
	AllowPermanentDelete  bool    `json:"allowPermanentDelete"`
	FFmpegPath            string  `json:"ffmpegPath"`
	FFprobePath           string  `json:"ffprobePath"`
}

type CreateInput struct {
	Name          string   `json:"name"`
	Paths         []string `json:"paths"`
	Recursive     bool     `json:"recursive"`
	Threshold     float64  `json:"threshold"`
	IncludeImages bool     `json:"includeImages"`
	IncludeVideos bool     `json:"includeVideos"`
	FrameCount    int      `json:"frameCount"`
	Workers       int      `json:"workers"`
	VideoWorkers  int      `json:"videoWorkers"`
	EnableThumbs  bool     `json:"enableThumbs"`
}

type UpdateInput struct {
	Name          *string   `json:"name"`
	Paths         *[]string `json:"paths"`
	Recursive     *bool     `json:"recursive"`
	Threshold     *float64  `json:"threshold"`
	IncludeImages *bool     `json:"includeImages"`
	IncludeVideos *bool     `json:"includeVideos"`
	FrameCount    *int      `json:"frameCount"`
	Workers       *int      `json:"workers"`
	VideoWorkers  *int      `json:"videoWorkers"`
	EnableThumbs  *bool     `json:"enableThumbs"`
}

type Store struct {
	mu       sync.RWMutex
	root     string
	projects map[string]*Project
}

func DefaultSettings() Settings {
	return Settings{
		DefaultThreshold:     0.8,
		DefaultIncludeVideos: true,
		DefaultFrameCount:    8,
		DefaultWorkers:       2,
		DefaultVideoWorkers:  1,
		DefaultDeleteMode:    "recycle",
		AllowPermanentDelete: false,
	}
}

func Open(root string) (*Store, error) {
	if root == "" {
		root = config.AppRoot()
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	s := &Store{root: root, projects: map[string]*Project{}}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Root() string { return s.root }

func (s *Store) ProjectDir(id string) string {
	return filepath.Join(s.root, "projects", id)
}

func (s *Store) CachePath(id string) string {
	return filepath.Join(s.ProjectDir(id), "cache.sqlite")
}

func (s *Store) ThumbDir(id string) string {
	return filepath.Join(s.ProjectDir(id), "thumbs")
}

func (s *Store) FrameDir(id string) string {
	return filepath.Join(s.ProjectDir(id), "frames")
}

func (s *Store) ReportDir(id string) string {
	return filepath.Join(s.ProjectDir(id), "reports")
}

func (s *Store) settingsPath() string {
	return filepath.Join(s.root, "settings.json")
}

func (s *Store) projectsPath() string {
	return filepath.Join(s.root, "projects.json")
}

func (s *Store) load() error {
	b, err := os.ReadFile(s.projectsPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var list []*Project
	if err := json.Unmarshal(b, &list); err != nil {
		return err
	}
	for _, p := range list {
		if p != nil && p.ID != "" {
			s.projects[p.ID] = p
		}
	}
	return nil
}

func (s *Store) persist() error {
	list := make([]*Project, 0, len(s.projects))
	for _, p := range s.projects {
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt > list[j].CreatedAt })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.projectsPath(), append(b, '\n'), 0o644)
}

func (s *Store) List() []Project {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Project, 0, len(s.projects))
	for _, p := range s.projects {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		li, lj := out[i].LastScanAt, out[j].LastScanAt
		if li == nil && lj == nil {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		if li == nil {
			return false
		}
		if lj == nil {
			return true
		}
		return *li > *lj
	})
	return out
}

func (s *Store) Get(id string) (Project, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.projects[id]
	if !ok {
		return Project{}, false
	}
	return *p, true
}

func (s *Store) Create(in CreateInput) (Project, error) {
	name := trimSpace(in.Name)
	if name == "" {
		return Project{}, errors.New("项目名称不能为空")
	}
	if len(in.Paths) == 0 {
		return Project{}, errors.New("至少需要一个扫描目录")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	p := &Project{
		ID:            newID(),
		Name:          name,
		CreatedAt:     now,
		UpdatedAt:     now,
		Paths:         append([]string(nil), in.Paths...),
		Recursive:     in.Recursive,
		Threshold:     in.Threshold,
		IncludeImages: in.IncludeImages,
		IncludeVideos: in.IncludeVideos,
		FrameCount:    in.FrameCount,
		Workers:       in.Workers,
		VideoWorkers:  in.VideoWorkers,
		EnableThumbs:  true,
	}
	if !p.IncludeImages && !p.IncludeVideos {
		p.IncludeImages = true
	}
	if p.Threshold <= 0 {
		p.Threshold = 0.8
	}
	if p.FrameCount < 1 {
		p.FrameCount = 8
	}
	if p.Workers < 1 {
		p.Workers = 2
	}
	if p.VideoWorkers < 1 {
		p.VideoWorkers = 1
	}
	for _, path := range p.Paths {
		_ = os.MkdirAll(s.ProjectDir(p.ID), 0o755)
		_ = path
	}
	if err := os.MkdirAll(s.ProjectDir(p.ID), 0o755); err != nil {
		return Project{}, err
	}

	s.mu.Lock()
	s.projects[p.ID] = p
	err := s.persist()
	s.mu.Unlock()
	if err != nil {
		return Project{}, err
	}
	return *p, nil
}

func (s *Store) Update(id string, in UpdateInput) (Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[id]
	if !ok {
		return Project{}, errors.New("项目不存在")
	}
	if in.Name != nil {
		name := trimSpace(*in.Name)
		if name == "" {
			return Project{}, errors.New("项目名称不能为空")
		}
		p.Name = name
	}
	if in.Paths != nil {
		if len(*in.Paths) == 0 {
			return Project{}, errors.New("至少需要一个扫描目录")
		}
		p.Paths = append([]string(nil), (*in.Paths)...)
	}
	if in.Recursive != nil {
		p.Recursive = *in.Recursive
	}
	if in.Threshold != nil && *in.Threshold > 0 {
		p.Threshold = *in.Threshold
	}
	if in.IncludeImages != nil {
		p.IncludeImages = *in.IncludeImages
	}
	if in.IncludeVideos != nil {
		p.IncludeVideos = *in.IncludeVideos
	}
	if in.FrameCount != nil && *in.FrameCount > 0 {
		p.FrameCount = *in.FrameCount
	}
	if in.Workers != nil && *in.Workers > 0 {
		p.Workers = *in.Workers
	}
	if in.VideoWorkers != nil && *in.VideoWorkers > 0 {
		p.VideoWorkers = *in.VideoWorkers
	}
	if in.EnableThumbs != nil {
		p.EnableThumbs = *in.EnableThumbs
	}
	p.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := s.persist(); err != nil {
		return Project{}, err
	}
	return *p, nil
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projects[id]; !ok {
		return errors.New("项目不存在")
	}
	delete(s.projects, id)
	if err := s.persist(); err != nil {
		return err
	}
	// Remove project data dir (cache/thumbs/frames) — never media files.
	return os.RemoveAll(s.ProjectDir(id))
}

func (s *Store) SaveLastScan(id string, summary LastSummary) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[id]
	if !ok {
		return errors.New("项目不存在")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	p.LastScanAt = &now
	p.LastSummary = summary
	p.UpdatedAt = now
	return s.persist()
}

func (s *Store) LoadSettings() (Settings, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, err := os.ReadFile(s.settingsPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DefaultSettings(), nil
		}
		return Settings{}, err
	}
	st := DefaultSettings()
	if err := json.Unmarshal(b, &st); err != nil {
		return DefaultSettings(), err
	}
	return st, nil
}

func (s *Store) SaveSettings(st Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st.DefaultThreshold <= 0 {
		st.DefaultThreshold = 0.8
	}
	if st.DefaultFrameCount < 1 {
		st.DefaultFrameCount = 8
	}
	if st.DefaultWorkers < 1 {
		st.DefaultWorkers = 2
	}
	if st.DefaultVideoWorkers < 1 {
		st.DefaultVideoWorkers = 1
	}
	if st.DefaultDeleteMode == "" {
		st.DefaultDeleteMode = "recycle"
	}
	if st.DefaultDeleteMode != "recycle" && st.DefaultDeleteMode != "permanent" {
		return fmt.Errorf("invalid delete mode: %s", st.DefaultDeleteMode)
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.settingsPath(), append(b, '\n'), 0o644)
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "prj_" + hex.EncodeToString(b[:])
}

func trimSpace(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

// ComputeSummary builds LastSummary from group list.
func ComputeSummary(groups []model.ReportGroup) LastSummary {
	var sum LastSummary
	sum.Groups = len(groups)
	for _, g := range groups {
		switch g.GroupType {
		case model.GroupExact:
			sum.ExactGroups++
		case model.GroupSimilarImage, model.GroupSimilarVideo:
			sum.SimilarGroups++
		}
		if g.Status == model.GroupPending || g.Status == "" {
			sum.PendingGroups++
		}
		for _, it := range g.Items {
			if g.RecommendedFileID > 0 && it.FileID != g.RecommendedFileID {
				sum.ReclaimableBytes += it.SizeBytes
			} else if g.RecommendedFileID <= 0 && it.Action == model.ActionCleanup {
				sum.ReclaimableBytes += it.SizeBytes
			}
		}
	}
	return sum
}

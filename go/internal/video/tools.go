package video

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

var (
	toolsMu       sync.RWMutex
	customFFmpeg  string
	customFFprobe string
)

// ConfigureTools applies user overrides from settings and augments PATH so
// GUI apps can find ffmpeg/ffprobe installed outside the default app PATH.
func ConfigureTools(ffmpegPath, ffprobePath string) {
	toolsMu.Lock()
	customFFmpeg = expandPath(ffmpegPath)
	customFFprobe = expandPath(ffprobePath)
	toolsMu.Unlock()
	augmentPATH()
}

// Tools returns resolved absolute paths for ffmpeg and ffprobe (empty if missing).
func Tools() (ffmpeg, ffprobe string) {
	ffOverride, fpOverride := customOverrides()
	ffmpeg = resolveTool("ffmpeg", ffOverride)
	ffprobe = resolveTool("ffprobe", fpOverride)
	if ffprobe == "" && ffmpeg != "" {
		// Common layout: both binaries live in the same directory.
		ffprobe = resolveTool("ffprobe", filepath.Dir(ffmpeg))
	}
	return ffmpeg, ffprobe
}

// Available reports whether both ffmpeg and ffprobe can be resolved.
func Available() (ffmpeg, ffprobe bool) {
	f, p := Tools()
	return f != "", p != ""
}

func customOverrides() (ffmpeg, ffprobe string) {
	toolsMu.RLock()
	defer toolsMu.RUnlock()
	return customFFmpeg, customFFprobe
}

func ffmpegBin() string {
	if p, _ := Tools(); p != "" {
		return p
	}
	return "ffmpeg"
}

func ffprobeBin() string {
	if _, p := Tools(); p != "" {
		return p
	}
	return "ffprobe"
}

func resolveTool(name, override string) string {
	exe := exeName(name)
	if override != "" {
		if st, err := os.Stat(override); err == nil && !st.IsDir() {
			return override
		}
		candidate := filepath.Join(override, exe)
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range commonToolDirs() {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, exe)
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate
		}
	}
	return ""
}

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func expandPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = os.ExpandEnv(p)
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			if p == "~" {
				p = home
			} else if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
				p = filepath.Join(home, p[2:])
			}
		}
	}
	return p
}

func commonToolDirs() []string {
	var dirs []string
	home, _ := os.UserHomeDir()

	switch runtime.GOOS {
	case "darwin":
		dirs = []string{
			"/opt/homebrew/bin",
			"/usr/local/bin",
			"/opt/local/bin",
			"/usr/bin",
		}
		if home != "" {
			dirs = append(dirs, filepath.Join(home, "bin"))
		}
	case "windows":
		add := func(elems ...string) {
			p := filepath.Join(elems...)
			if p != "" {
				dirs = append(dirs, p)
			}
		}
		if pf := os.Getenv("ProgramFiles"); pf != "" {
			add(pf, "ffmpeg", "bin")
		}
		if pf86 := os.Getenv("ProgramFiles(x86)"); pf86 != "" {
			add(pf86, "ffmpeg", "bin")
		}
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			add(local, "Microsoft", "WinGet", "Links")
			add(local, "Programs", "ffmpeg", "bin")
		}
		if profile := os.Getenv("USERPROFILE"); profile != "" {
			add(profile, "scoop", "shims")
		}
		if programData := os.Getenv("ProgramData"); programData != "" {
			add(programData, "chocolatey", "bin")
		}
		dirs = append(dirs, `C:\ffmpeg\bin`)
	default:
		dirs = []string{"/usr/local/bin", "/usr/bin", "/snap/bin"}
		if home != "" {
			dirs = append(dirs, filepath.Join(home, ".local", "bin"))
		}
	}
	return dirs
}

func augmentPATH() {
	ff, fp := Tools()
	seen := map[string]bool{}
	var prefix []string
	addDir := func(dir string) {
		if dir == "" || seen[dir] {
			return
		}
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			return
		}
		seen[dir] = true
		prefix = append(prefix, dir)
	}
	if ff != "" {
		addDir(filepath.Dir(ff))
	}
	if fp != "" {
		addDir(filepath.Dir(fp))
	}
	for _, dir := range commonToolDirs() {
		addDir(dir)
	}
	if len(prefix) == 0 {
		return
	}

	existing := os.Getenv("PATH")
	var rest []string
	if existing != "" {
		for _, part := range strings.Split(existing, string(os.PathListSeparator)) {
			if part == "" || seen[part] {
				continue
			}
			rest = append(rest, part)
		}
	}
	_ = os.Setenv("PATH", strings.Join(append(prefix, rest...), string(os.PathListSeparator)))
}

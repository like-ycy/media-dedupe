package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ExecutableDir returns the directory that contains the running program.
//
// - Normal binary: directory of the executable (symlinks resolved).
// - `go run` / temp-dir binaries: current working directory.
func ExecutableDir() string {
	exe, err := os.Executable()
	if err != nil {
		return workingDir()
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	if isTempDir(dir) {
		return workingDir()
	}
	return dir
}

// AppRoot returns the cache / project data root.
//
// Windows (primary): <program_dir>/media-dedupe-cache
//
//	Visible next to the exe so a finished project can be deleted wholesale.
//	WebView2's EBWebView is left alone; the OS manages it.
//
// macOS / other (debug): ~/.media-dedupe
//
//	Local runs only touch a few test files, so a home-dir root is enough.
func AppRoot() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(ExecutableDir(), "media-dedupe-cache")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(ExecutableDir(), "media-dedupe-cache")
	}
	return filepath.Join(home, ".media-dedupe")
}

// EnsureAppRoot creates the app root directory if missing.
func EnsureAppRoot() (string, error) {
	root := AppRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	return root, nil
}

func workingDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

func isTempDir(dir string) bool {
	tmp := os.TempDir()
	if tmp == "" {
		return false
	}
	absTmp, err := filepath.Abs(tmp)
	if err != nil {
		return false
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	return absDir == absTmp || strings.HasPrefix(absDir, absTmp+string(filepath.Separator))
}

package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ExecutableDir returns the directory that contains the running program.
// Portable layout: all scan caches / project data live under this directory
// so users can see them and delete them after finishing a project.
//
// - Normal binary: directory of the executable (symlinks resolved).
// - macOS .app bundle: the folder containing the .app, not Contents/MacOS.
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
	if runtime.GOOS == "darwin" {
		if parent := macAppBundleParent(dir); parent != "" {
			return parent
		}
	}
	return dir
}

// AppRoot returns the portable data root next to the running program.
// Layout: <program_dir>/data — one visible folder users can delete wholesale.
//
// WebView2's EBWebView (Windows) is left alone; the OS manages it.
func AppRoot() string {
	return filepath.Join(ExecutableDir(), "data")
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

// macAppBundleParent maps .../Name.app/Contents/MacOS → parent of Name.app.
func macAppBundleParent(dir string) string {
	macOS := filepath.Dir(dir)
	contents := filepath.Dir(macOS)
	appBundle := filepath.Dir(contents)
	if filepath.Base(macOS) == "MacOS" &&
		filepath.Base(contents) == "Contents" &&
		strings.HasSuffix(appBundle, ".app") {
		return filepath.Dir(appBundle)
	}
	return ""
}

package config

import (
	"os"
	"path/filepath"
	"runtime"
)

// AppRoot returns the per-user application data root for media-dedupe App.
// Windows: %AppData%/media-dedupe
// macOS:   ~/Library/Application Support/media-dedupe
// other:   $XDG_DATA_HOME/media-dedupe or ~/.local/share/media-dedupe
func AppRoot() string {
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("APPDATA")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return ".media-dedupe"
			}
			base = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(base, "media-dedupe")
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return ".media-dedupe"
		}
		return filepath.Join(home, "Library", "Application Support", "media-dedupe")
	default:
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			return filepath.Join(xdg, "media-dedupe")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return ".media-dedupe"
		}
		return filepath.Join(home, ".local", "share", "media-dedupe")
	}
}

// EnsureAppRoot creates the app root directory if missing.
func EnsureAppRoot() (string, error) {
	root := AppRoot()
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	return root, nil
}

//go:build darwin

package ops

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func trashPath(path string) error {
	script := fmt.Sprintf(`tell application "Finder" to delete POSIX file "%s"`, escapeAppleScript(path))
	cmd := exec.Command("osascript", "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Fallback to trashutil via mv into ~/.Trash
		home, herr := os.UserHomeDir()
		if herr != nil {
			return fmt.Errorf("recycle failed: %v (%s)", err, strings.TrimSpace(string(out)))
		}
		trashDir := filepath.Join(home, ".Trash")
		base := filepath.Base(path)
		dst := filepath.Join(trashDir, base)
		if _, statErr := os.Stat(dst); statErr == nil {
			dst = filepath.Join(trashDir, fmt.Sprintf("%s.%d", base, os.Getpid()))
		}
		if mvErr := os.Rename(path, dst); mvErr != nil {
			return fmt.Errorf("recycle failed: %v (fallback: %v)", err, mvErr)
		}
		return nil
	}
	return nil
}

func escapeAppleScript(s string) string {
	return strings.ReplaceAll(s, `"`, `\"`)
}

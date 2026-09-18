package ops

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type FailedItem struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

type Request struct {
	Mode     string   `json:"mode"` // recycle | permanent
	Paths    []string `json:"paths"`
	GroupIDs []int64  `json:"groupIds"`
}

type Result struct {
	Succeeded []string     `json:"succeeded"`
	Failed    []FailedItem `json:"failed"`
	Mode      string       `json:"mode"`
}

// DeleteFiles deletes paths with recycle (default) or permanent mode.
// Recycle never falls back silently to permanent.
func DeleteFiles(req Request) Result {
	mode := req.Mode
	if mode == "" {
		mode = "recycle"
	}
	res := Result{Mode: mode, Succeeded: []string{}, Failed: []FailedItem{}}
	if mode != "recycle" && mode != "permanent" {
		res.Failed = append(res.Failed, FailedItem{Path: "", Message: "invalid delete mode: " + mode})
		return res
	}
	for _, path := range req.Paths {
		path = strings.TrimSpace(path)
		if path == "" {
			res.Failed = append(res.Failed, FailedItem{Path: path, Message: "empty path"})
			continue
		}
		if _, err := os.Lstat(path); err != nil {
			res.Failed = append(res.Failed, FailedItem{Path: path, Message: err.Error()})
			continue
		}
		if isProtected(path) {
			res.Failed = append(res.Failed, FailedItem{Path: path, Message: "protected system path"})
			continue
		}
		var err error
		if mode == "permanent" {
			err = removePermanent(path)
		} else {
			err = moveToTrash(path)
		}
		if err != nil {
			res.Failed = append(res.Failed, FailedItem{Path: path, Message: err.Error()})
			continue
		}
		res.Succeeded = append(res.Succeeded, path)
	}
	return res
}

func removePermanent(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return os.RemoveAll(path)
	}
	return os.Remove(path)
}

func moveToTrash(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	switch runtime.GOOS {
	case "darwin":
		return trashMac(abs)
	case "windows":
		return trashWindows(abs)
	default:
		// MVP: Linux falls back to permanent with explicit message — never silent.
		err := os.Remove(abs)
		if err != nil {
			return err
		}
		return errors.New("Linux 回收站未实现，已永久删除（permanent fallback）")
	}
}

func trashMac(path string) error {
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

func trashWindows(path string) error {
	// PowerShell Microsoft.VisualBasic.FileIO — moves to Recycle Bin; fails loudly.
	ps := fmt.Sprintf(
		`[Microsoft.VisualBasic.FileIO.FileSystem]::DeleteFile('%s','OnlyErrorDialogs','SendToRecycleBin')`,
		windowsQuote(path),
	)
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("recycle failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func windowsQuote(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

func isProtected(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return true
	}
	abs = filepath.Clean(abs)
	if abs == "/" || abs == `C:\` || abs == `C:` {
		return true
	}
	lower := strings.ToLower(abs)
	protected := []string{
		`c:\windows`,
		`c:\program files`,
		`c:\program files (x86)`,
		"/system",
		"/library",
		"/usr",
		"/bin",
		"/sbin",
		"/etc",
		"/proc",
	}
	for _, p := range protected {
		if strings.HasPrefix(lower, p+string(filepath.Separator)) || lower == p {
			return true
		}
	}
	return false
}

// OpenPath opens path with the system default application.
func OpenPath(path string) error {
	if _, err := os.Lstat(path); err != nil {
		return err
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	return cmd.Start()
}

// RevealInFolder reveals path in Finder / Explorer.
func RevealInFolder(path string) error {
	if _, err := os.Lstat(path); err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", "-R", abs)
	case "windows":
		cmd = exec.Command("explorer", "/select,", abs)
	default:
		cmd = exec.Command("xdg-open", filepath.Dir(abs))
	}
	return cmd.Start()
}

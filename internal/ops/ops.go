package ops

import (
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
	return trashPath(abs)
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
	prepareCmd(cmd)
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
	prepareCmd(cmd)
	return cmd.Start()
}

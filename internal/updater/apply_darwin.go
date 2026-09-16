//go:build darwin

package updater

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ApplyAndRestart 在 macOS 上解压 tar.gz，替换当前 .app 应用程序包并重启。
func (m *Manager) ApplyAndRestart() error {
	pkgPath, tempDir, err := m.GetDownloadedFile()
	if err != nil {
		return err
	}

	currentAppPath, err := findCurrentAppBundle()
	if err != nil {
		return fmt.Errorf("定位当前应用目录失败: %w", err)
	}

	extractedDir := filepath.Join(tempDir, "extracted")
	if err := os.MkdirAll(extractedDir, 0o755); err != nil {
		return fmt.Errorf("创建解压目录失败: %w", err)
	}

	if err := extractTarGz(pkgPath, extractedDir); err != nil {
		return fmt.Errorf("解压更新包失败: %w", err)
	}

	newAppPath, err := findAppBundleInDir(extractedDir)
	if err != nil {
		return fmt.Errorf("未在更新包中找到 .app 目录: %w", err)
	}

	// 脚本在独立会话中运行，等主进程退出后替换 .app 并拉起新版本。
	pid := os.Getpid()
	scriptContent := `
PID="$1"
TARGET_APP="$2"
NEW_APP="$3"
CLEAN_DIR="$4"
BACKUP_APP="${TARGET_APP}.old-$$"

while kill -0 "$PID" 2>/dev/null; do
    sleep 0.2
done

rm -rf "$BACKUP_APP"
if ! mv "$TARGET_APP" "$BACKUP_APP"; then
    exit 1
fi
if ! mv "$NEW_APP" "$TARGET_APP"; then
    mv "$BACKUP_APP" "$TARGET_APP"
    exit 1
fi

xattr -dr com.apple.quarantine "$TARGET_APP" 2>/dev/null || true

open "$TARGET_APP"

rm -rf "$BACKUP_APP" 2>/dev/null || true
rm -rf "$CLEAN_DIR" 2>/dev/null || true
`

	cmd := exec.Command("/bin/bash", "-c", scriptContent, "updater-script",
		strconv.Itoa(pid),
		currentAppPath,
		newAppPath,
		tempDir,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动后台更新脚本失败: %w", err)
	}
	m.markApplyPending()
	return nil
}

// findCurrentAppBundle 根据当前可执行文件反查外层的 .app 路径。
func findCurrentAppBundle() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(exePath)
	if err == nil {
		exePath = resolved
	}

	curr := exePath
	for {
		if strings.HasSuffix(curr, ".app") {
			return curr, nil
		}
		parent := filepath.Dir(curr)
		if parent == curr || parent == "/" || parent == "." {
			break
		}
		curr = parent
	}

	return "", fmt.Errorf("当前程序运行路径为 %s，未处于 .app 应用程序包中（开发/调试模式下不支持就地替换，请打包后测试）", exePath)
}

// findAppBundleInDir 在目录中查找以 .app 结尾的目录（含一层子目录）。
func findAppBundleInDir(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}

	for _, entry := range entries {
		if entry.IsDir() && strings.HasSuffix(entry.Name(), ".app") {
			return filepath.Join(dir, entry.Name()), nil
		}
	}

	for _, entry := range entries {
		if entry.IsDir() {
			subEntries, sErr := os.ReadDir(filepath.Join(dir, entry.Name()))
			if sErr == nil {
				for _, sub := range subEntries {
					if sub.IsDir() && strings.HasSuffix(sub.Name(), ".app") {
						return filepath.Join(dir, entry.Name(), sub.Name()), nil
					}
				}
			}
		}
	}

	return "", fmt.Errorf("未找到 .app 目录")
}

// extractTarGz 解压 .tar.gz 更新包，防止路径穿越。
func extractTarGz(pkgPath, destDir string) error {
	f, err := os.Open(pkgPath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(destDir, hdr.Name)
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)+string(filepath.Separator)) {
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		case tar.TypeLink, tar.TypeSymlink:
			// 跳过链接，.app 包内正常不需要
		}
	}
	return nil
}

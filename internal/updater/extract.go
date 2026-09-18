package updater

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// extractZip 解压 .zip 更新包，防止路径穿越。
// 需保留可执行权限与符号链接，否则 macOS .app 替换后可能无法启动。
func extractZip(pkgPath, destDir string) error {
	r, err := zip.OpenReader(pkgPath)
	if err != nil {
		return err
	}
	defer r.Close()

	cleanDest := filepath.Clean(destDir)
	if err := os.MkdirAll(cleanDest, 0o755); err != nil {
		return err
	}
	// macOS 上临时目录常是 /var -> /private/var 符号链接，比较前先解析。
	resolvedDest, err := filepath.EvalSymlinks(cleanDest)
	if err != nil {
		return err
	}
	destPrefix := resolvedDest + string(filepath.Separator)

	for _, f := range r.File {
		target := filepath.Join(cleanDest, filepath.FromSlash(f.Name))
		if !strings.HasPrefix(filepath.Clean(target), cleanDest+string(filepath.Separator)) {
			continue
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := ensureParentUnderDest(resolvedDest, destPrefix, filepath.Dir(target)); err != nil {
			return fmt.Errorf("非法解压路径 %s: %w", f.Name, err)
		}

		if f.Mode()&os.ModeSymlink != 0 {
			if err := writeSymlink(f, target); err != nil {
				return err
			}
			continue
		}

		if err := writeRegularFile(f, target); err != nil {
			return err
		}
	}

	return ensureExtractedNotEmpty(cleanDest)
}

func ensureParentUnderDest(resolvedDest, destPrefix, parent string) error {
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return err
	}
	if resolvedParent != resolvedDest && !strings.HasPrefix(resolvedParent, destPrefix) {
		return fmt.Errorf("路径解析到目标目录之外: %s", resolvedParent)
	}
	return nil
}

func writeSymlink(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	linkBytes, readErr := io.ReadAll(rc)
	closeErr := rc.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}

	link := string(linkBytes)
	if link == "" || filepath.IsAbs(link) || strings.Contains(link, "..") {
		return fmt.Errorf("不支持的符号链接 %s -> %q", f.Name, link)
	}

	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Symlink(link, target)
}

func writeRegularFile(f *zip.File, target string) error {
	perm := f.Mode().Perm()
	if perm == 0 {
		perm = 0o644
	}

	rc, err := f.Open()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		rc.Close()
		return err
	}
	_, copyErr := io.Copy(out, rc)
	closeRC := rc.Close()
	closeOut := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeRC != nil {
		return closeRC
	}
	return closeOut
}

func ensureExtractedNotEmpty(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("更新包解压结果为空: %s", dir)
	}
	return nil
}

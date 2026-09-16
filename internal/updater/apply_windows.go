//go:build windows

package updater

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// ApplyAndRestart 在 Windows 上解压 tar.gz 中的 exe，替换当前程序并重启。
// 运行中的 exe 有文件锁，由隐藏窗口批处理等待退出后完成替换。
func (m *Manager) ApplyAndRestart() error {
	pkgPath, tempDir, err := m.GetDownloadedFile()
	if err != nil {
		return err
	}

	extractedDir := filepath.Join(tempDir, "extracted")
	if err := os.MkdirAll(extractedDir, 0o755); err != nil {
		return fmt.Errorf("创建解压目录失败: %w", err)
	}

	if err := extractTarGz(pkgPath, extractedDir); err != nil {
		return fmt.Errorf("解压更新包失败: %w", err)
	}

	newExePath, err := findExeInDir(extractedDir)
	if err != nil {
		return fmt.Errorf("未在更新包中找到可执行文件: %w", err)
	}

	currentExePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("获取当前可执行文件路径失败: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(currentExePath); err == nil {
		currentExePath = resolved
	}

	batPath := filepath.Join(tempDir, "update.bat")
	batContent := `@echo off
set PID=%1
set TARGET=%~2
set NEW_FILE=%~3
set CLEAN_DIR=%~4
set BACKUP=%TARGET%.old

:wait_exit
tasklist /FI "PID eq %PID%" 2>NUL | find /I "%PID%" >NUL
if "%ERRORLEVEL%"=="0" (
    timeout /t 1 /nobreak >nul
    goto wait_exit
)

set /a ATTEMPTS=0
:retry_move
set /a ATTEMPTS+=1
if exist "%BACKUP%" del /F /Q "%BACKUP%" >nul 2>&1
move /Y "%TARGET%" "%BACKUP%" >nul 2>&1
if errorlevel 1 (
    if %ATTEMPTS% GEQ 30 exit /b 1
    timeout /t 1 /nobreak >nul
    goto retry_move
)
move /Y "%NEW_FILE%" "%TARGET%" >nul 2>&1
if errorlevel 1 (
    move /Y "%BACKUP%" "%TARGET%" >nul 2>&1
    exit /b 1
)

start "" "%TARGET%"

del /F /Q "%BACKUP%" >nul 2>&1

if exist "%CLEAN_DIR%" (
    rd /s /q "%CLEAN_DIR%" >nul 2>&1
)
`
	if err := os.WriteFile(batPath, []byte(batContent), 0o755); err != nil {
		return fmt.Errorf("写入更新批处理脚本失败: %w", err)
	}

	cmd := exec.Command("cmd.exe", "/c", batPath,
		strconv.Itoa(os.Getpid()),
		currentExePath,
		newExePath,
		tempDir,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x08000000 | 0x00000200,
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动更新批处理进程失败: %w", err)
	}
	m.markApplyPending()
	return nil
}

// findExeInDir 在目录中查找可执行文件（含一层子目录）。
func findExeInDir(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(entry.Name()), ".exe") {
			return filepath.Join(dir, entry.Name()), nil
		}
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		subDir := filepath.Join(dir, entry.Name())
		subEntries, sErr := os.ReadDir(subDir)
		if sErr != nil {
			continue
		}
		for _, sub := range subEntries {
			if !sub.IsDir() && strings.HasSuffix(strings.ToLower(sub.Name()), ".exe") {
				return filepath.Join(subDir, sub.Name()), nil
			}
		}
	}
	return "", fmt.Errorf("未找到 .exe 文件")
}

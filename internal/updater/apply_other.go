//go:build !darwin && !windows

package updater

import "fmt"

// ApplyAndRestart 当前平台未实现自动替换。
func (m *Manager) ApplyAndRestart() error {
	return fmt.Errorf("当前操作系统暂不支持自动安装更新，请前往发布页面手动下载")
}

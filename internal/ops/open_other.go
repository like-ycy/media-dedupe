//go:build !windows

package ops

import (
	"os/exec"
	"runtime"
)

func openPath(path string) error {
	if runtime.GOOS == "darwin" {
		cmd := exec.Command("open", path)
		prepareCmd(cmd)
		return cmd.Start()
	}
	cmd := exec.Command("xdg-open", path)
	prepareCmd(cmd)
	return cmd.Start()
}

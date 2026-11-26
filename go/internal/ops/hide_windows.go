//go:build windows

package ops

import (
	"os/exec"
	"syscall"
)

// prepareCmd hides the console window for GUI-launched helpers (rundll32, explorer, …).
func prepareCmd(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}

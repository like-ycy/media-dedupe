//go:build windows

package video

import (
	"os/exec"
	"syscall"
)

// hideConsole prevents a flashing console window for GUI-launched ffmpeg/ffprobe.
func hideConsole(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}

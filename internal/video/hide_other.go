//go:build !windows

package video

import "os/exec"

func hideConsole(cmd *exec.Cmd) {}

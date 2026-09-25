//go:build !windows

package ops

import "os/exec"

func prepareCmd(cmd *exec.Cmd) {}

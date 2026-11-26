//go:build !windows && !darwin

package ops

import (
	"errors"
	"os"
)

// trashPath on non-Windows/non-mac is still MVP: delete and report explicitly — never silent.
func trashPath(path string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	return errors.New("Linux 回收站未实现，已永久删除（permanent fallback）")
}

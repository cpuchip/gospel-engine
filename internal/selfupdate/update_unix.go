//go:build !windows

package selfupdate

import (
	"fmt"
	"os"
	"syscall"
)

func swapAndReexec(exe, staged, newVersion string) error {
	prev := exe + ".prev"
	_ = os.Remove(prev)
	// rename is atomic; the running process keeps its file handle.
	if err := os.Rename(exe, prev); err != nil {
		return fmt.Errorf("renaming current exe: %w", err)
	}
	if err := os.Rename(staged, exe); err != nil {
		_ = os.Rename(prev, exe)
		return fmt.Errorf("renaming staged: %w", err)
	}
	args := append([]string{exe}, os.Args[1:]...)
	env := append(os.Environ(), "GOSPEL_UPDATED_FROM_VERSION="+newVersion)
	return syscall.Exec(exe, args, env)
}

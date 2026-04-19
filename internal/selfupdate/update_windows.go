//go:build windows

package selfupdate

import (
	"fmt"
	"os"
	"os/exec"
)

// Windows can't overwrite a running .exe, so move it aside as .prev,
// rename the staged one into place, and exec the replacement as a fresh
// process. The parent then exits.
func swapAndReexec(exe, staged, newVersion string) error {
	prev := exe + ".prev"
	_ = os.Remove(prev)
	if err := os.Rename(exe, prev); err != nil {
		return fmt.Errorf("renaming current exe to .prev: %w", err)
	}
	if err := os.Rename(staged, exe); err != nil {
		_ = os.Rename(prev, exe)
		return fmt.Errorf("renaming staged into place: %w", err)
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "GOSPEL_UPDATED_FROM_VERSION="+newVersion)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting updated binary: %w", err)
	}
	os.Exit(0)
	return nil
}

//go:build windows

package installer

import (
	"os/exec"
	"syscall"
)

func newPlatformCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
	}
	return cmd
}

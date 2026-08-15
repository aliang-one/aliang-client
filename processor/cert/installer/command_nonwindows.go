//go:build !windows

package installer

import "os/exec"

func newPlatformCommand(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

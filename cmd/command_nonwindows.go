//go:build !windows

package cmd

import "os/exec"

func newBackgroundCommand(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

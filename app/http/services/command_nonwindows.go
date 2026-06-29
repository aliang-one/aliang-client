//go:build !windows

package services

import (
	"context"
	"os/exec"
)

// newBackgroundCommand is the non-Windows counterpart: no window to hide, so it
// is a plain exec. Kept as an indirection so call sites are identical across
// platforms (see command_windows.go for why Windows needs to hide the console).
func newBackgroundCommand(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

func newBackgroundCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

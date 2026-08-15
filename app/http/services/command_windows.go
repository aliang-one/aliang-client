//go:build windows

package services

import (
	"context"
	"os/exec"
	"syscall"
)

// newBackgroundCommand spawns a console child process with its window hidden.
// On Windows, exec'ing any console program (git, claude, codex, …) otherwise
// flashes a cmd window for every invocation — and the inventory scan calls
// `git status` / `git ls-files` per project on a periodic tick, which strobes
// the screen continuously after login. HideWindow (CREATE_NO_WINDOW-equivalent)
// suppresses that. Stdio piping is unaffected, so stdio-based tool protocols
// keep working. Use for every short-lived tool probe / background tool launch.
func newBackgroundCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

func newBackgroundCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd
}

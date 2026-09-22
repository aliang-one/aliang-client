//go:build unix

package services

import "syscall"

// defaultExternalInterrupt sends SIGINT to the pid — byte-for-byte the signal
// the user's Esc keypress produces in the Claude Code TUI: the current turn
// aborts and the TUI stays open. Never SIGKILL: the goal is to interrupt the
// turn, not to kill the interactive session the user is sitting in. A failed
// delivery (including EPERM across a user boundary) is reported as an error so
// the caller degrades to the legacy "stopped" reply.
func defaultExternalInterrupt(pid int) error {
	return syscall.Kill(pid, syscall.SIGINT)
}

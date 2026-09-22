//go:build unix

package services

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// defaultExternalInterrupt sends SIGINT to the pid — byte-for-byte the signal
// the user's Esc keypress produces in the Claude Code TUI: the current turn
// aborts and the TUI stays open. Never SIGKILL: the goal is to interrupt the
// turn, not to kill the interactive session the user is sitting in. A failed
// delivery (including EPERM across a user boundary) is reported as an error so
// the caller degrades to the legacy "stopped" reply.
func defaultExternalInterrupt(pid int) error {
	return syscall.Kill(pid, syscall.SIGINT)
}

// defaultExternalInterruptTargetMatches is the pid-reuse guard: Claude Code
// prunes dead pid records "not always promptly", so a stale record can name a
// recycled pid. Verify the process command line mentions "claude" before
// signaling (the TUI binary and its SDK entrypoints all carry the name).
// `ps` failure is deliberately permissive (returns true) — a flaky probe must
// never degrade the feature; it only exists to catch obvious mismatches.
func defaultExternalInterruptTargetMatches(pid int) bool {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return true
	}
	return strings.Contains(strings.ToLower(string(out)), "claude")
}

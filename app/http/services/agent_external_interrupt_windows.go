//go:build windows

package services

import "errors"

// defaultExternalInterrupt reports unsupported on Windows. Windows'
// GenerateConsoleCtrlEvent(CTRL_C_EVENT) only reaches processes attached to
// the caller's console — the agent's service process cannot reliably reach
// the user's terminal TUI with it, and anything stronger (TerminateProcess)
// is forbidden: it would kill the interactive session instead of interrupting
// the turn. External stop therefore degrades to the legacy "stopped" reply on
// Windows.
func defaultExternalInterrupt(pid int) error {
	return errors.New("external interrupt is not supported on this platform")
}

// defaultExternalInterruptTargetMatches cannot verify process identity
// cheaply on Windows (no /proc, and the interrupt itself is unsupported
// anyway); permissive-by-default keeps the guard from degrading the feature.
func defaultExternalInterruptTargetMatches(pid int) bool {
	return true
}

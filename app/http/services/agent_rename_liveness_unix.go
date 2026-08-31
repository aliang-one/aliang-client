//go:build unix

package services

import "syscall"

// isPidAlive reports whether the process currently exists. A nil error from
// kill(pid, 0) means the signal was deliverable; EPERM means the process
// exists but is owned by another user (e.g. the agent runs as root while the
// Claude session belongs to the login user) — still alive for our purposes.
func isPidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

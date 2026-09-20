//go:build windows

package services

import (
	"golang.org/x/sys/windows"
)

// stillActive is the pseudo exit code Windows reports for a live process
// (NTSTATUS 0x103 / STILL_ACTIVE). x/sys/windows exposes it only as the
// NTStatus STATUS_PENDING, so spell the DWORD value locally.
const stillActive uint32 = 259

// isPidAlive reports whether the process currently exists on Windows.
// PROCESS_QUERY_LIMITED_INFORMATION is the least-privileged query right and is
// typically grantable across user boundaries; ACCESS_DENIED therefore means
// the pid exists but belongs to another user — still alive for our purposes
// (mirrors the unix implementation's EPERM handling). The handle is closed on
// every path; the function is a pure read-only predicate with no other state.
func isPidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		// A process we can describe but not open is a process that exists.
		return err == windows.ERROR_ACCESS_DENIED
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		// Opened but the query failed — treat as alive rather than flashing
		// sessions idle on a transient error.
		return true
	}
	return exitCode == stillActive
}

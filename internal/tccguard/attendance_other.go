//go:build !darwin

package tccguard

// Non-macOS platforms have no TCC "Files & Folders" prompt, so the guard is a
// no-op there. GuardPath still checks supportedFn, which reports false.

func platformSupported() bool { return false }

func platformAttended() bool { return true }

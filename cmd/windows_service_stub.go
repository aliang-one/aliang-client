//go:build !windows

package cmd

// MaybeRunAsWindowsService is a no-op on non-Windows platforms.
func MaybeRunAsWindowsService() (handled bool, err error) {
	return false, nil
}

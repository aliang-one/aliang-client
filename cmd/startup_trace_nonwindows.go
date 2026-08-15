//go:build !windows

package cmd

func writeStartupTrace(format string, args ...interface{}) {
	// Windows-only diagnostic hook.
}

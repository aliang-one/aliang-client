//go:build !windows

package cmd

func MaybeRunWindowsCompanionFromArgs() bool {
	return false
}

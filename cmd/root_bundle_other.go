//go:build !darwin

package cmd

func maybeRunAppBundleCompanion() bool {
	return false
}

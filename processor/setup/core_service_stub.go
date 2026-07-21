//go:build !darwin

package setup

import "fmt"

func InstallMacOSCoreService(execPath string, args []string) error {
	return fmt.Errorf("macOS core service is not supported on this platform")
}

func UninstallMacOSCoreService() error {
	return fmt.Errorf("macOS core service is not supported on this platform")
}

func KickstartCoreService() error {
	return fmt.Errorf("macOS core service is not supported on this platform")
}

func StopCoreServiceViaLaunchctl() error {
	return fmt.Errorf("macOS core service is not supported on this platform")
}

func IsCoreServiceInstalled() bool {
	return false
}

func IsCoreServiceRunning() bool {
	return false
}

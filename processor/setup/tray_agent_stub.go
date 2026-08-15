//go:build !darwin

package setup

import "fmt"

type MacOSTrayAgentStatus struct {
	Label       string
	DisplayName string
	PlistPath   string
	UserName    string
	UID         string
	IsInstalled bool
	IsRunning   bool
	Status      string
	PID         int
}

func GetMacOSTrayAgentStatus() (*MacOSTrayAgentStatus, error) {
	return &MacOSTrayAgentStatus{
		DisplayName: "Aliang Menu Bar Companion",
		Status:      "unsupported",
	}, nil
}

func InstallMacOSTrayAgent(execPath string) error {
	return fmt.Errorf("macOS tray companion is not supported on this platform")
}

func UninstallMacOSTrayAgent() error {
	return fmt.Errorf("macOS tray companion is not supported on this platform")
}

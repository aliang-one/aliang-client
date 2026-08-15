//go:build darwin

package setup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const macOSCoreServiceLabel = "one.aliang.aliang.core"

// InstallMacOSCoreService installs the core service as a LaunchDaemon.
// RunAtLoad=true, KeepAlive=true — the service starts automatically at system boot.
// Core runs as root and uses system-level directories.
func InstallMacOSCoreService(execPath string, args []string) error {
	if !IsRoot() {
		return fmt.Errorf("installing LaunchDaemon requires root privileges")
	}

	if strings.TrimSpace(execPath) == "" {
		var err error
		execPath, err = GetCurrentExecutable()
		if err != nil {
			return fmt.Errorf("failed to determine executable for core service: %w", err)
		}
	}
	if len(args) == 0 {
		args = []string{"core"}
	}

	launchDaemonsDir := "/Library/LaunchDaemons"
	dataDir := CoreDataDir()
	logDir := CoreLogDir()
	plistPath := macOSCoreServicePlistPath()

	if err := os.MkdirAll(launchDaemonsDir, 0o755); err != nil {
		return fmt.Errorf("failed to create LaunchDaemons directory: %w", err)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("failed to create core log directory: %w", err)
	}

	// Generate plist content for LaunchDaemon
	plistContent, err := RenderLaunchdPlist(LaunchdPlistData{
		Label:             macOSCoreServiceLabel,
		ProgramPath:       execPath,
		Args:              args,
		RunAtLoad:         true,
		KeepAlive:         true,
		WorkingDirectory:  dataDir,
		StandardOutPath:   filepath.Join(logDir, "core.log"),
		StandardErrorPath: filepath.Join(logDir, "core.error.log"),
		EnvironmentVars: map[string]string{
			"ALIANG_DATA_DIR":    dataDir,
			"ALIANG_LOG_DIR":     logDir,
			"ALIANG_SOCKET_PATH": CoreSocketPath(),
		},
	})
	if err != nil {
		return fmt.Errorf("failed to render core service plist: %w", err)
	}

	if err := os.WriteFile(plistPath, []byte(plistContent), 0o644); err != nil {
		return fmt.Errorf("failed to write core service plist: %w", err)
	}

	// Bootout existing service first if any
	exec.Command("launchctl", "bootout", "system/"+macOSCoreServiceLabel).CombinedOutput()

	// Bootstrap as system LaunchDaemon
	if output, err := exec.Command("launchctl", "bootstrap", "system", plistPath).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap failed: %w, output: %s", err, strings.TrimSpace(string(output)))
	}

	return nil
}

// UninstallMacOSCoreService stops and removes the core service LaunchDaemon.
func UninstallMacOSCoreService() error {
	if !IsRoot() {
		return fmt.Errorf("uninstalling LaunchDaemon requires root privileges")
	}

	plistPath := macOSCoreServicePlistPath()
	if !FileExists(plistPath) {
		return nil
	}

	// Bootout the service
	output, err := exec.Command("launchctl", "bootout", "system/"+macOSCoreServiceLabel).CombinedOutput()
	if err != nil {
		lowerOutput := strings.ToLower(string(output))
		if !strings.Contains(lowerOutput, "could not find service") &&
			!strings.Contains(lowerOutput, "service not found") &&
			!strings.Contains(lowerOutput, "no such process") {
			return fmt.Errorf("launchctl bootout failed: %w, output: %s", err, strings.TrimSpace(string(output)))
		}
	}

	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove core service plist: %w", err)
	}

	return nil
}

// KickstartCoreService starts the core service via launchctl kickstart.
func KickstartCoreService() error {
	target := "system/" + macOSCoreServiceLabel
	if output, err := exec.Command("launchctl", "kickstart", "-p", target).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl kickstart failed: %w, output: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// StopCoreServiceViaLaunchctl stops the core service via launchctl kill SIGTERM.
func StopCoreServiceViaLaunchctl() error {
	target := "system/" + macOSCoreServiceLabel
	output, err := exec.Command("launchctl", "kill", "SIGTERM", target).CombinedOutput()
	if err != nil {
		lowerOutput := strings.ToLower(string(output))
		if strings.Contains(lowerOutput, "could not find service") ||
			strings.Contains(lowerOutput, "service not found") ||
			strings.Contains(lowerOutput, "unknown service") {
			return nil // already stopped
		}
		return fmt.Errorf("launchctl kill failed: %w, output: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// IsCoreServiceInstalled checks whether the core service plist exists.
func IsCoreServiceInstalled() bool {
	return FileExists(macOSCoreServicePlistPath())
}

// IsCoreServiceRunning checks whether the core service is currently running.
func IsCoreServiceRunning() bool {
	output, err := exec.Command("launchctl", "print", "system/"+macOSCoreServiceLabel).CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(output), "state = running")
}

func macOSCoreServicePlistPath() string {
	return "/Library/LaunchDaemons/" + macOSCoreServiceLabel + ".plist"
}

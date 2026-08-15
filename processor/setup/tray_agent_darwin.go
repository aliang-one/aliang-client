package setup

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const macOSTrayAgentLabel = "one.aliang.tray"

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
	targetUser, err := resolveMacOSTrayAgentUser()
	if err != nil {
		return nil, err
	}

	status := &MacOSTrayAgentStatus{
		Label:       macOSTrayAgentLabel,
		DisplayName: "Aliang Menu Bar Companion",
		PlistPath:   macOSTrayAgentPlistPath(targetUser),
		UserName:    targetUser.Username,
		UID:         targetUser.Uid,
		Status:      "unknown",
	}

	status.IsInstalled = FileExists(status.PlistPath)
	if !status.IsInstalled {
		status.Status = "not_installed"
		return status, nil
	}

	output, err := exec.Command("launchctl", "print", fmt.Sprintf("gui/%s/%s", targetUser.Uid, status.Label)).CombinedOutput()
	if err != nil {
		lowerOutput := strings.ToLower(string(output))
		if strings.Contains(lowerOutput, "could not find service") ||
			strings.Contains(lowerOutput, "service not found") ||
			strings.Contains(lowerOutput, "not loaded") ||
			strings.Contains(lowerOutput, "unknown service") {
			status.Status = "stopped"
			return status, nil
		}
		return status, fmt.Errorf("launchctl print failed: %w, output: %s", err, strings.TrimSpace(string(output)))
	}

	outputText := string(output)
	if strings.Contains(outputText, "state = running") {
		status.IsRunning = true
		status.Status = "running"
	} else {
		status.Status = "loaded"
	}

	pidPattern := regexp.MustCompile(`pid = (\d+)`)
	matches := pidPattern.FindStringSubmatch(outputText)
	if len(matches) == 2 {
		status.PID = parseInt(matches[1])
	}

	return status, nil
}

func InstallMacOSTrayAgent(execPath string) error {
	targetUser, err := resolveMacOSTrayAgentUser()
	if err != nil {
		return err
	}
	if strings.TrimSpace(execPath) == "" {
		execPath, err = GetCurrentExecutable()
		if err != nil {
			return fmt.Errorf("failed to determine executable for tray companion: %w", err)
		}
	}

	launchAgentsDir := filepath.Join(targetUser.HomeDir, "Library", "LaunchAgents")
	logDir := filepath.Join(targetUser.HomeDir, "Library", "Logs", "Aliang")
	plistPath := macOSTrayAgentPlistPath(targetUser)

	if err := os.MkdirAll(launchAgentsDir, 0o755); err != nil {
		return fmt.Errorf("failed to create LaunchAgents directory: %w", err)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("failed to create tray log directory: %w", err)
	}
	if err := chownPathToUserIfPossible(launchAgentsDir, targetUser); err != nil {
		return err
	}
	if err := chownPathToUserIfPossible(logDir, targetUser); err != nil {
		return err
	}

	plistContent, err := RenderLaunchdPlist(LaunchdPlistData{
		Label:             macOSTrayAgentLabel,
		ProgramPath:       execPath,
		Args:              []string{"tray-agent"},
		RunAtLoad:         true,
		KeepAlive:         true,
		WorkingDirectory:  filepath.Dir(execPath),
		StandardOutPath:   filepath.Join(logDir, "tray-agent.log"),
		StandardErrorPath: filepath.Join(logDir, "tray-agent.error.log"),
	})
	if err != nil {
		return fmt.Errorf("failed to render tray companion plist: %w", err)
	}

	if err := os.WriteFile(plistPath, []byte(plistContent), 0o644); err != nil {
		return fmt.Errorf("failed to write tray companion plist: %w", err)
	}
	if err := chownPathToUserIfPossible(plistPath, targetUser); err != nil {
		return err
	}

	domain := fmt.Sprintf("gui/%s", targetUser.Uid)
	_, _ = exec.Command("launchctl", "bootout", domain, plistPath).CombinedOutput()

	if output, err := exec.Command("launchctl", "bootstrap", domain, plistPath).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap failed: %w, output: %s", err, strings.TrimSpace(string(output)))
	}
	if output, err := exec.Command("launchctl", "kickstart", "-k", fmt.Sprintf("%s/%s", domain, macOSTrayAgentLabel)).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl kickstart failed: %w, output: %s", err, strings.TrimSpace(string(output)))
	}

	return nil
}

func UninstallMacOSTrayAgent() error {
	targetUser, err := resolveMacOSTrayAgentUser()
	if err != nil {
		return err
	}

	plistPath := macOSTrayAgentPlistPath(targetUser)
	if !FileExists(plistPath) {
		return nil
	}

	domain := fmt.Sprintf("gui/%s", targetUser.Uid)
	output, err := exec.Command("launchctl", "bootout", domain, plistPath).CombinedOutput()
	if err != nil {
		lowerOutput := strings.ToLower(string(output))
		if !strings.Contains(lowerOutput, "could not find service") &&
			!strings.Contains(lowerOutput, "service not found") &&
			!strings.Contains(lowerOutput, "no such process") {
			return fmt.Errorf("launchctl bootout failed: %w, output: %s", err, strings.TrimSpace(string(output)))
		}
	}

	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove tray companion plist: %w", err)
	}

	return nil
}

func resolveMacOSTrayAgentUser() (*user.User, error) {
	if sudoUser := strings.TrimSpace(os.Getenv("SUDO_USER")); sudoUser != "" && sudoUser != "root" {
		return user.Lookup(sudoUser)
	}

	output, err := exec.Command("stat", "-f", "%Su", "/dev/console").CombinedOutput()
	if err == nil {
		consoleUser := strings.TrimSpace(string(output))
		if consoleUser != "" && consoleUser != "root" {
			return user.Lookup(consoleUser)
		}
	}

	currentUser, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("failed to determine tray companion user: %w", err)
	}
	if currentUser.HomeDir == "" {
		return nil, fmt.Errorf("current user home directory is empty")
	}
	return currentUser, nil
}

func macOSTrayAgentPlistPath(targetUser *user.User) string {
	return filepath.Join(targetUser.HomeDir, "Library", "LaunchAgents", macOSTrayAgentLabel+".plist")
}

func chownPathToUserIfPossible(path string, targetUser *user.User) error {
	if os.Geteuid() != 0 {
		return nil
	}

	uid, err := strconv.Atoi(targetUser.Uid)
	if err != nil {
		return fmt.Errorf("failed to parse user uid %q: %w", targetUser.Uid, err)
	}
	gid, err := strconv.Atoi(targetUser.Gid)
	if err != nil {
		return fmt.Errorf("failed to parse user gid %q: %w", targetUser.Gid, err)
	}
	if err := os.Chown(path, uid, gid); err != nil {
		return fmt.Errorf("failed to adjust ownership for %s: %w", path, err)
	}
	return nil
}

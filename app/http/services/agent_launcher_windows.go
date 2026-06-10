//go:build windows

package services

import (
	"fmt"
	"os/exec"
)

func launchInExternalTerminal(path string, args []string, cwd string) error {
	script, err := createAgentLaunchScript(windowsCommandLine(path, args), cwd, ".bat")
	if err != nil {
		return err
	}
	if err := exec.Command("cmd", "/C", "start", "", "cmd", "/K", windowsQuote(script)).Start(); err != nil {
		return fmt.Errorf("failed to open Command Prompt: %w", err)
	}
	return nil
}

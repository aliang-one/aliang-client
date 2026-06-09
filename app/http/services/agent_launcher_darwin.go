//go:build darwin

package services

import (
	"fmt"
	"os/exec"
)

func launchInExternalTerminal(path string, args []string, cwd string) error {
	script, err := createAgentLaunchScript(unixCommandLine(path, args), cwd, ".command")
	if err != nil {
		return err
	}
	if err := exec.Command("open", script).Start(); err != nil {
		return fmt.Errorf("failed to open Terminal: %w", err)
	}
	return nil
}

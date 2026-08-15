//go:build windows

package services

import (
	"fmt"
	"strings"
)

func removeFileElevated(path string) error {
	pathWin := strings.ReplaceAll(path, "/", "\\")
	psArgs := fmt.Sprintf(
		"-NoProfile -ExecutionPolicy Bypass -Command \"Remove-Item -LiteralPath '%s' -Force -ErrorAction SilentlyContinue\"",
		escapePowerShellSingleQuoted(pathWin),
	)

	exitCode, err := runElevatedHidden("powershell.exe", psArgs)
	if err != nil {
		return fmt.Errorf("elevated file removal failed: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("elevated file removal exited with code %d", exitCode)
	}
	return nil
}

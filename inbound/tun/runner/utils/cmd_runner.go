//go:build unix

package utils

import (
	"bytes"
	"os/exec"
	"strings"
)

func RunCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)

	return cmd.Run()
}

func RunCommandElevated(name string, args ...string) error {
	return RunCommand(name, args...)
}

func GetRunCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)

	return cmd
}

// RunCommandAndTrim runs a command and trims the output.
func RunCommandAndTrim(name string, args ...string) (string, error) {
	var out bytes.Buffer
	cmd := GetRunCommand(name, args...)
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

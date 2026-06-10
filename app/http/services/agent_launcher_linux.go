//go:build linux

package services

import (
	"fmt"
	"os/exec"
)

func launchInExternalTerminal(path string, args []string, cwd string) error {
	script, err := createAgentLaunchScript(unixCommandLine(path, args), cwd, ".sh")
	if err != nil {
		return err
	}

	candidates := []struct {
		bin  string
		args []string
	}{
		{"x-terminal-emulator", []string{"-e", "sh", script}},
		{"gnome-terminal", []string{"--", "sh", script}},
		{"konsole", []string{"-e", "sh", script}},
		{"xfce4-terminal", []string{"-e", "sh " + script}},
		{"mate-terminal", []string{"-e", "sh " + script}},
		{"xterm", []string{"-e", "sh", script}},
		{"alacritty", []string{"-e", "sh", script}},
		{"kitty", []string{"sh", script}},
	}

	for _, candidate := range candidates {
		if _, err := exec.LookPath(candidate.bin); err != nil {
			continue
		}
		if err := exec.Command(candidate.bin, candidate.args...).Start(); err == nil {
			return nil
		}
	}

	return fmt.Errorf("no supported terminal emulator found")
}

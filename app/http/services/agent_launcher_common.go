package services

import (
	"os"
	"path/filepath"
	"strings"
)

func createAgentLaunchScript(commandLine string, cwd string, extension string) (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, "aliang-agent")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}

	file, err := os.CreateTemp(dir, "launch-*"+extension)
	if err != nil {
		return "", err
	}
	defer file.Close()

	if extension == ".bat" {
		if cwd != "" {
			_, _ = file.WriteString("cd /d " + windowsQuote(cwd) + "\r\n")
		}
		_, _ = file.WriteString(commandLine + "\r\n")
		_, _ = file.WriteString("echo.\r\n")
		_, _ = file.WriteString("echo Command exited. Press any key to close.\r\n")
		_, _ = file.WriteString("pause >nul\r\n")
	} else {
		_, _ = file.WriteString("#!/usr/bin/env sh\n")
		if cwd != "" {
			_, _ = file.WriteString("cd " + shellQuote(cwd) + "\n")
		}
		_, _ = file.WriteString(commandLine + "\n")
		_, _ = file.WriteString("printf '\\nCommand exited. Press Enter to close. '\n")
		_, _ = file.WriteString("read _\n")
	}

	if extension != ".bat" {
		_ = os.Chmod(file.Name(), 0o700)
	}
	return file.Name(), nil
}

func unixCommandLine(path string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, shellQuote(path))
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func windowsCommandLine(path string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, windowsQuote(path))
	for _, arg := range args {
		parts = append(parts, windowsQuote(arg))
	}
	return strings.Join(parts, " ")
}

func windowsQuote(value string) string {
	if value == "" {
		return `""`
	}
	escaped := strings.ReplaceAll(value, `"`, `\"`)
	return `"` + escaped + `"`
}

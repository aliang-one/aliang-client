package services

import (
	"path/filepath"
	"strings"

	"aliang.one/nursorgate/processor/setup"
)

func resolveServiceConfigPath(logicalPath string) (string, error) {
	return setup.ResolveDefaultConfigPathForMode(setup.DetectRuntimeMode(), logicalPath)
}

func resolveServiceConfigPathForMode(mode setup.RuntimeMode, logicalPath string) (string, error) {
	return setup.ResolveDefaultConfigPathForMode(mode, logicalPath)
}

func resolveServiceRuntimeConfigPath() string {
	return filepath.Clean(setup.RuntimeConfigPath())
}

func resolveServiceUserConfigPath() (string, error) {
	path, err := setup.UserConfigPath()
	if err != nil {
		return "", err
	}
	return filepath.Clean(path), nil
}

func isDaemonRuntime() bool {
	return strings.TrimSpace(string(setup.DetectRuntimeMode())) == string(setup.RuntimeModeDaemon)
}

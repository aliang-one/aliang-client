package setup

import (
	"aliang.one/nursorgate/internal/runtimepath"
)

type RuntimeMode = runtimepath.Mode

const (
	RuntimeModeInteractive RuntimeMode = runtimepath.ModeInteractive
	RuntimeModeDaemon      RuntimeMode = runtimepath.ModeDaemon
)

// DetectRuntimeMode infers whether the current process is running as a
// background daemon/service based on the managed runtime environment variables.
func DetectRuntimeMode() RuntimeMode {
	return runtimepath.DetectMode()
}

// RuntimeConfigPath returns the canonical config.json path under the runtime data directory.
func RuntimeConfigPath() string {
	return runtimepath.RuntimeConfigPath()
}

// RuntimeExecutablePath returns the canonical managed executable path for the current platform.
func RuntimeExecutablePath() string {
	return runtimepath.RuntimeExecutablePath()
}

// BinaryFilename returns the platform-native executable name for Aliang.
func BinaryFilename() string {
	return runtimepath.BinaryFilename()
}

// UserConfigPath returns the interactive per-user config.json path.
func UserConfigPath() (string, error) {
	return runtimepath.UserConfigPath()
}

// ResolveDefaultConfigPathForMode resolves logical default config locations
// such as ./config.json or ~/.aliang/config.json to the effective path for
// the current runtime mode.
func ResolveDefaultConfigPathForMode(mode RuntimeMode, logicalPath string) (string, error) {
	return runtimepath.ResolveDefaultConfigPathForMode(mode, logicalPath)
}

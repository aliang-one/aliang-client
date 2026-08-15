package ipc

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"aliang.one/nursorgate/processor/setup"
)

// Socket path based on platform
func defaultSocketPath() string {
	switch runtime.GOOS {
	case "darwin":
		return "/var/run/aliang-core.sock"
	case "linux":
		return "/run/aliang-core.sock"
	case "windows":
		return `\\.\pipe\aliang-core`
	default:
		return "/tmp/aliang-core.sock"
	}
}

// SocketPath returns the IPC socket path.
// Uses ALIANG_SOCKET_PATH env var if set, otherwise defaults.
func SocketPath() string {
	path := setup.CoreSocketPath()
	if runtime.GOOS == "windows" && !strings.HasPrefix(path, `\\.\pipe\`) {
		return defaultSocketPath()
	}
	return path
}

// CoreDataDir returns the system-level data directory.
// Uses ALIANG_DATA_DIR env var if set, otherwise defaults.
func CoreDataDir() string {
	return setup.CoreDataDir()
}

// CoreLogDir returns the system-level log directory.
func CoreLogDir() string {
	return setup.CoreLogDir()
}

// EnsureCoreDirs ensures all required directories exist.
func EnsureCoreDirs() error {
	if err := setup.EnsureCoreDirs(); err != nil {
		return fmt.Errorf("failed to ensure core runtime directories: %w", err)
	}

	// Windows named pipes live under \\.\pipe and do not require filesystem directories.
	if runtime.GOOS == "windows" {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(SocketPath()), 0755); err != nil {
		return fmt.Errorf("failed to create socket directory %s: %w", filepath.Dir(SocketPath()), err)
	}
	return nil
}

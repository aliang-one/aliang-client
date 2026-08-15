package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/internal/runtimepath"
)

// CoreDataDir returns the system-level data directory.
// macOS: /Library/Application Support/one.aliang.aliang/
// Linux: /var/lib/aliang/
// Windows: %ProgramData%\Aliang\
func CoreDataDir() string {
	return runtimepath.CoreDataDir()
}

// CoreLogDir returns the system-level log directory.
// macOS: /Library/Logs/Aliang/
// Linux: /var/log/aliang/
// Windows: %ProgramData%\Aliang\logs\
func CoreLogDir() string {
	return runtimepath.CoreLogDir()
}

// CoreSocketDir returns the directory containing the IPC socket.
func CoreSocketDir() string {
	switch runtime.GOOS {
	case "darwin":
		return "/var/run"
	case "linux":
		return "/run"
	case "windows":
		return ""
	default:
		return "/var/run"
	}
}

// CoreSocketPath returns the IPC socket path.
// macOS: /var/run/aliang-core.sock
// Linux: /run/aliang-core.sock
// Windows: \\.\pipe\aliang-core
func CoreSocketPath() string {
	return runtimepath.CoreSocketPath()
}

// EnsureCoreDataDir creates the system-level data directory if it doesn't exist.
func EnsureCoreDataDir() error {
	dirs := []string{
		CoreDataDir(),
		filepath.Join(CoreDataDir(), "certs"),
		filepath.Join(CoreDataDir(), "geoip"),
	}

	for _, dir := range dirs {
		mode := os.FileMode(0o755)
		if filepath.Base(dir) == "certs" {
			mode = 0o700
		}
		if err := os.MkdirAll(dir, mode); err != nil {
			return fmt.Errorf("failed to create data directory %s: %w", dir, err)
		}
		if err := os.Chmod(dir, mode); err != nil && runtime.GOOS != "windows" {
			return fmt.Errorf("failed to set data directory permissions %s: %w", dir, err)
		}
		logger.Debug(fmt.Sprintf("[Setup] Ensured data directory: %s", dir))
	}
	return nil
}

// EnsureCoreDirs ensures all required directories for the Core daemon exist.
func EnsureCoreDirs() error {
	// Create socket directory
	socketDir := CoreSocketDir()
	if socketDir != "" {
		if err := os.MkdirAll(socketDir, 0755); err != nil {
			return fmt.Errorf("failed to create socket directory %s: %w", socketDir, err)
		}
	}

	// Create data directory
	if err := EnsureCoreDataDir(); err != nil {
		return err
	}

	// Create log directory
	logDir := CoreLogDir()
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fmt.Errorf("failed to create log directory %s: %w", logDir, err)
	}

	logger.Debug("[Setup] All core directories ensured")
	return nil
}

// MigrateUserData migrates user data from old location to new system-level location.
// This is called during installation to migrate existing data.
func MigrateUserData() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	oldDataDir := filepath.Join(homeDir, ".aliang")
	newDataDir := CoreDataDir()

	// Check if old data exists
	if _, err := os.Stat(oldDataDir); os.IsNotExist(err) {
		logger.Debug("[Setup] No old data directory found, skipping migration")
		return nil
	}

	// Check if new data already exists
	if _, err := os.Stat(newDataDir); err == nil {
		logger.Debug(fmt.Sprintf("[Setup] New data directory already exists at %s, skipping migration", newDataDir))
		return nil
	}

	// Migrate data
	logger.Debug(fmt.Sprintf("[Setup] Migrating data from %s to %s", oldDataDir, newDataDir))

	// Create new directory
	if err := os.MkdirAll(newDataDir, 0755); err != nil {
		return fmt.Errorf("failed to create new data directory: %w", err)
	}

	// Copy all files and subdirectories
	if err := copyDir(oldDataDir, newDataDir); err != nil {
		return fmt.Errorf("failed to copy data: %w", err)
	}

	logger.Debug("[Setup] Data migration completed successfully")
	return nil
}

// copyDir copies a directory recursively.
func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := os.MkdirAll(dstPath, 0755); err != nil {
				return err
			}
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			data, err := os.ReadFile(srcPath)
			if err != nil {
				return err
			}
			if err := os.WriteFile(dstPath, data, 0644); err != nil {
				return err
			}
		}
	}
	return nil
}

package cache

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"aliang.one/nursorgate/internal/runtimepath"
)

const (
	// DefaultCacheDirName is the default name for the cache directory
	DefaultCacheDirName = ".aliang"

	// CacheDirEnvVar is the environment variable name for custom cache directory
	CacheDirEnvVar = "ALIANG_CACHE_DIR"

	// DefaultPermissions is the default permission mode for cache directory (777 = rwxrwxrwx)
	DefaultPermissions = 0o777

	// DefaultFilePermissions is the default permission mode for cache files (666 = rw-rw-rw-)
	DefaultFilePermissions = 0o666
)

var (
	// cacheDir stores the resolved cache directory path
	cacheDir     string
	cacheDirOnce sync.Once
)

// ResetCacheDirForTest clears the package cache-dir singleton so tests can isolate HOME/env changes.
func ResetCacheDirForTest() {
	cacheDir = ""
	cacheDirOnce = sync.Once{}
}

// GetCacheDir returns the cache directory path.
// It resolves paths in the following order:
// 1. ALIANG_CACHE_DIR environment variable (if set)
// 2. ~/.aliang (default)
//
// The directory is created with 0777 permissions if it doesn't exist.
// This ensures all users can read, write, and execute in the directory.
func GetCacheDir() (string, error) {
	var err error
	cacheDirOnce.Do(func() {
		cacheDir, err = resolveCacheDir()
		if err != nil {
			return
		}
		// Ensure the cache directory exists with correct permissions
		err = ensureCacheDirExists(cacheDir)
	})
	return cacheDir, err
}

// resolveCacheDir resolves the cache directory path from environment or defaults.
func resolveCacheDir() (string, error) {
	dir, err := runtimepath.ResolveStateDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve cache directory: %w", err)
	}
	return dir, nil
}

// ensureCacheDirExists creates the cache directory if it doesn't exist.
// Sets permissions to 0777 to allow all users to read, write, and execute.
func ensureCacheDirExists(dir string) error {
	// Check if directory already exists
	stat, err := os.Stat(dir)
	if err == nil {
		// Directory exists - verify it's actually a directory
		if !stat.IsDir() {
			return fmt.Errorf("cache path '%s' exists but is not a directory", dir)
		}

		// Directory exists - attempt to set permissions to 0777
		// This may not work on all filesystems or with all permission models
		if err := os.Chmod(dir, DefaultPermissions); err != nil {
			// Warn but don't fail if chmod doesn't work (e.g., on some network filesystems)
			// The directory might already have correct permissions or the filesystem might not support chmod
			fmt.Fprintf(os.Stderr, "Warning: failed to set cache directory permissions to 0777: %v\n", err)
		}
		return nil
	}

	// Directory doesn't exist - create it with all parent directories
	if err := os.MkdirAll(dir, DefaultPermissions); err != nil {
		return fmt.Errorf("failed to create cache directory '%s': %w", dir, err)
	}

	// Verify the directory was created and set permissions again
	// (in case the system defaults don't match our requested permissions)
	if err := os.Chmod(dir, DefaultPermissions); err != nil {
		// Log warning but don't fail
		fmt.Fprintf(os.Stderr, "Warning: failed to set cache directory permissions after creation: %v\n", err)
	}

	return nil
}

// GetCacheSubdir returns a subdirectory within the cache directory.
// Creates the subdirectory if it doesn't exist.
// All subdirectories are created with 0777 permissions.
func GetCacheSubdir(subdir string) (string, error) {
	baseDir, err := GetCacheDir()
	if err != nil {
		return "", err
	}

	fullPath := filepath.Join(baseDir, subdir)

	// Ensure subdirectory exists
	if err := os.MkdirAll(fullPath, DefaultPermissions); err != nil {
		return "", fmt.Errorf("failed to create cache subdirectory '%s': %w", fullPath, err)
	}

	// Set permissions to 0777
	if err := os.Chmod(fullPath, DefaultPermissions); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to set cache subdirectory permissions to 0777: %v\n", err)
	}

	return fullPath, nil
}

// GetCacheFile returns the full path to a file within the cache directory.
// Does not create the file or parent directories.
func GetCacheFile(filename string) (string, error) {
	cacheDir, err := GetCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, filename), nil
}

// SetCacheFilePermissions sets the permissions on a cache file to 0666 (rw-rw-rw-).
// This allows any user to read and write the file.
func SetCacheFilePermissions(filepath string) error {
	if err := os.Chmod(filepath, DefaultFilePermissions); err != nil {
		return fmt.Errorf("failed to set cache file permissions: %w", err)
	}
	return nil
}

// getHomeDir returns the user's home directory.
// Works across Windows, macOS, and Linux.
func getHomeDir() (string, error) {
	return runtimepath.UserHomeDir()
}

// expandHome expands ~ in paths to the user's home directory.
func expandHome(path string) (string, error) {
	return runtimepath.ExpandHome(path)
}

// ExpandHomePath expands ~ in paths to the user's home directory.
// This is a public wrapper for expandHome to be used by other packages.
//
// Examples:
//   - "~" -> "/Users/username"
//   - "~/.aliang" -> "/Users/username/.aliang"
//   - "~/data/file.db" -> "/Users/username/data/file.db"
//   - "/absolute/path" -> "/absolute/path" (no change)
func ExpandHomePath(path string) (string, error) {
	return expandHome(path)
}

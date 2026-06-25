package runtimepath

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Mode string

const (
	ModeInteractive Mode = "interactive"
	ModeDaemon      Mode = "daemon"

	UserStateDirName = ".aliang"
	CacheDirEnvVar   = "ALIANG_CACHE_DIR"
)

// DetectMode infers whether the current process is running as an interactive
// app/CLI or as a background daemon/service.
func DetectMode() Mode {
	if strings.TrimSpace(os.Getenv("ALIANG_DATA_DIR")) != "" {
		return ModeDaemon
	}
	if strings.TrimSpace(os.Getenv("ALIANG_SOCKET_PATH")) != "" {
		return ModeDaemon
	}
	return ModeInteractive
}

func BinaryFilename() string {
	if runtime.GOOS == "windows" {
		return "aliang.exe"
	}
	return "aliang"
}

func CoreDataDir() string {
	if dir := strings.TrimSpace(os.Getenv("ALIANG_DATA_DIR")); dir != "" {
		return filepath.Clean(dir)
	}
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/one.aliang.aliang"
	case "linux":
		return "/var/lib/aliang"
	case "windows":
		return os.ExpandEnv(`${ProgramData}\Aliang`)
	default:
		return "/var/lib/aliang"
	}
}

func CoreLogDir() string {
	if dir := strings.TrimSpace(os.Getenv("ALIANG_LOG_DIR")); dir != "" {
		return filepath.Clean(dir)
	}
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Logs/Aliang"
	case "linux":
		return "/var/log/aliang"
	case "windows":
		return os.ExpandEnv(`${ProgramData}\Aliang\logs`)
	default:
		return "/var/log/aliang"
	}
}

func CoreSocketPath() string {
	if path := strings.TrimSpace(os.Getenv("ALIANG_SOCKET_PATH")); path != "" {
		if runtime.GOOS == "windows" && !strings.HasPrefix(path, `\\.\pipe\`) {
			return `\\.\pipe\aliang-core`
		}
		return path
	}
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

func UserHomeDir() (string, error) {
	switch runtime.GOOS {
	case "windows":
		if home := strings.TrimSpace(os.Getenv("USERPROFILE")); home != "" {
			return home, nil
		}
	default:
		if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
			return home, nil
		}
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	homeDir = strings.TrimSpace(homeDir)
	if homeDir == "" {
		return "", errors.New("user home directory is empty")
	}
	return homeDir, nil
}

func UserStateDir() (string, error) {
	homeDir, err := UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, UserStateDirName), nil
}

func UserConfigPath() (string, error) {
	stateDir, err := UserStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(stateDir, "config.json"), nil
}

func RuntimeConfigPath() string {
	return filepath.Join(CoreDataDir(), "config.json")
}

func RuntimeExecutablePath() string {
	return filepath.Join(CoreDataDir(), BinaryFilename())
}

func ExpandHome(path string) (string, error) {
	if path == "" || path[0] != '~' {
		return path, nil
	}

	homeDir, err := UserHomeDir()
	if err != nil {
		return "", err
	}

	if len(path) == 1 {
		return homeDir, nil
	}
	return filepath.Join(homeDir, path[1:]), nil
}

// ResolveStateDir returns the canonical directory for local mutable state such
// as sqlite files, logs, generated certificates, and GeoIP databases.
func ResolveStateDir() (string, error) {
	if envDir := strings.TrimSpace(os.Getenv(CacheDirEnvVar)); envDir != "" {
		expandedDir, err := ExpandHome(envDir)
		if err != nil {
			return "", err
		}
		return filepath.Clean(expandedDir), nil
	}

	if DetectMode() == ModeDaemon {
		return filepath.Clean(CoreDataDir()), nil
	}

	userStateDir, err := UserStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Clean(userStateDir), nil
}

func ResolveDefaultConfigPathForMode(mode Mode, logicalPath string) (string, error) {
	logicalPath = strings.TrimSpace(logicalPath)
	if logicalPath == "" {
		return "", errors.New("config path is empty")
	}

	if mode == ModeDaemon {
		return filepath.Join(CoreDataDir(), filepath.Base(logicalPath)), nil
	}

	if strings.HasPrefix(logicalPath, "~") {
		path, err := UserConfigPath()
		if err != nil {
			return "", err
		}
		return filepath.Clean(path), nil
	}

	return filepath.Clean(logicalPath), nil
}

// effectiveHome resolves the desktop user's home when running as root, with a
// short TTL so fast-user-switching is eventually picked up without re-reading
// /etc/passwd on every call.
var (
	effectiveHomeMu      sync.Mutex
	effectiveHomeCached  string
	effectiveHomeChecked time.Time
)

const effectiveHomeCacheTTL = 5 * time.Second

// EffectiveAgentHome returns the home directory whose ~/.claude and ~/.codex the
// Claude Code / Codex agent detection should scan.
//
// For a normal (non-root) process this is identical to UserHomeDir(). When the
// process runs as root — e.g. the Linux systemd Core daemon, where $HOME is
// /root and useless for detecting the desktop user's agent history — it resolves
// the active desktop user's home instead (via systemd-logind's /run/user on
// Linux, or by scanning /Users on macOS). It falls back to UserHomeDir() when no
// desktop user can be determined.
func EffectiveAgentHome() (string, error) {
	// os.Getuid() returns -1 on Windows, so this never triggers there.
	if os.Getuid() != 0 {
		return UserHomeDir()
	}

	effectiveHomeMu.Lock()
	if effectiveHomeCached != "" && time.Since(effectiveHomeChecked) < effectiveHomeCacheTTL {
		cached := effectiveHomeCached
		effectiveHomeMu.Unlock()
		return cached, nil
	}
	effectiveHomeMu.Unlock()

	if home, ok := resolveDesktopUserHome(); ok {
		home = strings.TrimSpace(home)
		if home != "" {
			effectiveHomeMu.Lock()
			effectiveHomeCached = home
			effectiveHomeChecked = time.Now()
			effectiveHomeMu.Unlock()
			return home, nil
		}
	}

	// No desktop user resolvable — fall back to the process home (/root).
	return UserHomeDir()
}

// resolveDesktopUserHome locates the interactive desktop user's home when the
// current process is root. Returns ok=false when it cannot be determined.
func resolveDesktopUserHome() (string, bool) {
	switch runtime.GOOS {
	case "linux":
		// systemd-logind creates /run/user/<uid> per active login session;
		// the most recently touched one is the active desktop user.
		if home, ok := newestSessionHome("/run/user", true); ok {
			return home, true
		}
		return newestSessionHome("/home", false)
	case "darwin":
		if home, ok := newestSessionHome("/Users", false); ok {
			return home, true
		}
		return "", false
	default:
		return "", false
	}
}

// newestSessionHome scans a directory of entries (session dirs keyed by numeric
// uid when uidKeyed, otherwise by username) and returns the home of the most
// recently modified entry that maps to a real user.
func newestSessionHome(dir string, uidKeyed bool) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	var bestHome string
	var bestMtime time.Time
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		var home string
		if uidKeyed {
			if _, err := strconv.Atoi(name); err != nil {
				continue // not a uid
			}
			home = homeForUID(name)
		} else {
			if isSystemUser(name) {
				continue
			}
			home = homeForUser(name)
			if home == "" {
				home = filepath.Join(dir, name) // no passwd entry; use the path as-is
			}
		}
		if home == "" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(bestMtime) {
			bestMtime = info.ModTime()
			bestHome = home
		}
	}
	if bestHome == "" {
		return "", false
	}
	return bestHome, true
}

func isSystemUser(name string) bool {
	switch name {
	case "", "shared", "Guest", "guest", "root", "daemon", "bin", "sys", "nobody", "nobody4":
		return true
	}
	// macOS service users are prefixed with underscore (_spotlight, _windowserver, ...).
	return strings.HasPrefix(name, "_")
}

func homeForUID(uid string) string {
	u, err := user.LookupId(uid)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(u.HomeDir)
}

func homeForUser(name string) string {
	u, err := user.Lookup(name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(u.HomeDir)
}

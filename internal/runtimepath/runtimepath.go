package runtimepath

import (
	"errors"
	"os"
	"os/exec"
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
// /etc/passwd (or probing /dev/console) on every call. Negative results are
// cached too, so a headless host does not fork a probe per call.
var (
	effectiveHomeMu      sync.Mutex
	effectiveHomeCached  string
	effectiveHomeOK      bool
	effectiveHomeChecked time.Time
)

const effectiveHomeCacheTTL = 5 * time.Second

// EffectiveAgentHome returns the home directory whose ~/.claude and ~/.codex the
// Claude Code / Codex agent detection should scan.
//
// For a normal (non-root) process this is identical to UserHomeDir(). When the
// process runs as root — e.g. the Linux systemd Core daemon, where $HOME is
// /root and useless for detecting the desktop user's agent history — it resolves
// the *console* (physically logged-in) user's home: the owner of /dev/console
// on macOS, the active local seat session in systemd-logind on Linux.
//
// It never guesses a home from directory timestamps. On a multi-user machine
// that guess can point the root daemon at another account's private files, so
// an ambiguous situation resolves to ok=false and the caller falls back to the
// process home (/root).
func EffectiveAgentHome() (string, error) {
	// os.Getuid() returns -1 on Windows, so this never triggers there.
	if os.Getuid() != 0 {
		return UserHomeDir()
	}

	effectiveHomeMu.Lock()
	if !effectiveHomeChecked.IsZero() && time.Since(effectiveHomeChecked) < effectiveHomeCacheTTL {
		cached, ok := effectiveHomeCached, effectiveHomeOK
		effectiveHomeMu.Unlock()
		if ok {
			return cached, nil
		}
		return UserHomeDir()
	}
	effectiveHomeMu.Unlock()

	home, ok := resolveDesktopUserHome()
	if ok {
		home = strings.TrimSpace(home)
		ok = home != ""
	}

	effectiveHomeMu.Lock()
	effectiveHomeCached = home
	effectiveHomeOK = ok
	effectiveHomeChecked = time.Now()
	effectiveHomeMu.Unlock()

	if ok {
		return home, nil
	}

	// No desktop user resolvable — fall back to the process home (/root).
	return UserHomeDir()
}

// resolveDesktopUserHome locates the interactive console user's home when the
// current process is root. Returns ok=false when it cannot be determined
// unambiguously — never a best guess.
func resolveDesktopUserHome() (string, bool) {
	switch runtime.GOOS {
	case "linux":
		// systemd-logind publishes one KEY=VALUE record per session under
		// /run/systemd/sessions; the physical console is the ACTIVE local
		// session on a seat. This deliberately excludes SSH logins and
		// background sessions, which an mtime scan cannot tell apart.
		if home, ok := systemdConsoleUserHome(); ok {
			return home, true
		}
		// Older/non-systemd hosts: accept only an unambiguous single account.
		if home, ok := soleDesktopUserHome("/run/user", true); ok {
			return home, true
		}
		return soleDesktopUserHome("/home", false)
	case "darwin":
		return darwinConsoleUserHome()
	default:
		return "", false
	}
}

// darwinConsoleUserHome resolves the owner of /dev/console — the user actually
// logged in at the screen. At the login window (and on headless machines) the
// console belongs to root, which is rejected. We never fall back to picking the
// newest home under /Users: that is exactly how a root daemon ends up reading a
// different local account's private files.
func darwinConsoleUserHome() (string, bool) {
	out, err := exec.Command("/usr/bin/stat", "-f", "%u", "/dev/console").Output()
	if err != nil {
		return "", false
	}
	uid := strings.TrimSpace(string(out))
	if uid == "" || uid == "0" {
		return "", false
	}
	home := homeForUID(uid)
	if !isPlausibleUserHome(home) {
		return "", false
	}
	return home, true
}

// systemdConsoleUserHome reads logind's session records and returns the home of
// the user holding the active local seat session. It refuses to answer when
// more than one candidate remains, so it can never silently land on a second
// account.
func systemdConsoleUserHome() (string, bool) {
	const sessionDir = "/run/systemd/sessions"
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return "", false
	}
	homes := make(map[string]struct{})
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(sessionDir, entry.Name()))
		if err != nil {
			continue
		}
		fields := parseSystemdSessionFields(string(raw))
		if fields["ACTIVE"] != "1" || fields["STATE"] != "active" {
			continue
		}
		if fields["REMOTE"] == "1" || strings.TrimSpace(fields["SEAT"]) == "" {
			continue
		}
		uid := strings.TrimSpace(fields["UID"])
		if uid == "" || uid == "0" {
			continue
		}
		home := homeForUID(uid)
		if !isPlausibleUserHome(home) {
			continue
		}
		homes[home] = struct{}{}
	}
	return soleHome(homes)
}

// parseSystemdSessionFields decodes the KEY=VALUE body of a logind session file.
func parseSystemdSessionFields(raw string) map[string]string {
	fields := make(map[string]string)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return fields
}

// soleHome returns the single home in the set, or ok=false when the set is
// empty or ambiguous. Ambiguity is deliberately treated as "unknown": with
// several candidate accounts there is no privacy-safe way to guess.
func soleHome(homes map[string]struct{}) (string, bool) {
	if len(homes) != 1 {
		return "", false
	}
	for home := range homes {
		return home, true
	}
	return "", false
}

// soleDesktopUserHome is the fallback for hosts without usable logind data. It
// reads a directory keyed by uid (/run/user) or username (/home) and resolves a
// home only when exactly one plausible desktop account is present — on a
// multi-user box it returns ok=false rather than picking one at random.
func soleDesktopUserHome(dir string, uidKeyed bool) (string, bool) {
	return soleDesktopUserHomeWithResolver(dir, func(name string) string {
		if uidKeyed {
			if _, err := strconv.Atoi(name); err != nil {
				return "" // not a uid
			}
			if name == "0" {
				return ""
			}
			return homeForUID(name)
		}
		if isSystemUser(name) {
			return ""
		}
		home := homeForUser(name)
		if home == "" {
			home = filepath.Join(dir, name) // no passwd entry; use the path as-is
		}
		return home
	})
}

func soleDesktopUserHomeWithResolver(dir string, resolve func(name string) string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	homes := make(map[string]struct{})
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		home := resolve(entry.Name())
		if !isPlausibleUserHome(home) {
			continue
		}
		homes[home] = struct{}{}
	}
	return soleHome(homes)
}

// isPlausibleUserHome rejects homes that belong to service/system accounts
// rather than a person who could have logged in at the console.
func isPlausibleUserHome(home string) bool {
	home = strings.TrimSpace(home)
	if home == "" {
		return false
	}
	home = filepath.Clean(home)
	switch home {
	case "/", "/root", "/nonexistent", "/var/root", "/var/empty":
		return false
	}
	for _, prefix := range []string{"/var/", "/run/", "/usr/", "/etc/", "/bin", "/sbin", "/lib", "/dev/", "/proc/", "/sys/"} {
		if home == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(home, prefix) {
			return false
		}
	}
	return true
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

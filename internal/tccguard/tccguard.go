// Package tccguard gates macOS privacy-protected folders behind an explicit,
// attended consent check.
//
// macOS raises "Files & Folders" (TCC) prompts inside the GUI session and blocks
// the requesting syscall until a human answers. Phone-driven requests can arrive
// while the user is away from the Mac (locked screen, asleep, or simply idle), so
// an unconditional stat/open of ~/Desktop, ~/Documents, ~/Downloads, iCloud Drive
// or an external volume leaves an unanswerable prompt and a stuck request.
//
// GuardPath therefore:
//
//   - never touches the disk for paths outside protected categories;
//   - never probes another local account's home directory — the app only ever
//     accesses files of the account it runs for (privacy requirement);
//   - refuses protected paths with an actionable consent error when the Mac is
//     unattended, instead of raising a prompt nobody can answer;
//   - when attended, probes the protected category ROOT once (one prompt per
//     category), persists the grant in a small JSON cache, and reuses it — later
//     access does not touch the protected folder again until the executable
//     changes (grants are stamped with the executable identity).
//
// The package is macOS-only; GuardPath is a no-op elsewhere. Test binaries are
// no-ops too (a unit test must never raise a real system prompt); tests exercise
// the logic through SetTestHooks.
package tccguard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aliang.one/nursorgate/internal/runtimepath"
)

// ErrConsentRequired is the sentinel matched by errors.Is on every consent
// rejection. Callers surface the message verbatim to the phone (the message is
// already written for a human); programmatic detection uses this sentinel.
var ErrConsentRequired = errors.New("tcc_consent_required")

// consentPrefix is the machine-readable semantic-code prefix, mirroring the
// existing "project_path_missing: " convention the phone already parses.
const consentPrefix = "tcc_consent_required: "

// ConsentRequiredError describes why a protected path was refused.
type ConsentRequiredError struct {
	Path     string
	Category string
	// Attended reports that the Mac looked attended and the category probe ran
	// but did not yield access (prompt timed out, was denied, or failed).
	Attended bool
	// Foreign reports a path under another local account's home directory.
	Foreign bool
	// ProbeErr carries the raw probe failure for diagnostics (not shown to the
	// phone; the message already explains the remedy).
	ProbeErr error
}

func (e *ConsentRequiredError) Error() string {
	if e == nil {
		return consentPrefix
	}
	path := filepath.ToSlash(e.Path)
	switch {
	case e.Foreign:
		return consentPrefix + fmt.Sprintf("%s 位于其他本地账户的家目录下。为保护隐私，本应用只访问当前登录账户的文件，不会申请其他账户的隐私目录权限。请改用当前账户可访问的路径。", path)
	case e.Attended:
		return consentPrefix + fmt.Sprintf("%s 位于 macOS 受保护的「%s」目录，需要你在 Mac 上确认一次文件访问授权。这次授权提示没有得到允许（超时未点「允许」或被拒绝）。请在手机上重试，并在 Mac 弹出的「文件与文件夹」提示中点「允许」；如果之前误点过拒绝，可到 系统设置 › 隐私与安全性 › 文件与文件夹 中手动勾选本应用。把项目放到桌面、文稿、下载之外（例如家目录下的开发目录）可以避免这类授权。", path, e.Category)
	default:
		return consentPrefix + fmt.Sprintf("%s 位于 macOS 受保护的「%s」目录，需要你在 Mac 上确认一次文件访问授权。当前 Mac 无人值守（屏幕锁定、已休眠或长时间无操作），为避免弹出无人应答的系统授权框，本次操作已取消。请回到电脑前解锁 Mac 后在手机上重试一次，并在弹出的「文件与文件夹」提示中点「允许」——授权会被记住，之后无需再次确认。把项目放到桌面、文稿、下载之外（例如家目录下的开发目录）可以完全避免这类授权。", path, e.Category)
	}
}

func (e *ConsentRequiredError) Unwrap() error { return ErrConsentRequired }

// Indirection points for tests (SetTestHooks) and platform split.
var (
	supportedFn = platformSupported
	attendedFn  = platformAttended
	probeFn     = defaultProbe
	homeFn      = runtimepath.EffectiveAgentHome
	stateDirFn  = runtimepath.ResolveStateDir
	probeWait   = 25 * time.Second
	hooksActive atomic.Bool
)

// Hooks overrides guard inputs for tests. Zero fields keep the current value.
type Hooks struct {
	// Supported forces the platform-supported answer.
	Supported *bool
	// Attended reports whether a human is at the Mac right now.
	Attended func() bool
	// Probe simulates reading a protected category root.
	Probe func(root string) error
	// Home overrides the effective (current account) home directory.
	Home func() (string, error)
	// StateDir overrides where tcc-grants.json lives.
	StateDir func() (string, error)
	// ProbeWait overrides how long a caller waits for the probe goroutine.
	ProbeWait time.Duration
}

// SetTestHooks installs test doubles and forces the guard on regardless of the
// platform/test-binary gates. The returned restore must run at test end
// (t.Cleanup); it also resets the grant and failure caches.
func SetTestHooks(h Hooks) (restore func()) {
	prevSupported, prevAttended, prevProbe := supportedFn, attendedFn, probeFn
	prevHome, prevState, prevWait := homeFn, stateDirFn, probeWait
	if h.Supported != nil {
		value := *h.Supported
		supportedFn = func() bool { return value }
	}
	if h.Attended != nil {
		attendedFn = h.Attended
	}
	if h.Probe != nil {
		probeFn = h.Probe
	}
	if h.Home != nil {
		homeFn = h.Home
	}
	if h.StateDir != nil {
		stateDirFn = h.StateDir
	}
	if h.ProbeWait > 0 {
		probeWait = h.ProbeWait
	}
	resetGrantCache()
	resetProbeFailures()
	hooksActive.Store(true)
	return func() {
		supportedFn, attendedFn, probeFn = prevSupported, prevAttended, prevProbe
		homeFn, stateDirFn, probeWait = prevHome, prevState, prevWait
		hooksActive.Store(false)
		resetGrantCache()
		resetProbeFailures()
	}
}

// guardEnabled reports whether GuardPath should enforce anything.
func guardEnabled() bool {
	if hooksActive.Load() {
		return true
	}
	if testing.Testing() {
		// Unit tests must never raise real TCC prompts or write the real state
		// dir: a test binary can run on the developer's own Mac, and probing
		// ~/Desktop would block behind a system dialog. Tests that need the
		// guard install SetTestHooks.
		return false
	}
	return supportedFn()
}

// GuardPath vets access to path before any caller touches the disk. It returns
// nil when access is allowed, or a *ConsentRequiredError (errors.Is
// ErrConsentRequired) when the path is privacy-protected and cannot be safely
// accessed right now. The check itself performs no disk I/O unless the path
// belongs to a protected category, the Mac is attended, and no grant is cached.
func GuardPath(path string) error {
	if !guardEnabled() {
		return nil
	}
	home, _ := homeFn()
	abs, err := normalizePath(path, home)
	if err != nil || abs == "" {
		return nil
	}
	category, outcome := classify(abs, home)
	switch outcome {
	case classUnprotected:
		return nil
	case classForeign:
		return &ConsentRequiredError{Path: abs, Category: category.label, Foreign: true}
	}
	if hasGrant(category.key) {
		return nil
	}
	if !attendedFn() {
		return &ConsentRequiredError{Path: abs, Category: category.label}
	}
	if probeRecentlyFailed(category.key) {
		return &ConsentRequiredError{Path: abs, Category: category.label, Attended: true}
	}
	if err := probeCategory(category.key, category.root); err != nil {
		consentErr := &ConsentRequiredError{Path: abs, Category: category.label, Attended: true}
		if !errors.Is(err, errProbeTimeout) {
			consentErr.ProbeErr = err
		}
		return consentErr
	}
	return nil
}

// ---------------------------------------------------------------------------
// Path normalization and classification
// ---------------------------------------------------------------------------

// normalizePath makes path absolute and lexically clean. "~" and "~/x" are
// expanded against the effective home; an unresolved home leaves the path
// unexpanded (downstream resolvers then fail with their own not-exist error).
func normalizePath(raw string, home string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return "", nil
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		if strings.TrimSpace(home) == "" {
			return "", nil
		}
		path = filepath.Join(home, path[1:])
	}
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		path = abs
	}
	return filepath.Clean(path), nil
}

type pathClass int

const (
	classUnprotected pathClass = iota
	classProtected
	classForeign
)

type category struct {
	// key identifies the grant: one prompt per category per account/volume.
	key   string
	root  string
	label string
}

// homeProtectedSubdirs are the TCC "Files & Folders" categories inside a home
// directory. The on-disk names stay English on localized macOS.
var homeProtectedSubdirs = []struct {
	key   string
	rel   string
	label string
}{
	{"desktop", "Desktop", "桌面"},
	{"documents", "Documents", "文稿"},
	{"downloads", "Downloads", "下载"},
	{"trash", ".Trash", "废纸篓"},
	{"icloud", filepath.Join("Library", "Mobile Documents"), "iCloud 云盘"},
	{"cloudstorage", filepath.Join("Library", "CloudStorage"), "云端存储"},
}

// classify maps an absolute path to its protected category. Comparison is
// case-insensitive because macOS volumes are case-insensitive by default.
func classify(abs string, home string) (category, pathClass) {
	cleanHome := ""
	if strings.TrimSpace(home) != "" {
		if normalized, err := normalizePath(home, ""); err == nil {
			cleanHome = normalized
		}
	}
	if cleanHome != "" {
		homeKey := strings.ToLower(cleanHome)
		for _, sub := range homeProtectedSubdirs {
			root := filepath.Join(cleanHome, sub.rel)
			if pathWithin(abs, root) {
				return category{key: sub.key + "@" + homeKey, root: root, label: sub.label}, classProtected
			}
		}
	}
	if home := foreignHomeOf(abs, cleanHome); home != "" {
		return category{key: "foreign@" + strings.ToLower(home), label: "其他本地账户的家目录"}, classForeign
	}
	if name, ok := volumeOf(abs); ok {
		return category{key: "volume:" + strings.ToLower(name), root: filepath.Join("/Volumes", name), label: "外置磁盘「" + name + "」"}, classProtected
	}
	return category{}, classUnprotected
}

// foreignHomeOf reports the other account's home prefix when abs belongs to a
// different local account. It only judges when our own home is exactly
// /Users/<name> or /home/<name> — an unresolvable home (root fallback) must not
// turn the regular user's own folders into "foreign".
func foreignHomeOf(abs, ownHome string) string {
	ownPrefix, ownName, ok := userHomeUnder(ownHome)
	if !ok {
		return ""
	}
	prefix, name, ok := userHomeOf(abs)
	if !ok {
		return ""
	}
	if strings.EqualFold(prefix, ownPrefix) && strings.EqualFold(name, ownName) {
		return ""
	}
	if strings.EqualFold(name, "Shared") {
		// /Users/Shared is the shared scratch area, not anyone's private home.
		return ""
	}
	return filepath.Join(prefix, name)
}

// userHomeUnder returns the /Users/<name> or /home/<name> prefix when path is
// exactly one segment below it.
func userHomeUnder(path string) (string, string, bool) {
	clean := filepath.Clean(path)
	for _, prefix := range []string{"/Users", "/home"} {
		if pathEqualFold(clean, prefix) || !pathWithin(clean, prefix) {
			continue
		}
		rel, err := filepath.Rel(prefix, clean)
		if err != nil {
			continue
		}
		parts := strings.Split(rel, string(os.PathSeparator))
		if len(parts) != 1 || parts[0] == "" || parts[0] == "." || parts[0] == ".." {
			continue
		}
		return prefix, parts[0], true
	}
	return "", "", false
}

// userHomeOf returns the /Users/<name> or /home/<name> prefix of any path below
// a local user's home.
func userHomeOf(path string) (string, string, bool) {
	clean := filepath.Clean(path)
	for _, prefix := range []string{"/Users", "/home"} {
		if pathEqualFold(clean, prefix) || !pathWithin(clean, prefix) {
			continue
		}
		rel, err := filepath.Rel(prefix, clean)
		if err != nil {
			continue
		}
		parts := strings.Split(rel, string(os.PathSeparator))
		if len(parts) == 0 || parts[0] == "" || parts[0] == "." || parts[0] == ".." {
			continue
		}
		return prefix, parts[0], true
	}
	return "", "", false
}

// volumeOf returns the mount name for /Volumes/<name>/... paths.
func volumeOf(abs string) (string, bool) {
	const prefix = "/Volumes"
	if pathEqualFold(abs, prefix) || !pathWithin(abs, prefix) {
		return "", false
	}
	rel, err := filepath.Rel(prefix, abs)
	if err != nil {
		return "", false
	}
	parts := strings.Split(rel, string(os.PathSeparator))
	if len(parts) == 0 || parts[0] == "" || parts[0] == "." || parts[0] == ".." {
		return "", false
	}
	return parts[0], true
}

func pathWithin(path, root string) bool {
	if path == "" || root == "" {
		return false
	}
	cleanPath := filepath.Clean(path)
	cleanRoot := filepath.Clean(root)
	if pathEqualFold(cleanPath, cleanRoot) {
		return true
	}
	p := strings.ToLower(cleanPath)
	r := strings.TrimSuffix(strings.ToLower(cleanRoot), string(os.PathSeparator))
	if r == "" {
		return false
	}
	return strings.HasPrefix(p, r+string(os.PathSeparator))
}

func pathEqualFold(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// ---------------------------------------------------------------------------
// Consent probe: one detached, single-flight syscall per category
// ---------------------------------------------------------------------------

// errNothingToProbe reports that the category root does not exist, so there is
// no syscall that could trigger a prompt. Access is allowed but deliberately
// NOT granted: if the folder appears later it still needs one real probe.
var errNothingToProbe = errors.New("tccguard: protected category root is absent")

// errProbeTimeout reports that the probe syscall has not returned in time. The
// detached goroutine keeps waiting and records the grant whenever the human
// eventually answers the prompt.
var errProbeTimeout = errors.New("tccguard: consent probe timed out")

type probeFlight struct {
	done chan struct{}
	err  error
}

var (
	flightsMu sync.Mutex
	flights   = map[string]*probeFlight{}
)

// probeCategory runs (or joins) the single in-flight probe for key and waits up
// to probeWait for it. The probe syscall itself runs detached so a caller
// timeout never leaves a goroutine leak on the caller's path — and so a grant
// answered late is still recorded.
func probeCategory(key, root string) error {
	flightsMu.Lock()
	flight, exists := flights[key]
	if !exists {
		flight = &probeFlight{done: make(chan struct{})}
		flights[key] = flight
		probe := probeFn // capture: tests may restore hooks while the probe runs
		flightsMu.Unlock()
		go func() {
			err := runProbe(probe, key, root)
			flight.err = err
			flightsMu.Lock()
			delete(flights, key)
			flightsMu.Unlock()
			close(flight.done)
		}()
	} else {
		flightsMu.Unlock()
	}
	wait := probeWait
	if wait <= 0 {
		wait = 25 * time.Second
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-flight.done:
		return flight.err
	case <-timer.C:
		return errProbeTimeout
	}
}

func runProbe(probe func(string) error, key, root string) error {
	err := probe(root)
	switch {
	case errors.Is(err, errNothingToProbe):
		return nil
	case err != nil:
		rememberProbeFailure(key)
		return err
	}
	clearProbeFailure(key)
	recordGrant(key, root)
	return nil
}

// defaultProbe reads the category root itself: opening and listing the
// top-level folder is exactly the syscall macOS gates (one prompt per
// category), and a successful read means files below it are accessible too.
func defaultProbe(root string) error {
	if _, err := os.Lstat(root); err != nil {
		if os.IsNotExist(err) {
			return errNothingToProbe
		}
		return err
	}
	file, err := os.Open(root)
	if err != nil {
		if os.IsNotExist(err) {
			return errNothingToProbe
		}
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}
	if _, err := file.Readdirnames(1); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// Negative probe cache: avoid re-raising a prompt storm after a failure
// ---------------------------------------------------------------------------

var (
	probeFailureMu  sync.Mutex
	probeFailures   = map[string]time.Time{}
	probeFailureTTL = 30 * time.Second
)

func probeRecentlyFailed(key string) bool {
	probeFailureMu.Lock()
	defer probeFailureMu.Unlock()
	at, ok := probeFailures[key]
	if !ok {
		return false
	}
	if time.Since(at) > probeFailureTTL {
		delete(probeFailures, key)
		return false
	}
	return true
}

func rememberProbeFailure(key string) {
	probeFailureMu.Lock()
	probeFailures[key] = time.Now()
	probeFailureMu.Unlock()
}

func clearProbeFailure(key string) {
	probeFailureMu.Lock()
	delete(probeFailures, key)
	probeFailureMu.Unlock()
}

func resetProbeFailures() {
	probeFailureMu.Lock()
	probeFailures = map[string]time.Time{}
	probeFailureMu.Unlock()
}

// ---------------------------------------------------------------------------
// Grant store: one JSON file, stamped with the executable identity
// ---------------------------------------------------------------------------

const (
	grantFileName    = "tcc-grants.json"
	grantFileVersion = 1
)

type grantEntry struct {
	GrantedAt string `json:"granted_at"`
	ProbePath string `json:"probe_path"`
}

type grantFile struct {
	Version int                   `json:"version"`
	Stamp   string                `json:"stamp"`
	Grants  map[string]grantEntry `json:"grants"`
}

var (
	grantMu     sync.Mutex
	grantLoaded bool
	grantStamp  string
	grantMap    map[string]grantEntry
)

// executableStamp identifies the current binary: an upgrade/reinstall replaces
// it (and unsigned builds get a new cdhash), which usually makes macOS ask for
// the folder again — so cached grants must not outlive the binary they were
// granted to.
func executableStamp() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	info, err := os.Stat(exe)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s|%d|%d", exe, info.ModTime().UnixNano(), info.Size())
}

func hasGrant(key string) bool {
	stamp := executableStamp()
	if stamp == "" {
		return false
	}
	grantMu.Lock()
	defer grantMu.Unlock()
	loadGrantsLocked(stamp)
	_, ok := grantMap[key]
	return ok
}

func recordGrant(key, probePath string) {
	stamp := executableStamp()
	if stamp == "" {
		return
	}
	grantMu.Lock()
	defer grantMu.Unlock()
	loadGrantsLocked(stamp)
	if grantMap == nil {
		grantMap = make(map[string]grantEntry)
	}
	grantMap[key] = grantEntry{
		GrantedAt: time.Now().UTC().Format(time.RFC3339),
		ProbePath: probePath,
	}
	persistGrantsLocked(stamp)
}

func loadGrantsLocked(stamp string) {
	if grantLoaded && grantStamp == stamp {
		return
	}
	grantLoaded = true
	grantStamp = stamp
	grantMap = nil
	stateDir, err := stateDirFn()
	if err != nil || strings.TrimSpace(stateDir) == "" {
		return
	}
	data, err := os.ReadFile(filepath.Join(stateDir, grantFileName))
	if err != nil {
		return
	}
	var payload grantFile
	if err := json.Unmarshal(data, &payload); err != nil {
		return
	}
	if payload.Version != grantFileVersion || payload.Stamp != stamp || len(payload.Grants) == 0 {
		return
	}
	grantMap = payload.Grants
}

func persistGrantsLocked(stamp string) {
	stateDir, err := stateDirFn()
	if err != nil || strings.TrimSpace(stateDir) == "" {
		return
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return
	}
	payload := grantFile{Version: grantFileVersion, Stamp: stamp, Grants: grantMap}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(stateDir, grantFileName+".*")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(tmpName, filepath.Join(stateDir, grantFileName))
}

func resetGrantCache() {
	grantMu.Lock()
	grantLoaded = false
	grantStamp = ""
	grantMap = nil
	grantMu.Unlock()
}

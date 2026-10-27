package tccguard

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func boolPtr(v bool) *bool { return &v }

type guardEnv struct {
	home     string
	stateDir string
	attended atomic.Bool
	probes   atomic.Int32
	probeMu  sync.Mutex
	roots    []string
	probe    func(root string) error
}

func newGuardEnv(t *testing.T, attended bool) *guardEnv {
	t.Helper()
	env := &guardEnv{home: t.TempDir(), stateDir: t.TempDir()}
	env.attended.Store(attended)
	restore := SetTestHooks(Hooks{
		Supported: boolPtr(true),
		Attended:  env.attended.Load,
		Probe: func(root string) error {
			env.probes.Add(1)
			env.probeMu.Lock()
			env.roots = append(env.roots, root)
			probe := env.probe
			env.probeMu.Unlock()
			if probe != nil {
				return probe(root)
			}
			return nil
		},
		Home:      func() (string, error) { return env.home, nil },
		StateDir:  func() (string, error) { return env.stateDir, nil },
		ProbeWait: time.Second,
	})
	t.Cleanup(restore)
	return env
}

func (e *guardEnv) setProbe(fn func(string) error) {
	e.probeMu.Lock()
	e.probe = fn
	e.probeMu.Unlock()
}

func (e *guardEnv) probeRoots() []string {
	e.probeMu.Lock()
	defer e.probeMu.Unlock()
	return append([]string(nil), e.roots...)
}

func assertConsent(t *testing.T, err error) *ConsentRequiredError {
	t.Helper()
	if err == nil {
		t.Fatal("expected a consent error, got nil")
	}
	if !errors.Is(err, ErrConsentRequired) {
		t.Fatalf("errors.Is(err, ErrConsentRequired) = false; err = %v", err)
	}
	if !strings.HasPrefix(err.Error(), consentPrefix) {
		t.Fatalf("message %q does not start with %q", err.Error(), consentPrefix)
	}
	var consentErr *ConsentRequiredError
	if !errors.As(err, &consentErr) {
		t.Fatalf("errors.As(*ConsentRequiredError) failed: %v", err)
	}
	return consentErr
}

func TestGuardPathAllowsUnprotectedWithoutProbe(t *testing.T) {
	env := newGuardEnv(t, false)
	for _, path := range []string{
		filepath.Join(env.home, "dev", "proj"),
		filepath.Join(t.TempDir(), "some", "file"),
	} {
		if err := GuardPath(path); err != nil {
			t.Fatalf("GuardPath(%q) = %v, want nil", path, err)
		}
	}
	if got := env.probes.Load(); got != 0 {
		t.Fatalf("unprotected paths triggered %d probes, want 0", got)
	}
}

func TestGuardPathUnattendedRefusesProtected(t *testing.T) {
	env := newGuardEnv(t, false)
	for _, rel := range []string{
		"Desktop",
		"Documents",
		"Downloads",
		".Trash",
		filepath.Join("Library", "Mobile Documents"),
		filepath.Join("Library", "CloudStorage"),
	} {
		path := filepath.Join(env.home, rel, "proj")
		consentErr := assertConsent(t, GuardPath(path))
		if consentErr.Foreign {
			t.Errorf("%s: Foreign = true, want false", rel)
		}
		if consentErr.Attended {
			t.Errorf("%s: Attended = true, want false (unattended refusal)", rel)
		}
		if !strings.Contains(consentErr.Error(), "无人值守") {
			t.Errorf("%s: message does not explain unattended state: %v", rel, consentErr)
		}
	}
	if got := env.probes.Load(); got != 0 {
		t.Fatalf("unattended guard probed %d times, want 0", got)
	}
}

func TestGuardPathAttendedProbesCategoryRootOnceAndRemembersGrant(t *testing.T) {
	env := newGuardEnv(t, true)
	project := filepath.Join(env.home, "Documents", "proj")
	if err := GuardPath(project); err != nil {
		t.Fatalf("attended GuardPath = %v, want nil", err)
	}
	if got := env.probes.Load(); got != 1 {
		t.Fatalf("probe count = %d, want 1", got)
	}
	if roots := env.probeRoots(); len(roots) != 1 || roots[0] != filepath.Join(env.home, "Documents") {
		t.Fatalf("probe roots = %v, want [%s]", roots, filepath.Join(env.home, "Documents"))
	}

	// The grant is per category: once granted, even an unattended request
	// passes without touching the protected folder again.
	env.attended.Store(false)
	if err := GuardPath(project); err != nil {
		t.Fatalf("post-grant GuardPath = %v, want nil", err)
	}
	if got := env.probes.Load(); got != 1 {
		t.Fatalf("post-grant probe count = %d, want 1", got)
	}

	// A different category still needs its own consent.
	assertConsent(t, GuardPath(filepath.Join(env.home, "Desktop", "proj")))
}

func TestGuardPathExpandsTilde(t *testing.T) {
	env := newGuardEnv(t, false)
	consentErr := assertConsent(t, GuardPath("~/Desktop/proj"))
	if consentErr.Path != filepath.Join(env.home, "Desktop", "proj") {
		t.Fatalf("consent path = %q, want expanded home path", consentErr.Path)
	}
}

func TestGuardPathForeignHomeRefused(t *testing.T) {
	env := newGuardEnv(t, true)
	env.home = "/Users/mac"

	consentErr := assertConsent(t, GuardPath("/Users/other/Documents/proj"))
	if !consentErr.Foreign {
		t.Fatal("Foreign = false, want true")
	}
	if !strings.Contains(consentErr.Error(), "其他本地账户") {
		t.Fatalf("message does not mention the other account: %v", consentErr)
	}
	if got := env.probes.Load(); got != 0 {
		t.Fatalf("foreign path probed %d times, want 0", got)
	}

	// Case-insensitive match still recognizes our own home as protected (an
	// ordinary consent request, not a foreign-account refusal).
	env.attended.Store(false)
	consentErr = assertConsent(t, GuardPath("/users/mac/Desktop/proj"))
	if consentErr.Foreign {
		t.Fatal("own home (case variant) treated as foreign")
	}
	if consentErr.Attended {
		t.Fatal("unattended own-home request reported Attended")
	}

	// /Users/Shared is the shared scratch area, not a private home.
	if err := GuardPath("/Users/Shared/build"); err != nil {
		t.Fatalf("/Users/Shared path refused: %v", err)
	}
}

func TestGuardPathForeignRequiresParsableOwnHome(t *testing.T) {
	env := newGuardEnv(t, true)
	env.home = t.TempDir() // not /Users/<name> or /home/<name>
	if err := GuardPath("/Users/other/project"); err != nil {
		t.Fatalf("path refused although own home is unparsable: %v", err)
	}
}

func TestGuardPathVolume(t *testing.T) {
	env := newGuardEnv(t, false)
	path := "/Volumes/Data/proj"
	consentErr := assertConsent(t, GuardPath(path))
	if !strings.Contains(consentErr.Category, "Data") {
		t.Fatalf("category %q does not name the volume", consentErr.Category)
	}

	env.attended.Store(true)
	if err := GuardPath(path); err != nil {
		t.Fatalf("attended volume path refused: %v", err)
	}
	if roots := env.probeRoots(); len(roots) != 1 || roots[0] != "/Volumes/Data" {
		t.Fatalf("probe roots = %v, want [/Volumes/Data]", roots)
	}
}

func TestGuardPathAbsentCategoryRootNotGranted(t *testing.T) {
	env := newGuardEnv(t, true)
	env.setProbe(func(string) error { return errNothingToProbe })
	project := filepath.Join(env.home, "Library", "CloudStorage", "proj")
	if err := GuardPath(project); err != nil {
		t.Fatalf("absent category root should not block access: %v", err)
	}
	if hasGrant("cloudstorage@" + strings.ToLower(env.home)) {
		t.Fatal("absent category root recorded a grant")
	}
	env.attended.Store(false)
	assertConsent(t, GuardPath(project))
}

func TestGuardPathProbeFailureNegativeCache(t *testing.T) {
	env := newGuardEnv(t, true)
	probeErr := errors.New("operation not permitted")
	env.setProbe(func(string) error { return probeErr })

	consentErr := assertConsent(t, GuardPath(filepath.Join(env.home, "Desktop", "proj")))
	if !consentErr.Attended {
		t.Fatal("Attended = false, want true for a probe failure while attended")
	}
	if !errors.Is(consentErr.ProbeErr, probeErr) {
		t.Fatalf("ProbeErr = %v, want %v", consentErr.ProbeErr, probeErr)
	}
	if got := env.probes.Load(); got != 1 {
		t.Fatalf("probe count = %d, want 1", got)
	}

	// Within the negative-cache TTL the prompt is not raised again.
	assertConsent(t, GuardPath(filepath.Join(env.home, "Desktop", "other")))
	if got := env.probes.Load(); got != 1 {
		t.Fatalf("probe count within failure TTL = %d, want 1", got)
	}

	oldTTL := probeFailureTTL
	probeFailureTTL = time.Millisecond
	defer func() { probeFailureTTL = oldTTL }()
	time.Sleep(5 * time.Millisecond)
	assertConsent(t, GuardPath(filepath.Join(env.home, "Desktop", "proj")))
	if got := env.probes.Load(); got != 2 {
		t.Fatalf("probe count after TTL = %d, want 2", got)
	}
}

func TestGuardPathProbeTimeoutRecordsLateGrant(t *testing.T) {
	home := t.TempDir()
	stateDir := t.TempDir()
	release := make(chan struct{})
	var probes atomic.Int32
	attended := true
	t.Cleanup(SetTestHooks(Hooks{
		Supported: boolPtr(true),
		Attended:  func() bool { return attended },
		Probe: func(string) error {
			probes.Add(1)
			<-release
			return nil
		},
		Home:      func() (string, error) { return home, nil },
		StateDir:  func() (string, error) { return stateDir, nil },
		ProbeWait: 50 * time.Millisecond,
	}))

	project := filepath.Join(home, "Desktop", "proj")
	consentErr := assertConsent(t, GuardPath(project))
	if !consentErr.Attended {
		t.Fatal("timeout refusal should report Attended")
	}
	if consentErr.ProbeErr != nil {
		t.Fatalf("timeout should not be surfaced as ProbeErr: %v", consentErr.ProbeErr)
	}

	// Answer the prompt late: the detached probe records the grant.
	close(release)
	key := "desktop@" + strings.ToLower(home)
	deadline := time.Now().Add(2 * time.Second)
	for !hasGrant(key) {
		if time.Now().After(deadline) {
			t.Fatal("late probe did not record a grant")
		}
		time.Sleep(5 * time.Millisecond)
	}
	attended = false
	if err := GuardPath(project); err != nil {
		t.Fatalf("post-grant GuardPath = %v, want nil", err)
	}
	if got := probes.Load(); got != 1 {
		t.Fatalf("probe count = %d, want 1 (grant must be reused)", got)
	}
}

func TestGrantCacheInvalidatedOnStampChange(t *testing.T) {
	env := newGuardEnv(t, true)
	project := filepath.Join(env.home, "Documents", "proj")
	if err := GuardPath(project); err != nil {
		t.Fatalf("GuardPath = %v", err)
	}
	key := "documents@" + strings.ToLower(env.home)
	if !hasGrant(key) {
		t.Fatal("grant missing after successful probe")
	}

	grantPath := filepath.Join(env.stateDir, grantFileName)
	info, err := os.Stat(grantPath)
	if err != nil {
		t.Fatalf("grant file missing: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("grant file mode = %o, want 600", mode)
	}
	data, err := os.ReadFile(grantPath)
	if err != nil {
		t.Fatal(err)
	}
	var payload grantFile
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("grant file is not valid JSON: %v", err)
	}
	if payload.Version != grantFileVersion || payload.Stamp != executableStamp() {
		t.Fatalf("grant file version/stamp = %d/%q, want %d/%q",
			payload.Version, payload.Stamp, grantFileVersion, executableStamp())
	}
	if _, ok := payload.Grants[key]; !ok {
		t.Fatalf("grant file does not contain %q: %+v", key, payload.Grants)
	}

	// A different binary (upgrade/reinstall) invalidates every grant.
	payload.Stamp = "old-binary|1|1"
	rewritten, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(grantPath, rewritten, 0o600); err != nil {
		t.Fatal(err)
	}
	resetGrantCache()
	if hasGrant(key) {
		t.Fatal("grant survived an executable stamp change")
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name      string
		home      string
		path      string
		wantClass pathClass
		wantKey   string
		wantRoot  string
	}{
		{"desktop", "/Users/mac", "/Users/mac/Desktop/a", classProtected, "desktop@/users/mac", "/Users/mac/Desktop"},
		{"desktop category root itself", "/Users/mac", "/Users/mac/Desktop", classProtected, "desktop@/users/mac", "/Users/mac/Desktop"},
		{"case-insensitive", "/Users/mac", "/Users/mac/desktop/a", classProtected, "desktop@/users/mac", "/Users/mac/Desktop"},
		{"documents", "/Users/mac", "/Users/mac/Documents/a", classProtected, "documents@/users/mac", "/Users/mac/Documents"},
		{"downloads", "/Users/mac", "/Users/mac/Downloads/a", classProtected, "downloads@/users/mac", "/Users/mac/Downloads"},
		{"trash", "/Users/mac", "/Users/mac/.Trash/old", classProtected, "trash@/users/mac", "/Users/mac/.Trash"},
		{"icloud", "/Users/mac", "/Users/mac/Library/Mobile Documents/com~apple~CloudDocs/x", classProtected, "icloud@/users/mac", "/Users/mac/Library/Mobile Documents"},
		{"cloud storage", "/Users/mac", "/Users/mac/Library/CloudStorage/Dropbox/x", classProtected, "cloudstorage@/users/mac", "/Users/mac/Library/CloudStorage"},
		{"prefix boundary", "/Users/mac", "/Users/mac/Desktop2/x", classUnprotected, "", ""},
		{"own dev dir", "/Users/mac", "/Users/mac/dev/x", classUnprotected, "", ""},
		{"users root", "/Users/mac", "/Users", classUnprotected, "", ""},
		{"foreign home", "/Users/mac", "/Users/other/Documents/x", classForeign, "foreign@/users/other", ""},
		{"foreign home nested deeper", "/Users/mac", "/Users/other/dev/x", classForeign, "foreign@/users/other", ""},
		{"shared exempt", "/Users/mac", "/Users/Shared/x", classUnprotected, "", ""},
		{"linux home", "/home/alice", "/home/alice/Downloads/x", classProtected, "downloads@/home/alice", "/home/alice/Downloads"},
		{"linux foreign", "/home/alice", "/home/bob/x", classForeign, "foreign@/home/bob", ""},
		{"volume", "/Users/mac", "/Volumes/Data/x", classProtected, "volume:data", "/Volumes/Data"},
		{"volumes root", "/Users/mac", "/Volumes", classUnprotected, "", ""},
		{"root fallback does not judge users", "/root", "/Users/other/x", classUnprotected, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			category, class := classify(tc.path, tc.home)
			if class != tc.wantClass {
				t.Fatalf("class = %v, want %v", class, tc.wantClass)
			}
			if category.key != tc.wantKey {
				t.Errorf("key = %q, want %q", category.key, tc.wantKey)
			}
			if category.root != tc.wantRoot {
				t.Errorf("root = %q, want %q", category.root, tc.wantRoot)
			}
		})
	}
}

func TestNormalizePath(t *testing.T) {
	home := "/Users/mac"
	cases := map[string]string{
		"":                  "",
		"~/Desktop/x":       "/Users/mac/Desktop/x",
		"~":                 "/Users/mac",
		"/Users/mac/../x":   "/Users/x",
		"  /Users/mac/a/  ": "/Users/mac/a",
	}
	for input, want := range cases {
		got, err := normalizePath(input, home)
		if err != nil {
			t.Fatalf("normalizePath(%q) error: %v", input, err)
		}
		if got != want {
			t.Errorf("normalizePath(%q) = %q, want %q", input, got, want)
		}
	}
	if got, err := normalizePath("~/Desktop", ""); err != nil || got != "" {
		t.Errorf("normalizePath with no home = %q, %v; want empty", got, err)
	}
}

// A test binary must never raise a real TCC prompt: without SetTestHooks the
// guard is inert even for protected-looking paths.
func TestGuardPathInertInTestBinaryWithoutHooks(t *testing.T) {
	if hooksActive.Load() {
		t.Fatal("hooks still active; previous test leaked state")
	}
	if err := GuardPath("/Users/nobody/Desktop"); err != nil {
		t.Fatalf("guard active without hooks in a test binary: %v", err)
	}
}

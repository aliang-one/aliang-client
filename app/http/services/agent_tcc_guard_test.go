package services

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aliang.one/nursorgate/internal/tccguard"
)

// phoneTCCEnv activates the macOS "Files & Folders" guard with deterministic
// hooks: a throwaway home (so Desktop/Documents/Downloads classification works
// on any machine), an atomic attendance switch, and a probe counter that stands
// in for the real syscall that would raise the system prompt.
//
// Hooks are process-global; these tests must not call t.Parallel.
type phoneTCCEnv struct {
	home     string
	stateDir string
	attended atomic.Bool
	probes   atomic.Int32
}

func newPhoneTCCEnv(t *testing.T, attended bool) *phoneTCCEnv {
	t.Helper()
	env := &phoneTCCEnv{home: t.TempDir(), stateDir: t.TempDir()}
	env.attended.Store(attended)
	t.Setenv("HOME", env.home)
	t.Setenv("USERPROFILE", env.home)
	restore := tccguard.SetTestHooks(tccguard.Hooks{
		Supported: phoneTCCBoolPtr(true),
		Attended:  env.attended.Load,
		Probe: func(string) error {
			env.probes.Add(1)
			return nil
		},
		Home:      func() (string, error) { return env.home, nil },
		StateDir:  func() (string, error) { return env.stateDir, nil },
		ProbeWait: time.Second,
	})
	t.Cleanup(restore)
	return env
}

func phoneTCCBoolPtr(v bool) *bool { return &v }

// phoneTCCResolvedPath mirrors what resolveAgentAIAttachments stores: the
// absolute path after symlink resolution (/var → /private/var on macOS).
func phoneTCCResolvedPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	return resolved
}

func assertPhoneTCCConsent(t *testing.T, err error, wantForeign bool) *tccguard.ConsentRequiredError {
	t.Helper()
	if err == nil {
		t.Fatal("expected a consent error, got nil")
	}
	if !errors.Is(err, tccguard.ErrConsentRequired) {
		t.Fatalf("errors.Is(err, ErrConsentRequired) = false; err = %v", err)
	}
	if !strings.HasPrefix(err.Error(), "tcc_consent_required: ") {
		t.Fatalf("message %q does not start with the semantic code", err.Error())
	}
	var consentErr *tccguard.ConsentRequiredError
	if !errors.As(err, &consentErr) {
		t.Fatalf("errors.As(*ConsentRequiredError) failed: %v", err)
	}
	if consentErr.Foreign != wantForeign {
		t.Fatalf("Foreign = %v, want %v (%v)", consentErr.Foreign, wantForeign, consentErr)
	}
	return consentErr
}

// The phone drives four path resolvers (AI session cwd, terminal cwd, project
// detail, project file content). All of them must refuse a privacy-protected
// folder while the Mac is unattended, and none may probe (probe == the syscall
// that raises the TCC prompt) in that state.
func TestPhoneDrivenPathResolversRefuseProtectedFolderUnattended(t *testing.T) {
	env := newPhoneTCCEnv(t, false)
	setAgentAuthorizedExecutionDirectoriesCache(nil)
	t.Cleanup(func() { setAgentAuthorizedExecutionDirectoriesCache(nil) })

	project := filepath.Join(env.home, "Documents", "proj")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	cases := map[string]func() (string, error){
		"resolveAgentAICWD": func() (string, error) {
			return resolveAgentAICWD(project)
		},
		"resolveAgentAuthorizedCWD": func() (string, error) {
			return resolveAgentAuthorizedCWD(project, "terminal cwd")
		},
		"resolveAgentProjectPath": func() (string, error) {
			return resolveAgentProjectPath(project)
		},
		"resolveAgentProjectContentPath": func() (string, error) {
			return resolveAgentProjectContentPath(project, "notes.txt")
		},
	}
	for name, resolve := range cases {
		_, err := resolve()
		consentErr := assertPhoneTCCConsent(t, err, false)
		if consentErr.Attended {
			t.Errorf("%s: Attended = true, want unattended refusal", name)
		}
		if !strings.Contains(consentErr.Error(), "无人值守") {
			t.Errorf("%s: message does not explain the unattended Mac: %v", name, consentErr)
		}
	}

	// Empty terminal cwd walks the authorized list; every candidate is guarded
	// on the same terms (the first protected+unattended candidate refuses).
	setAgentAuthorizedExecutionDirectoriesCache([]string{project})
	_, err := resolveAgentAuthorizedCWD("", "terminal cwd")
	assertPhoneTCCConsent(t, err, false)

	if got := env.probes.Load(); got != 0 {
		t.Fatalf("unattended resolvers probed %d times, want 0", got)
	}
}

// When a human is at the Mac the first request probes the category root once
// and records the grant; from then on the same folder is reachable even after
// the Mac goes unattended again (the user already answered the prompt).
func TestPhoneDrivenPathResolversProbeOnceThenPassUnattended(t *testing.T) {
	env := newPhoneTCCEnv(t, true)
	setAgentAuthorizedExecutionDirectoriesCache(nil)
	t.Cleanup(func() { setAgentAuthorizedExecutionDirectoriesCache(nil) })

	project := filepath.Join(env.home, "Documents", "proj")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := os.WriteFile(filepath.Join(project, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write attachment: %v", err)
	}

	if _, err := resolveAgentAICWD(project); err != nil {
		t.Fatalf("attended resolveAgentAICWD: %v", err)
	}
	if got := env.probes.Load(); got != 1 {
		t.Fatalf("probe count after first attended resolve = %d, want 1", got)
	}

	attachments, err := resolveAgentAIAttachments([]interface{}{
		map[string]interface{}{"path": "a.txt"},
	}, project)
	if err != nil {
		t.Fatalf("attended resolveAgentAIAttachments: %v", err)
	}
	if len(attachments) != 1 || attachments[0].Path != phoneTCCResolvedPath(t, filepath.Join(project, "a.txt")) {
		t.Fatalf("attachments = %+v, want the resolved a.txt", attachments)
	}
	if _, err := resolveAgentProjectPath(project); err != nil {
		t.Fatalf("attended resolveAgentProjectPath: %v", err)
	}
	if got := env.probes.Load(); got != 1 {
		t.Fatalf("probe count after grant = %d, want 1 (grant must be reused)", got)
	}

	// Grant survives the Mac going unattended: no prompt, no probe.
	env.attended.Store(false)
	if _, err := resolveAgentAICWD(project); err != nil {
		t.Fatalf("post-grant resolveAgentAICWD: %v", err)
	}
	if _, err := resolveAgentAIAttachments([]interface{}{
		map[string]interface{}{"path": "a.txt"},
	}, project); err != nil {
		t.Fatalf("post-grant resolveAgentAIAttachments: %v", err)
	}
	if _, err := resolveAgentProjectPath(project); err != nil {
		t.Fatalf("post-grant resolveAgentProjectPath: %v", err)
	}
	if got := env.probes.Load(); got != 1 {
		t.Fatalf("probe count after unattended retry = %d, want 1", got)
	}
}

// Attachment paths are vetted individually: an absolute path that leaves the
// (unprotected) project and lands in a protected folder is refused before any
// filesystem access, even though the project itself needs no consent.
func TestPhoneDrivenAttachmentOutsideProjectRefusedWithoutProbe(t *testing.T) {
	env := newPhoneTCCEnv(t, false)
	project := filepath.Join(env.home, "dev", "proj")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	desktopFile := filepath.Join(env.home, "Desktop", "pic.png")

	_, err := resolveAgentAIAttachments([]interface{}{
		map[string]interface{}{"path": desktopFile},
	}, project)
	consentErr := assertPhoneTCCConsent(t, err, false)
	if consentErr.Path != desktopFile {
		t.Fatalf("consent path = %q, want %q", consentErr.Path, desktopFile)
	}
	if got := env.probes.Load(); got != 0 {
		t.Fatalf("unattended attachment probe count = %d, want 0", got)
	}
}

// Another local account's home is refused outright — no prompt, no probe, no
// stat — which is the "never touch someone else's private folders" rule.
func TestPhoneDrivenAttachmentForeignHomeRefused(t *testing.T) {
	env := newPhoneTCCEnv(t, true)
	env.home = "/Users/phone-tcc-owner"
	project := t.TempDir()

	_, err := resolveAgentAIAttachments([]interface{}{
		map[string]interface{}{"path": "/Users/someone-else/Desktop/x.png"},
	}, project)
	consentErr := assertPhoneTCCConsent(t, err, true)
	if !strings.Contains(consentErr.Error(), "其他本地账户") {
		t.Fatalf("message does not mention the other account: %v", consentErr)
	}
	if got := env.probes.Load(); got != 0 {
		t.Fatalf("foreign path probe count = %d, want 0", got)
	}
}

// The synchronized-directory cache feeds background ticks (hello/keepalive).
// It must stay a pure string cache: no stat, no probe, no consent prompt — disk
// validation happens only when an explicit user action resolves a cwd.
func TestAuthorizedDirectoryCacheDefersConsentToExplicitResolve(t *testing.T) {
	env := newPhoneTCCEnv(t, false)
	setAgentAuthorizedExecutionDirectoriesCache(nil)
	t.Cleanup(func() { setAgentAuthorizedExecutionDirectoriesCache(nil) })

	missing := filepath.Join(env.home, "Documents", "not-on-disk")
	setAgentAuthorizedExecutionDirectoriesCache([]string{missing})
	if got := env.probes.Load(); got != 0 {
		t.Fatalf("cache refresh probed %d times, want 0 (background paths must not touch projects)", got)
	}

	// The cache kept the entry even though nothing exists on disk (a stat-based
	// cache would have dropped it), and the explicit resolve — not the cache
	// refresh — is where consent is requested.
	_, err := resolveAgentAuthorizedCWD("", "terminal cwd")
	consentErr := assertPhoneTCCConsent(t, err, false)
	if !strings.Contains(consentErr.Error(), "无人值守") {
		t.Fatalf("message does not explain the unattended Mac: %v", consentErr)
	}
	if got := env.probes.Load(); got != 0 {
		t.Fatalf("unattended explicit resolve probed %d times, want 0", got)
	}
}

// End-to-end phone surface: ai.session.create / ai.message refuse before any
// provider is spawned, and the phone sees the semantic tcc_consent_required
// error instead of a silent hang or a prompt on an unattended Mac.
func TestPhoneAIMessageRefusesProtectedProjectUnattended(t *testing.T) {
	env := newPhoneTCCEnv(t, false)
	setAgentAuthorizedExecutionDirectoriesCache(nil)
	t.Cleanup(func() { setAgentAuthorizedExecutionDirectoriesCache(nil) })

	project := filepath.Join(env.home, "Documents", "proj")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	manager := newAgentAIManager()
	defer manager.closeAll()

	var mu sync.Mutex
	events := make([]map[string]interface{}, 0)
	writeJSON := func(payload interface{}) error {
		event, ok := payload.(map[string]interface{})
		if !ok {
			t.Fatalf("payload type = %T, want map[string]interface{}", payload)
		}
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		return nil
	}

	manager.create(map[string]interface{}{
		"type":         "ai.session.create",
		"session_id":   "ai_tcc",
		"project_path": project,
		"mode":         "agent",
	}, writeJSON)
	manager.message(map[string]interface{}{
		"type":         "ai.message",
		"session_id":   "ai_tcc",
		"message_id":   "msg_tcc",
		"content":      "hello",
		"project_path": project,
	}, writeJSON)

	mu.Lock()
	defer mu.Unlock()
	var consentErrors int
	for _, event := range events {
		if event["type"] != "ai.error" {
			continue
		}
		text, _ := event["error"].(string)
		if strings.HasPrefix(text, "tcc_consent_required: ") {
			consentErrors++
		}
	}
	if consentErrors != 2 {
		t.Fatalf("consent error events = %d, want 2 (create + message); events = %+v", consentErrors, events)
	}
	if got := env.probes.Load(); got != 0 {
		t.Fatalf("phone surface probed %d times while unattended, want 0", got)
	}
}

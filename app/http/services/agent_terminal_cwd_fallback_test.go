package services

import (
	"os"
	"path/filepath"
	"testing"
)

// setAgentHomeDir points agentHome() at dir on every platform: runtimepath's
// UserHomeDir reads $HOME on unix but $USERPROFILE on Windows, so tests must
// set both (same convention as agent_boot_fallback_test.go).
func setAgentHomeDir(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

// withNoAuthorizedDirectories clears the authorized-directories cache to model a
// fresh device that has discovered no projects yet (the VM002 failure state).
func withNoAuthorizedDirectories(t *testing.T) {
	t.Helper()
	setAgentAuthorizedExecutionDirectoriesCache(nil)
	t.Cleanup(func() { setAgentAuthorizedExecutionDirectoriesCache(nil) })
}

// TestResolveAgentTerminalCWDFallsBackToHomeWithoutProjects pins the no-project
// fallback: a device with zero synchronized project directories has no guard
// boundary to enforce, so the terminal must still open — defaulting to the
// user's home directory for the empty/"~" cwd the phone sends.
func TestResolveAgentTerminalCWDFallsBackToHomeWithoutProjects(t *testing.T) {
	home := t.TempDir()
	setAgentHomeDir(t, home)
	withNoAuthorizedDirectories(t)

	want, cleanErr := cleanExistingAgentDirectory(home)
	if cleanErr != nil {
		t.Fatal(cleanErr)
	}

	// "" 与 "~"、"~/" 都落同一个家目录（同一 agentHome 解析源）。
	for _, raw := range []string{"", "~", "~/"} {
		resolved, err := resolveAgentTerminalCWD(raw)
		if err != nil {
			t.Fatalf("cwd %q with no authorized directories = %v, want home fallback", raw, err)
		}
		if resolved != want {
			t.Fatalf("cwd %q with no authorized directories = %q, want %q", raw, resolved, want)
		}
	}
}

// The whole ~ family must resolve against agentHome (not $HOME): on root/systemd
// deployments $HOME=/root while agentHome() resolves the desktop user's home.
func TestResolveAgentTerminalCWDTildeSubpathSameSourceAsTilde(t *testing.T) {
	home := t.TempDir()
	setAgentHomeDir(t, home)
	withNoAuthorizedDirectories(t)

	sub := filepath.Join(home, "Downloads")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}

	resolved, err := resolveAgentTerminalCWD("~/Downloads")
	if err != nil {
		t.Fatalf(`cwd "~/Downloads" with no authorized directories = %v`, err)
	}
	want, cleanErr := cleanExistingAgentDirectory(sub)
	if cleanErr != nil {
		t.Fatal(cleanErr)
	}
	if resolved != want {
		t.Fatalf(`cwd "~/Downloads" resolved to %q, want %q`, resolved, want)
	}

	if _, err := resolveAgentTerminalCWD("~/does-not-exist"); err == nil {
		t.Fatal(`cwd "~/does-not-exist" accepted without authorized directories`)
	}
}

// Without projects any existing directory may seed a terminal (the guard only
// ever confined the START directory — a shell can cd anywhere it can read), but
// the path must still exist and be a directory.
func TestResolveAgentTerminalCWDWithoutProjectsRequiresExistingDirectory(t *testing.T) {
	home := t.TempDir()
	setAgentHomeDir(t, home)
	withNoAuthorizedDirectories(t)

	existing := t.TempDir()
	resolved, err := resolveAgentTerminalCWD(existing)
	if err != nil {
		t.Fatalf("existing directory with no authorized directories = %v, want accepted", err)
	}
	want, cleanErr := cleanExistingAgentDirectory(existing)
	if cleanErr != nil {
		t.Fatal(cleanErr)
	}
	if resolved != want {
		t.Fatalf("existing directory resolved to %q, want %q", resolved, want)
	}

	missing := filepath.Join(home, "does-not-exist")
	if _, err := resolveAgentTerminalCWD(missing); err == nil {
		t.Fatalf("missing directory %q accepted without authorized directories", missing)
	}
}

// Devices WITH synchronized projects keep the guard intact: "~" stays rejected
// when the home directory is outside every authorized root.
func TestResolveAgentTerminalCWDKeepsGuardWithProjects(t *testing.T) {
	home := t.TempDir()
	setAgentHomeDir(t, home)
	root := t.TempDir()
	setAgentAuthorizedExecutionDirectoriesCache([]string{root})
	t.Cleanup(func() { setAgentAuthorizedExecutionDirectoriesCache(nil) })

	if _, err := resolveAgentTerminalCWD("~"); err == nil {
		t.Fatal("cwd ~ outside authorized roots should still be rejected when projects exist")
	}

	authorized := filepath.Join(root, "project")
	if err := os.Mkdir(authorized, 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveAgentTerminalCWD(authorized)
	if err != nil {
		t.Fatalf("authorized directory = %v", err)
	}
	want, cleanErr := cleanExistingAgentDirectory(authorized)
	if cleanErr != nil {
		t.Fatal(cleanErr)
	}
	if resolved != want {
		t.Fatalf("authorized directory resolved to %q, want %q", resolved, want)
	}
}

// A home directory that cannot be resolved (service context with no desktop
// user) must surface a clear error instead of an empty cwd. Both HOME and
// USERPROFILE must be cleared — Windows only reads the latter.
func TestResolveAgentTerminalCWDWithoutProjectsWithoutHomeErrors(t *testing.T) {
	setAgentHomeDir(t, "")
	withNoAuthorizedDirectories(t)

	for _, raw := range []string{"", "~", "~/x"} {
		if _, err := resolveAgentTerminalCWD(raw); err == nil {
			t.Fatalf("cwd %q with no authorized directories and no resolvable home should error", raw)
		}
	}
}

// ai.run shares the no-project fallback: on a zero-project device the AI session
// must start at the home directory instead of failing on every project_path
// (same VM002 incident, AI half). Empty and ~-family inputs resolve to agentHome.
func TestResolveAgentAICWDFallsBackToHomeWithoutProjects(t *testing.T) {
	home := t.TempDir()
	setAgentHomeDir(t, home)
	withNoAuthorizedDirectories(t)

	want, cleanErr := cleanExistingAgentDirectory(home)
	if cleanErr != nil {
		t.Fatal(cleanErr)
	}
	for _, raw := range []string{"", "~", "~/"} {
		resolved, err := resolveAgentAICWD(raw)
		if err != nil {
			t.Fatalf("project path %q with no authorized directories = %v, want home fallback", raw, err)
		}
		if resolved != want {
			t.Fatalf("project path %q with no authorized directories = %q, want %q", raw, resolved, want)
		}
	}
}

// ai.run deliberately does NOT keep the terminal's authorized-list guard on
// devices WITH projects: the discovery snapshot is a display cache, NOT an
// execution ACL (2026-10-12 RCA — a project outside the scanned list could
// never start a session from the phone). Any existing directory the user
// names is accepted; only existence is enforced.
func TestResolveAgentAICWDAcceptsOutsideListEvenWithProjects(t *testing.T) {
	home := t.TempDir()
	setAgentHomeDir(t, home)
	root := t.TempDir()
	setAgentAuthorizedExecutionDirectoriesCache([]string{root})
	t.Cleanup(func() { setAgentAuthorizedExecutionDirectoriesCache(nil) })

	if _, err := resolveAgentAICWD("~"); err != nil {
		t.Fatal("project path ~ outside authorized roots should still be rejected when projects exist")
	}

	authorized := filepath.Join(root, "project")
	if err := os.Mkdir(authorized, 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveAgentAICWD(authorized)
	if err != nil {
		t.Fatalf("authorized project path = %v", err)
	}
	want, cleanErr := cleanExistingAgentDirectory(authorized)
	if cleanErr != nil {
		t.Fatal(cleanErr)
	}
	if resolved != want {
		t.Fatalf("authorized project path resolved to %q, want %q", resolved, want)
	}
}

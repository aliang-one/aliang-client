package services

import (
	"os"
	"path/filepath"
	"testing"
)

// Operator policy: the discovery snapshot is a display cache, not an execution
// ACL (2026-10-12 RCA: a project whose only transcript fell out of the scan
// window could never start a session from the phone). resolveAgentAICWD must
// accept ANY existing directory the caller names — even one the discovery scan
// has never seen — and fall back to the user's home when no path is supplied.
// Existence + symlink resolution remain enforced.
func TestResolveAgentAICWDAcceptsExistingDirOutsideAuthorizedList(t *testing.T) {
	setAgentAuthorizedExecutionDirectoriesCache(nil)
	t.Cleanup(func() { setAgentAuthorizedExecutionDirectoriesCache(nil) })

	outside := t.TempDir()
	authorized := filepath.Join(outside, "discovered-project")
	if err := os.MkdirAll(authorized, 0o700); err != nil {
		t.Fatalf("mkdir authorized: %v", err)
	}
	setAgentAuthorizedExecutionDirectoriesCache([]string{authorized})

	project := filepath.Join(outside, "cold-project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatalf("mkdir cold project: %v", err)
	}

	got, err := resolveAgentAICWD(project)
	if err != nil {
		t.Fatalf("resolveAgentAICWD(%q) rejected an existing dir outside the discovery list: %v", project, err)
	}
	want, wantErr := cleanExistingAgentDirectory(project)
	if wantErr != nil {
		t.Fatalf("clean existing project: %v", wantErr)
	}
	if got != want {
		t.Fatalf("resolveAgentAICWD = %q, want %q", got, want)
	}
}

func TestResolveAgentAICWDEmptyPathFallsBackToHome(t *testing.T) {
	setAgentAuthorizedExecutionDirectoriesCache(nil)
	t.Cleanup(func() { setAgentAuthorizedExecutionDirectoriesCache(nil) })

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	got, err := resolveAgentAICWD("   ")
	if err != nil {
		t.Fatalf("empty path: %v", err)
	}
	want, wantErr := cleanExistingAgentDirectory(home)
	if wantErr != nil {
		t.Fatalf("clean home: %v", wantErr)
	}
	if got != want {
		t.Fatalf("empty path resolved to %q, want home %q", got, want)
	}
}

func TestResolveAgentAICWDStillRejectsMissingDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	if _, err := resolveAgentAICWD(missing); err == nil {
		t.Fatalf("missing dir %q accepted, want error", missing)
	}
}

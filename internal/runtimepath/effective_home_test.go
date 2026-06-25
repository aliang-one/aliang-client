package runtimepath

import (
	"os"
	"testing"
)

// EffectiveAgentHome must mirror UserHomeDir for non-root processes (the common
// interactive/CLI and macOS-tray case). The root → desktop-user resolution only
// triggers under uid 0, which this test process is not.
func TestEffectiveAgentHome_NonRoot(t *testing.T) {
	home, err := EffectiveAgentHome()
	if err != nil {
		t.Fatalf("EffectiveAgentHome returned error: %v", err)
	}
	expected, err := UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir returned error: %v", err)
	}
	if home != expected {
		t.Errorf("non-root EffectiveAgentHome = %q, want %q", home, expected)
	}
	if home == "" {
		t.Error("expected a non-empty home directory")
	}
}

func TestNewestSessionHome_MissingDir(t *testing.T) {
	if home, ok := newestSessionHome("/this/path/should/not/exist/aliang-test", true); ok || home != "" {
		t.Errorf("newestSessionHome on missing dir = (%q, %v), want (\"\", false)", home, ok)
	}
}

func TestIsSystemUser(t *testing.T) {
	system := []string{"", "root", "daemon", "bin", "nobody", "_spotlight", "Guest", "shared"}
	for _, name := range system {
		if !isSystemUser(name) {
			t.Errorf("isSystemUser(%q) = false, want true", name)
		}
	}
	real := []string{"alice", "bob", "developer"}
	for _, name := range real {
		if isSystemUser(name) {
			t.Errorf("isSystemUser(%q) = true, want false", name)
		}
	}
}

// On a real machine the active user's home must be discoverable by the resolver
// (root or not, the scan helpers should at least not panic and should agree with
// the OS home on a single-user box).
func TestResolveDesktopUserHome_DoesNotPanic(t *testing.T) {
	if _, err := os.Stat("/home"); err == nil {
		// Linux: scanning /home should be safe; we only assert no panic.
		_, _ = resolveDesktopUserHome()
	}
}

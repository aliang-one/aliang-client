package runtimepath

import (
	"os"
	"path/filepath"
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

func TestSoleDesktopUserHome_MissingDir(t *testing.T) {
	if home, ok := soleDesktopUserHome("/this/path/should/not/exist/aliang-test", true); ok || home != "" {
		t.Errorf("soleDesktopUserHome on missing dir = (%q, %v), want (\"\", false)", home, ok)
	}
}

// Privacy regression: with two candidate accounts the resolver must refuse to
// pick one. A root daemon that guesses can end up scanning another user's
// private folders.
func TestSoleDesktopUserHome_Ambiguous(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"alice", "bob"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	resolve := func(name string) string { return "/Users/" + name }
	if home, ok := soleDesktopUserHomeWithResolver(dir, resolve); ok || home != "" {
		t.Errorf("soleDesktopUserHome with two accounts = (%q, %v), want (\"\", false)", home, ok)
	}
}

func TestSoleDesktopUserHome_SingleCandidate(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "alice"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolve := func(name string) string { return "/Users/" + name }
	home, ok := soleDesktopUserHomeWithResolver(dir, resolve)
	if !ok {
		t.Fatal("soleDesktopUserHome with one account = ok false, want true")
	}
	if home != "/Users/alice" {
		t.Errorf("soleDesktopUserHome = %q, want %q", home, "/Users/alice")
	}
}

func TestIsPlausibleUserHome(t *testing.T) {
	plausible := []string{"/Users/alice", "/home/alice", "/Volumes/Data/alice"}
	for _, home := range plausible {
		if !isPlausibleUserHome(home) {
			t.Errorf("isPlausibleUserHome(%q) = false, want true", home)
		}
	}
	system := []string{"", "/", "/root", "/var/root", "/var/empty", "/nonexistent", "/var/lib/lightdm", "/run/user/501", "/usr/share/foo"}
	for _, home := range system {
		if isPlausibleUserHome(home) {
			t.Errorf("isPlausibleUserHome(%q) = true, want false", home)
		}
	}
}

func TestParseSystemdSessionFields(t *testing.T) {
	raw := "# This is private data. Do not parse.\nUID=501\nUSER=alice\nACTIVE=1\nSTATE=active\nREMOTE=0\nSEAT=seat0\n\nbadline\n"
	fields := parseSystemdSessionFields(raw)
	want := map[string]string{"UID": "501", "USER": "alice", "ACTIVE": "1", "STATE": "active", "REMOTE": "0", "SEAT": "seat0"}
	for key, value := range want {
		if fields[key] != value {
			t.Errorf("fields[%q] = %q, want %q", key, fields[key], value)
		}
	}
	if _, ok := fields["badline"]; ok {
		t.Error("malformed line should be ignored")
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

package services

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGoalWorkspaceFingerprintSkipsTransientFiles reproduces the prod failure
// (lstat …/foo.vue.tmp.<pid>.<hash>: no such file or directory) at the parse
// level: an actively-edited workspace sprouts atomic-write temps + editor swaps.
// The fingerprint MUST skip them (not collect → not lstat) so it neither errors
// on a vanishing temp nor changes when a temp appears/disappears.
func TestGoalWorkspaceFingerprintSkipsTransientFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.js"), []byte("console.log('hi')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Atomic-write temp (the exact pattern from prod) + vim swap + DS_Store.
	for _, rel := range []string{"app.js.tmp.933022.86adf9fecf7c", ".app.js.swp", ".DS_Store"} {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte("transient"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	fp, err := goalWorkspaceFingerprint(dir)
	if err != nil {
		t.Fatalf("fingerprint must tolerate transient files, got: %v", err)
	}
	if fp == "" {
		t.Fatal("empty fingerprint")
	}

	// A directory with ONLY the real file must hash identically — the transients
	// were excluded, not folded into the digest.
	onlyReal := t.TempDir()
	if err := os.WriteFile(filepath.Join(onlyReal, "app.js"), []byte("console.log('hi')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fpOnly, err := goalWorkspaceFingerprint(onlyReal)
	if err != nil {
		t.Fatal(err)
	}
	if fp != fpOnly {
		t.Fatalf("transient files leaked into fingerprint: with-transients=%s only-real=%s", fp, fpOnly)
	}
}

// TestShouldSkipGoalFingerprintFile pins the transient-file pattern so a future
// edit doesn't silently stop excluding the prod atomic-temp pattern.
func TestShouldSkipGoalFingerprintFile(t *testing.T) {
	skip := []string{
		"app.vue.tmp.933022.86adf9fecf7c", // prod atomic-write temp
		"foo.js.tmp",
		".app.js.swp", ".x.swo",
		".#app.js", "#app.js#", "app.js~", // editor lock/autosave/backup
		".DS_Store", "Thumbs.db",
	}
	keep := []string{"app.js", "App.vue", "tmp-config.js", "styles.css"} // legit, no .tmp
	for _, n := range skip {
		if !shouldSkipGoalFingerprintFile(n) {
			t.Errorf("expected %q to be skipped (transient)", n)
		}
	}
	for _, n := range keep {
		if shouldSkipGoalFingerprintFile(n) {
			t.Errorf("expected %q to be KEPT (not transient)", n)
		}
	}
}

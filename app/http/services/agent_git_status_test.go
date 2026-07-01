package services

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestClassifyGitStatusCode(t *testing.T) {
	cases := []struct{ code, want string }{
		{"??", "added"},   // untracked
		{" M", "modified"}, // unstaged modify
		{"M ", "modified"}, // staged modify
		{"MM", "modified"}, // staged + unstaged
		{"A ", "added"},    // staged new
		{"AM", "added"},    // staged new + further modify → added
		{" D", "deleted"},  // unstaged delete
		{"D ", "deleted"},  // staged delete
		{"RM", "modified"}, // rename
		{"CM", "modified"}, // copy
		{"", "modified"},   // too short → safe default
	}
	for _, c := range cases {
		if got := classifyGitStatusCode(c.code); got != c.want {
			t.Errorf("classifyGitStatusCode(%q) = %q, want %q", c.code, got, c.want)
		}
	}
}

func TestGitDirectoryStatus(t *testing.T) {
	dir := "/repo/src"
	// direct child changed
	if got := gitDirectoryStatus(dir, map[string]string{"/repo/src/a.ts": "modified"}); got != "modified" {
		t.Errorf("child file: got %q, want modified", got)
	}
	// deeply nested changed
	if got := gitDirectoryStatus(dir, map[string]string{"/repo/src/nested/b.ts": "modified"}); got != "modified" {
		t.Errorf("nested file: got %q, want modified", got)
	}
	// sibling path with overlapping name prefix must NOT match
	if got := gitDirectoryStatus(dir, map[string]string{"/repo/srcX/c.ts": "modified"}); got != "clean" {
		t.Errorf("sibling name prefix: got %q, want clean", got)
	}
	// outside the dir
	if got := gitDirectoryStatus(dir, map[string]string{"/repo/readme.md": "modified"}); got != "clean" {
		t.Errorf("outside: got %q, want clean", got)
	}
	// empty
	if got := gitDirectoryStatus(dir, map[string]string{}); got != "clean" {
		t.Errorf("empty: got %q, want clean", got)
	}
	// trailing slash tolerated
	if got := gitDirectoryStatus(dir+"/", map[string]string{"/repo/src/a.ts": "modified"}); got != "modified" {
		t.Errorf("trailing slash: got %q, want modified", got)
	}
}

func TestLoadGitStatusMap(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "clean.go"), []byte("package x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "clean.go")
	run("commit", "-m", "init")
	// now modify the committed file + add an untracked one
	if err := os.WriteFile(filepath.Join(dir, "clean.go"), []byte("package y\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.go"), []byte("package z\n"), 0644); err != nil {
		t.Fatal(err)
	}

	m := loadGitStatusMap(dir)
	if m[filepath.Join(dir, "clean.go")] != "modified" {
		t.Errorf("clean.go: got %q, want modified", m[filepath.Join(dir, "clean.go")])
	}
	if m[filepath.Join(dir, "new.go")] != "added" {
		t.Errorf("new.go: got %q, want added", m[filepath.Join(dir, "new.go")])
	}
	// non-git dir → empty map (graceful)
	if got := loadGitStatusMap(t.TempDir()); len(got) != 0 {
		t.Errorf("non-git dir: got %v, want empty", got)
	}
}

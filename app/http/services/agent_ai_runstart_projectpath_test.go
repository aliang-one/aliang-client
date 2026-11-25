package services

import (
	"os"
	"path/filepath"
	"testing"
)

// 2026-11-25 线上事故:ai.run.start 曾把载荷里的 project_path 裸赋值进
// session(与 create() 的 resolveAgentAICWD 校验纪律不一致),手机端目录解析
// 不出来时发出的字面 "~" 直达执行层,整局会话一碰 shell 就死。这里锁定
// runStart 的收值纪律:空值沿用既有;解析通过才覆盖;解析失败保留既有值。

func TestRunStartProjectPathKeepsExistingOnEmpty(t *testing.T) {
	old := filepath.Join(t.TempDir(), "proj")
	if got, updated := runStartProjectPath("   ", old, "s1"); updated || got != old {
		t.Fatalf("空 payload 应回沿用: got=%q updated=%v, want (%q,false)", got, updated, old)
	}
}

func TestRunStartProjectPathAcceptsTilde(t *testing.T) {
	setAgentAuthorizedExecutionDirectoriesCache(nil)
	t.Cleanup(func() { setAgentAuthorizedExecutionDirectoriesCache(nil) })

	home := t.TempDir()
	resolved, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	t.Setenv("HOME", resolved)
	t.Setenv("USERPROFILE", resolved)
	old := filepath.Join(t.TempDir(), "proj")

	got, updated := runStartProjectPath("~", old, "s1")
	if !updated {
		t.Fatalf("\"~\" 应解析为主目录并覆盖,却回沿用")
	}
	want, wantErr := cleanExistingAgentDirectory(resolved)
	if wantErr != nil {
		t.Fatalf("clean home: %v", wantErr)
	}
	if got != want {
		t.Fatalf("runStartProjectPath(\"~\") = %q, want %q", got, want)
	}
}

func TestRunStartProjectPathAcceptsValidDir(t *testing.T) {
	setAgentAuthorizedExecutionDirectoriesCache(nil)
	t.Cleanup(func() { setAgentAuthorizedExecutionDirectoriesCache(nil) })

	proj := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	old := filepath.Join(t.TempDir(), "old")

	got, updated := runStartProjectPath(proj, old, "s1")
	if !updated {
		t.Fatalf("有效目录应覆盖")
	}
	want, wantErr := cleanExistingAgentDirectory(proj)
	if wantErr != nil {
		t.Fatalf("clean proj: %v", wantErr)
	}
	if got != want {
		t.Fatalf("runStartProjectPath = %q, want %q", got, want)
	}
}

func TestRunStartProjectPathKeepsExistingOnInvalidDir(t *testing.T) {
	setAgentAuthorizedExecutionDirectoriesCache(nil)
	t.Cleanup(func() { setAgentAuthorizedExecutionDirectoriesCache(nil) })

	missing := filepath.Join(t.TempDir(), "nope")
	old := filepath.Join(t.TempDir(), "proj")

	if got, updated := runStartProjectPath(missing, old, "s1"); updated || got != old {
		t.Fatalf("无效目录应保留既有值: got=%q updated=%v, want (%q,false)", got, updated, old)
	}
}

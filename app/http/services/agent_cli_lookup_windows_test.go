package services

import (
	"os"
	"path/filepath"
	"testing"
)

// ---- isExecutableFileForOS:Windows 下"存在且非目录"即可执行 ----

func TestIsExecutableFileForOSWindowsAcceptsNonExec(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "claude.cmd")
	if err := os.WriteFile(plain, []byte("@echo off\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !isExecutableFileForOS(plain, "windows") {
		t.Errorf("Windows 下无执行位的普通文件应判定为可执行: %s", plain)
	}
	if isExecutableFileForOS(plain, "darwin") {
		t.Errorf("非 Windows 下无执行位文件仍应判为不可执行: %s", plain)
	}
	if isExecutableFileForOS(dir, "windows") {
		t.Errorf("Windows 下目录也不应判为可执行: %s", dir)
	}
}

// ---- userBinCandidatesForOS:Windows 补 %APPDATA%\npm 与 .exe/.cmd 变体 ----

func TestUserBinCandidatesForOSWindowsIncludesNpmAndExts(t *testing.T) {
	home := `C:\Users\u`
	got := userBinCandidatesForOS(home, "claude", "windows")
	want := []string{
		filepath.Join(home, "AppData", "Roaming", "npm", "claude.exe"),
		filepath.Join(home, "AppData", "Roaming", "npm", "claude.cmd"),
		filepath.Join(home, "AppData", "Roaming", "npm", "claude"),
		filepath.Join(home, ".local", "bin", "claude.exe"),
		filepath.Join(home, ".local", "bin", "claude.cmd"),
	}
	for _, w := range want {
		if !agentAIStringSliceContains(got, w) {
			t.Errorf("Windows 候选缺少 %q；got %v", w, got)
		}
	}
}

func TestUserBinCandidatesNonWindowsUnchanged(t *testing.T) {
	got := userBinCandidatesForOS("/home/u", "claude", "darwin")
	for _, c := range got {
		if filepath.Ext(c) == ".exe" || filepath.Ext(c) == ".cmd" {
			t.Errorf("非 Windows 不应追加扩展名变体，got %q", c)
		}
	}
	if !agentAIStringSliceContains(got, filepath.Join("/home/u", ".local", "bin", "claude")) {
		t.Errorf("基础候选丢失，got %v", got)
	}
}

// ---- 端到端:windows goos 下兜底扫描命中 npm 的 .cmd shim ----

func TestLookPathInHomesForOSFindsNpmCmdShimOnWindows(t *testing.T) {
	home := t.TempDir()
	npmDir := filepath.Join(home, "AppData", "Roaming", "npm")
	if err := os.MkdirAll(npmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(npmDir, "claude.cmd")
	// 0644:模拟 Windows 上无执行位的普通文件,Windows 判定应放行
	if err := os.WriteFile(shim, []byte("@node \"claude\" %*\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := lookPathInHomesForOS([]string{home}, "claude", "windows", "amd64")
	if err != nil {
		t.Fatalf("期望 Windows 下兜底命中 npm .cmd shim，错误: %v", err)
	}
	if got != shim {
		t.Errorf("找到 %q，期望 %q", got, shim)
	}
}

func TestLookPathInHomesForOSPrefersExeOverCmdOnWindows(t *testing.T) {
	home := t.TempDir()
	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(binDir, "claude.exe")
	cmd := filepath.Join(binDir, "claude.cmd")
	if err := os.WriteFile(exe, []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cmd, []byte("@echo off\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := lookPathInHomesForOS([]string{home}, "claude", "windows", "amd64")
	if err != nil {
		t.Fatalf("期望命中 Windows 候选，错误: %v", err)
	}
	if got != exe {
		t.Errorf("同目录 .exe 应优先于 .cmd，got %q 期望 %q", got, exe)
	}
}

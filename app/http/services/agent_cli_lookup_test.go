package services

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUserBinCandidates(t *testing.T) {
	got := userBinCandidates("/home/u", "claude")
	want := filepath.Join("/home/u", ".local", "bin", "claude")
	if !agentAIStringSliceContains(got, want) {
		t.Errorf("userBinCandidates 缺少 %q；got %v", want, got)
	}
}

func TestLookPathInHomesFindsLocalBin(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(binDir, "claude")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := lookPathInHomes([]string{dir}, "claude")
	if err != nil {
		t.Fatalf("期望在 %s/.local/bin 下兜底命中 claude，错误: %v", dir, err)
	}
	if got != cli {
		t.Errorf("找到 %q，期望 %q", got, cli)
	}
}

func TestLookPathInHomesFindsNvmNode(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, ".nvm", "versions", "node", "v20.0.0", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(binDir, "claude")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := lookPathInHomes([]string{dir}, "claude")
	if err != nil {
		t.Fatalf("期望在 nvm 目录兜底命中 claude，错误: %v", err)
	}
	if got != cli {
		t.Errorf("找到 %q，期望 %q", got, cli)
	}
}

func TestLookPathInHomesMissingReturnsError(t *testing.T) {
	if _, err := lookPathInHomes([]string{t.TempDir()}, "definitely-not-installed-cli"); err == nil {
		t.Fatal("期望找不到时返回错误（exec.ErrNotFound 语义）")
	}
}

func TestIsExecutableFileRejectsDirAndNonExec(t *testing.T) {
	dir := t.TempDir()
	// 目录本身不应被视为可执行文件
	if isExecutableFile(dir) {
		t.Errorf("目录不应被判定为可执行文件: %s", dir)
	}
	// 无执行位的普通文件
	plain := filepath.Join(dir, "noexec")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if isExecutableFile(plain) {
		t.Errorf("无执行位的文件不应被判定为可执行文件: %s", plain)
	}
}

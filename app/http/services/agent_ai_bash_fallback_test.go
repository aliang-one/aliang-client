package services

import (
	"os"
	"path/filepath"
	"testing"
)

// 2026-11-25 线上事故:Windows 机器会话中途装了 Git for Windows(自带
// bash),但运行中的 agent 进程 PATH 快照不刷新,LookPath("bash") 探不到,
// 模型的备用 shell 被堵死。兜底探测按 Git 固定安装路径补一轮;这里锁定
// 「候选列表里挑第一个存在的可执行文件」的选择逻辑。

func TestFirstExistingExecutablePicksFirstHit(t *testing.T) {
	dir := t.TempDir()
	hit := filepath.Join(dir, "bash.exe")
	if err := os.WriteFile(hit, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	missing := filepath.Join(dir, "nope.exe")

	if got := firstExistingExecutable([]string{missing, hit, filepath.Join(dir, "later.exe")}); got != hit {
		t.Fatalf("firstExistingExecutable = %q, want %q", got, hit)
	}
}

func TestFirstExistingExecutableEmptyWhenAllMissing(t *testing.T) {
	dir := t.TempDir()
	candidates := []string{filepath.Join(dir, "a.exe"), filepath.Join(dir, "b.exe")}
	if got := firstExistingExecutable(candidates); got != "" {
		t.Fatalf("全不存在应返回空串,得到 %q", got)
	}
}

func TestFirstExistingExecutableRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	// 同名目录不能冒充可执行文件。
	if got := firstExistingExecutable([]string{dir}); got != "" {
		t.Fatalf("目录不应命中,得到 %q", got)
	}
}

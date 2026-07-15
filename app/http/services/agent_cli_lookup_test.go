package services

import (
	"os"
	"path/filepath"
	"strings"
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

func TestLookPathInHomesFindsUserChatGPTCodexOnDarwin(t *testing.T) {
	home := t.TempDir()
	codex := filepath.Join(
		home,
		"Applications",
		"ChatGPT.app",
		"Contents",
		"Resources",
		"codex",
	)
	if err := os.MkdirAll(filepath.Dir(codex), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codex, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := lookPathInHomesForOS([]string{home}, "codex", "darwin")
	if err != nil {
		t.Fatalf("期望 service PATH 缺失时发现 ChatGPT.app 内置 Codex，错误: %v", err)
	}
	if got != codex {
		t.Errorf("找到 %q，期望 %q", got, codex)
	}
}

func TestLookPathInHomesPrefersStandaloneCodexOverChatGPTBundle(t *testing.T) {
	home := t.TempDir()
	standalone := filepath.Join(home, ".local", "bin", "codex")
	bundled := filepath.Join(
		home,
		"Applications",
		"ChatGPT.app",
		"Contents",
		"Resources",
		"codex",
	)
	for _, path := range []string{standalone, bundled} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	got, err := lookPathInHomesForOS([]string{home}, "codex", "darwin")
	if err != nil {
		t.Fatal(err)
	}
	if got != standalone {
		t.Errorf("找到 %q，期望优先独立安装 %q", got, standalone)
	}
}

func TestPlatformBinCandidatesOnlyAddsChatGPTBundleForCodex(t *testing.T) {
	home := "/Users/example"
	codexCandidates := platformBinCandidates("darwin", []string{home}, "codex")
	wantUserBundle := filepath.Join(
		home,
		"Applications",
		"ChatGPT.app",
		"Contents",
		"Resources",
		"codex",
	)
	wantSystemBundle := filepath.Join(
		"/Applications",
		"ChatGPT.app",
		"Contents",
		"Resources",
		"codex",
	)
	for _, want := range []string{wantUserBundle, wantSystemBundle} {
		if !agentAIStringSliceContains(codexCandidates, want) {
			t.Errorf("Codex 候选缺少 %q；got %v", want, codexCandidates)
		}
	}

	claudeCandidates := platformBinCandidates("darwin", []string{home}, "claude")
	for _, candidate := range claudeCandidates {
		if strings.Contains(candidate, "ChatGPT.app") {
			t.Errorf("Claude 候选不应包含 ChatGPT bundle：%q", candidate)
		}
	}
	if got := platformBinCandidates("linux", []string{home}, "codex"); got != nil {
		t.Errorf("Linux 不应包含 macOS 候选；got %v", got)
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

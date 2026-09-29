package services

import (
	"os"
	"path/filepath"
	"runtime"
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
	isolateDarwinSystemBins(t)
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
	isolateDarwinSystemBins(t)
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
	isolateDarwinSystemBins(t)
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

	got, err := lookPathInHomesForOS([]string{home}, "codex", "darwin", "arm64")
	if err != nil {
		t.Fatalf("期望 service PATH 缺失时发现 ChatGPT.app 内置 Codex，错误: %v", err)
	}
	if got != codex {
		t.Errorf("找到 %q，期望 %q", got, codex)
	}
}

func TestLookPathInHomesPrefersStandaloneCodexOverChatGPTBundle(t *testing.T) {
	isolateDarwinSystemBins(t)
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

	got, err := lookPathInHomesForOS([]string{home}, "codex", "darwin", "arm64")
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

// newFakeExtension 在给定家目录相对路径下生成一个可执行文件。
func newFakeExtension(t *testing.T, home, relPath string) string {
	t.Helper()
	bin := filepath.Join(home, relPath)
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// isolateDarwinSystemBins 把机器级绝对路径候选指到不存在的临时路径，隔离
// 开发机真实安装（/opt/homebrew/bin/claude、/Applications/ChatGPT.app/codex 等）
// 对「全部候选落空后回落扩展目录」断言的污染。测试串行执行，允许改包级变量。
func isolateDarwinSystemBins(t *testing.T) {
	t.Helper()
	origBins, origBundle := darwinSystemBinDirs, darwinSystemAppBundleCodex
	darwinSystemBinDirs = []string{filepath.Join(t.TempDir(), "no-homebrew")}
	darwinSystemAppBundleCodex = filepath.Join(t.TempDir(), "no-chatgpt", "codex")
	t.Cleanup(func() {
		darwinSystemBinDirs, darwinSystemAppBundleCodex = origBins, origBundle
	})
}

func TestLookPathInHomesFindsClaudeInVSCodeExtension(t *testing.T) {
	isolateDarwinSystemBins(t)
	dir := t.TempDir()
	oldBin := newFakeExtension(t, dir, filepath.Join(".vscode/extensions",
		"anthropic.claude-code-2.1.274-darwin-arm64", "resources/native-binary/claude"))
	newBin := newFakeExtension(t, dir, filepath.Join(".vscode/extensions",
		"anthropic.claude-code-2.1.278-darwin-arm64", "resources/native-binary/claude"))

	got, err := lookPathInHomesForOS([]string{dir}, "claude", "darwin", "arm64")
	if err != nil {
		t.Fatalf("期望独立 CLI 缺失时发现 VSCode 扩展内置 claude，错误: %v", err)
	}
	if got != newBin {
		t.Errorf("找到 %q，期望最新版本 %q（而非 %q）", got, newBin, oldBin)
	}
}

func TestLookPathInHomesFindsCodexInChatGPTExtension(t *testing.T) {
	isolateDarwinSystemBins(t)
	dir := t.TempDir()
	bin := newFakeExtension(t, dir, filepath.Join(".vscode/extensions",
		"openai.chatgpt-26.5908.31748-darwin-arm64", "bin/macos-aarch64/codex"))

	got, err := lookPathInHomesForOS([]string{dir}, "codex", "darwin", "arm64")
	if err != nil {
		t.Fatalf("期望发现 ChatGPT 扩展内置 codex，错误: %v", err)
	}
	if got != bin {
		t.Errorf("找到 %q，期望 %q", got, bin)
	}
}

func TestLookPathInHomesFindsExtensionAcrossEditors(t *testing.T) {
	isolateDarwinSystemBins(t)
	for _, editorRoot := range []string{
		".vscode-insiders/extensions",
		".vscode-server/extensions",
		".vscode-oss/extensions",
		".cursor/extensions",
		".windsurf/extensions",
	} {
		dir := t.TempDir()
		bin := newFakeExtension(t, dir, filepath.Join(editorRoot,
			"anthropic.claude-code-2.1.278-darwin-arm64", "resources/native-binary/claude"))
		got, err := lookPathInHomesForOS([]string{dir}, "claude", "darwin", "arm64")
		if err != nil {
			t.Fatalf("%s: 期望发现扩展内置 claude，错误: %v", editorRoot, err)
		}
		if got != bin {
			t.Errorf("%s: 找到 %q，期望 %q", editorRoot, got, bin)
		}
	}
}

func TestLookPathInHomesPrefersStandaloneOverExtension(t *testing.T) {
	isolateDarwinSystemBins(t)
	dir := t.TempDir()
	standalone := newFakeExtension(t, dir, filepath.Join(".local/bin", "claude"))
	extBin := newFakeExtension(t, dir, filepath.Join(".vscode/extensions",
		"anthropic.claude-code-2.1.278-darwin-arm64", "resources/native-binary/claude"))

	got, err := lookPathInHomesForOS([]string{dir}, "claude", "darwin", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if got != standalone {
		t.Errorf("找到 %q，期望优先独立安装 %q（扩展内 %q）", got, standalone, extBin)
	}
}

func TestExtensionDirSelectionIgnoresOtherPlatformsAndLegacyNames(t *testing.T) {
	isolateDarwinSystemBins(t)
	// 其它平台的扩展包不应被选中；无平台后缀的旧式目录名应被接受。
	dir := t.TempDir()
	newFakeExtension(t, dir, filepath.Join(".vscode/extensions",
		"anthropic.claude-code-2.1.300-linux-x64", "resources/native-binary/claude"))
	legacyBin := newFakeExtension(t, dir, filepath.Join(".vscode/extensions",
		"anthropic.claude-code-1.2.3", "resources/native-binary/claude"))

	got, err := lookPathInHomesForOS([]string{dir}, "claude", "darwin", "arm64")
	if err != nil {
		t.Fatalf("期望回退到无平台后缀的旧式目录，错误: %v", err)
	}
	if got != legacyBin {
		t.Errorf("找到 %q，期望 %q", got, legacyBin)
	}
}

func TestExtensionDirPicksNewestVersionNumerically(t *testing.T) {
	isolateDarwinSystemBins(t)
	dir := t.TempDir()
	newFakeExtension(t, dir, filepath.Join(".vscode/extensions",
		"anthropic.claude-code-2.1.9-darwin-arm64", "resources/native-binary/claude"))
	newBin := newFakeExtension(t, dir, filepath.Join(".vscode/extensions",
		"anthropic.claude-code-2.1.278-darwin-arm64", "resources/native-binary/claude"))

	got, err := lookPathInHomesForOS([]string{dir}, "claude", "darwin", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if got != newBin {
		t.Errorf("找到 %q，期望按数值比较取 %q（2.1.278 > 2.1.9）", got, newBin)
	}
}

func TestLookPathInHomesFindsExtensionExeOnWindows(t *testing.T) {
	dir := t.TempDir()
	bin := newFakeExtension(t, dir, filepath.Join(".vscode/extensions",
		"anthropic.claude-code-2.1.278-win32-x64", "resources/native-binary/claude.exe"))

	got, err := lookPathInHomesForOS([]string{dir}, "claude", "windows", "amd64")
	if err != nil {
		t.Fatalf("期望 Windows 下发现扩展内置 claude.exe，错误: %v", err)
	}
	if got != bin {
		t.Errorf("找到 %q，期望 %q", got, bin)
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

// ---- 多候选并存时按版本取最新(2026-09-29 事故回归) ----
// 事故:GUI 启动的 agent PATH 缺失,fallback 扫描里 ~/.local/bin 排在
// /opt/homebrew/bin 之前,陈旧的 2.1.17 native 装机仅凭目录顺序压过了
// 2.1.280 的 homebrew 装机。目录顺序不应决定生死,版本才是。

func writeVersionedFakeCLI(t *testing.T, dir, name, version string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir fake cli dir: %v", err)
	}
	body := "#!/bin/sh\n"
	if version != "" {
		body += "echo \"" + version + "\"\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake cli: %v", err)
	}
	return path
}

func overrideDarwinSystemBins(t *testing.T, dir string) {
	t.Helper()
	orig := darwinSystemBinDirs
	darwinSystemBinDirs = []string{dir}
	t.Cleanup(func() { darwinSystemBinDirs = orig })
}

func TestLookPathInHomesForOSPrefersNewestAcrossInstallRoots(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is unix-only")
	}
	home := t.TempDir()
	stale := writeVersionedFakeCLI(t, filepath.Join(home, ".local", "bin"), "claude", "2.1.17 (Claude Code)")
	sysDir := t.TempDir()
	newest := writeVersionedFakeCLI(t, sysDir, "claude", "2.1.280 (Claude Code)")
	overrideDarwinSystemBins(t, sysDir)

	got, err := lookPathInHomesForOS([]string{home}, "claude", "darwin", "arm64")
	if err != nil {
		t.Fatalf("lookPathInHomesForOS: %v", err)
	}
	if got != newest {
		t.Errorf("找到 %q,期望按版本选中新装机 %q(而非目录序先到的 %q)", got, newest, stale)
	}
}

func TestLookPathInHomesForOSNewerHomeInstallBeatsOlderSystem(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is unix-only")
	}
	home := t.TempDir()
	newer := writeVersionedFakeCLI(t, filepath.Join(home, ".local", "bin"), "claude", "2.1.300 (Claude Code)")
	sysDir := t.TempDir()
	writeVersionedFakeCLI(t, sysDir, "claude", "2.1.280 (Claude Code)")
	overrideDarwinSystemBins(t, sysDir)

	got, err := lookPathInHomesForOS([]string{home}, "claude", "darwin", "arm64")
	if err != nil {
		t.Fatalf("lookPathInHomesForOS: %v", err)
	}
	if got != newer {
		t.Errorf("找到 %q,期望家目录新装机 %q 胜出", got, newer)
	}
}

func TestLookPathInHomesForOSKeepsOrderWhenVersionsUnknown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is unix-only")
	}
	home := t.TempDir()
	first := writeVersionedFakeCLI(t, filepath.Join(home, ".local", "bin"), "claude", "")
	sysDir := t.TempDir()
	writeVersionedFakeCLI(t, sysDir, "claude", "")
	overrideDarwinSystemBins(t, sysDir)

	got, err := lookPathInHomesForOS([]string{home}, "claude", "darwin", "arm64")
	if err != nil {
		t.Fatalf("lookPathInHomesForOS: %v", err)
	}
	if got != first {
		t.Errorf("版本全部探不到时应保持原顺序,期望 %q,得到 %q", first, got)
	}
}

// 版本仲裁不得越过文档化的「独立安装优先于 ChatGPT.app 内置」偏好:
// 应用捆绑版随 app 更新整体替换,即使版本号更高也不参与仲裁。
func TestLookPathInHomesForOSStandaloneBeatsNewerAppBundle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is unix-only")
	}
	home := t.TempDir()
	standalone := writeVersionedFakeCLI(t, filepath.Join(home, ".local", "bin"), "codex", "codex-cli 0.144.5")
	bundled := writeVersionedFakeCLI(t, filepath.Join(home, "Applications", "ChatGPT.app", "Contents", "Resources"), "codex", "codex-cli 0.150.0")
	overrideDarwinSystemBins(t, t.TempDir())

	got, err := lookPathInHomesForOS([]string{home}, "codex", "darwin", "arm64")
	if err != nil {
		t.Fatalf("lookPathInHomesForOS: %v", err)
	}
	if got != standalone {
		t.Errorf("找到 %q,期望独立安装 %q 胜出(尽管 app 内置版 %q 版本更高)", got, standalone, bundled)
	}
}

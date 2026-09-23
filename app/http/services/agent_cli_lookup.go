package services

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"aliang.one/nursorgate/internal/runtimepath"
)

// userBinSubdirs 是用户级 CLI（claude/codex/opencode 等）常见的安装子目录，
// 相对家目录解析。覆盖 npm/volta/bun/yarn/cargo/deno 的全局安装位置。
var userBinSubdirs = []string{
	".local/bin",
	".npm-global/bin",
	".volta/bin",
	".bun/bin",
	".yarn/bin",
	".cargo/bin",
	".deno/bin",
}

// extensionRootSubdirs 是 VSCode 系编辑器的扩展目录（相对家目录）。覆盖
// VSCode 稳定版/Insiders、SSH Remote（.vscode-server，Linux/容器场景）、
// VSCodium、Cursor 与 Windsurf；Windows 上同样以家目录相对路径命中
// %USERPROFILE%\.vscode\extensions。
var extensionRootSubdirs = []string{
	".vscode/extensions",
	".vscode-insiders/extensions",
	".vscode-server/extensions",
	".vscode-oss/extensions",
	".cursor/extensions",
	".windsurf/extensions",
}

// lookPathCLI 定位 AI CLI 可执行文件（claude/codex/opencode/claudecode）。
//
// 先走 exec.LookPath，覆盖 PATH 正常的普通启动场景（macOS 桌面、非 sudo 运行）。
// 失败时——典型情况是 Linux 以 sudo 或 systemd 启动，PATH 被 secure_path 重置成
// 不含用户级目录——退回扫描「当前用户 + 桌面登录用户」家目录下的常见 bin 子目录，
// 以及 nvm 的 ~/.nvm/versions/node/*/bin 和 macOS 的 /opt/homebrew/bin。仍找不到时
// 最后兜底 VSCode 系编辑器扩展内置的 CLI（anthropic.claude-code / openai.chatgpt
// 扩展捆绑了完整原生二进制，独立运行可用且 codex 支持 app-server 审批桥）。
//
// 这样 sudo 下 /root/.local/bin/claude、或桌面用户 ~/.local/bin/claude 仍能被定位，
// 而 PATH 正常时行为与 exec.LookPath 完全一致。
func lookPathCLI(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return lookPathInHomes(cliSearchHomes(), name)
}

// cliSearchHomes 收集要扫描的家目录候选并去重：EffectiveAgentHome（root 下解析
// 桌面登录用户）、当前 HOME、UserHomeDir。三者共同覆盖「root 直接登录、claude
// 装在 /root/.local/bin」与「普通用户 sudo、claude 装在自己家」两种典型情况。
func cliSearchHomes() []string {
	var homes []string
	if h, err := runtimepath.EffectiveAgentHome(); err == nil {
		if h = strings.TrimSpace(h); h != "" {
			homes = append(homes, h)
		}
	}
	if h := strings.TrimSpace(os.Getenv("HOME")); h != "" {
		homes = append(homes, h)
	}
	if h, err := runtimepath.UserHomeDir(); err == nil {
		if h = strings.TrimSpace(h); h != "" {
			homes = append(homes, h)
		}
	}
	return dedupStrings(homes)
}

// lookPathInHomes 在给定家目录集合与平台目录中查找可执行文件 name。
// 抽出来便于单测注入临时目录，不依赖真实家目录。
func lookPathInHomes(homes []string, name string) (string, error) {
	return lookPathInHomesForOS(homes, name, runtime.GOOS, runtime.GOARCH)
}

func lookPathInHomesForOS(homes []string, name, goos, goarch string) (string, error) {
	for _, home := range homes {
		for _, candidate := range userBinCandidates(home, name) {
			if isExecutableFile(candidate) {
				return candidate, nil
			}
		}
	}
	for _, candidate := range platformBinCandidates(goos, homes, name) {
		if isExecutableFile(candidate) {
			return candidate, nil
		}
	}
	// VSCode 系编辑器扩展内置的 CLI 排在最后兜底：独立安装生命周期独立且
	// 稳定，扩展捆绑版可能是 alpha 且随扩展更新被整体替换。
	for _, candidate := range extensionBinCandidatesForOS(goos, goarch, homes, name) {
		if isExecutableFile(candidate) {
			return candidate, nil
		}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

func userBinCandidates(home, name string) []string {
	var out []string
	for _, sub := range userBinSubdirs {
		out = append(out, filepath.Join(home, sub, name))
	}
	// nvm: ~/.nvm/versions/node/<ver>/bin/<name>，需展开版本子目录。
	nvmNode := filepath.Join(home, ".nvm", "versions", "node")
	if entries, err := os.ReadDir(nvmNode); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				out = append(out, filepath.Join(nvmNode, e.Name(), "bin", name))
			}
		}
	}
	return out
}

// darwinSystemBinDirs 与 darwinSystemAppBundleCodex 是机器级绝对路径候选，
// 提取为变量以便单测把开发机真实安装（/opt/homebrew/bin/claude、
// /Applications/ChatGPT.app 等）与兜底扫描断言隔离。
var (
	darwinSystemBinDirs        = []string{"/opt/homebrew/bin", "/usr/local/bin"}
	darwinSystemAppBundleCodex = "/Applications/ChatGPT.app/Contents/Resources/codex"
)

func platformBinCandidates(goos string, homes []string, name string) []string {
	if goos != "darwin" {
		return nil
	}

	// Prefer standalone package-manager installs over application-bundled CLIs.
	// They have an independent lifecycle and remain stable across app updates.
	out := make([]string, 0, len(darwinSystemBinDirs)+2*len(homes)+1)
	for _, dir := range darwinSystemBinDirs {
		out = append(out, filepath.Join(dir, name))
	}
	if name != "codex" {
		return out
	}

	// ChatGPT for macOS ships a fully functional Codex CLI, but LaunchAgents and
	// services do not inherit the interactive shell path that exposes it. Support
	// both per-user and system-wide app installs as a deterministic fallback.
	for _, home := range homes {
		out = append(out, filepath.Join(
			home,
			"Applications",
			"ChatGPT.app",
			"Contents",
			"Resources",
			"codex",
		))
	}
	out = append(out, darwinSystemAppBundleCodex)
	return dedupStrings(out)
}

// isExecutableFile 判断路径是否为可执行文件（非目录且带任意执行位）。
// 与 exec.LookPath 在 root 下的判定语义一致：只要存在任一 x 位即可执行。
func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}

// extensionDirPrefixes 返回捆绑了 CLI name 的扩展目录名前缀。
// VSCode 按平台独立发布扩展包，目录名形如
// anthropic.claude-code-2.1.278-darwin-arm64 / openai.chatgpt-26.5908.31748-darwin-arm64。
func extensionDirPrefixes(name string) []string {
	switch name {
	case "claude", "claudecode":
		return []string{"anthropic.claude-code-"}
	case "codex":
		return []string{"openai.chatgpt-"}
	default:
		return nil
	}
}

// extensionBinaryName 平台化二进制名：Windows 扩展捆绑的是 .exe。
func extensionBinaryName(name, goos string) string {
	if goos == "windows" {
		return name + ".exe"
	}
	return name
}

// extensionCLIBinGlobs 返回扩展目录内二进制的 glob 相对路径模式。
// codex 的 bin 下平台子目录名随发布包变化（macos-aarch64/linux-x64/...），用 * 通配。
func extensionCLIBinGlobs(name, goos string) []string {
	bin := extensionBinaryName(name, goos)
	switch name {
	case "claude", "claudecode":
		return []string{filepath.Join("resources", "native-binary", bin)}
	case "codex":
		return []string{filepath.Join("bin", "*", bin)}
	default:
		return nil
	}
}

// extensionBinCandidatesForOS 在各编辑器扩展目录中查找捆绑的 CLI。
// 同一扩展多版本共存时（VSCode 升级后旧版本目录残留）按版本号数值取最新。
func extensionBinCandidatesForOS(goos, goarch string, homes []string, name string) []string {
	prefixes := extensionDirPrefixes(name)
	if len(prefixes) == 0 {
		return nil
	}
	var out []string
	for _, home := range homes {
		for _, root := range extensionRootSubdirs {
			entries, err := os.ReadDir(filepath.Join(home, root))
			if err != nil {
				continue
			}
			for _, prefix := range prefixes {
				dir := newestExtensionDir(entries, prefix, goos, goarch)
				if dir == "" {
					continue
				}
				base := filepath.Join(home, root, dir)
				for _, pattern := range extensionCLIBinGlobs(name, goos) {
					matches, err := filepath.Glob(filepath.Join(base, pattern))
					if err != nil {
						continue
					}
					out = append(out, matches...)
				}
			}
		}
	}
	return dedupStrings(out)
}

// newestExtensionDir 从扩展目录列表中选出 prefix 前缀下版本最新、且与本机
// 平台匹配的目录名；无匹配返回空串。
func newestExtensionDir(entries []os.DirEntry, prefix, goos, goarch string) string {
	bestName := ""
	var bestVersion []int
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		remainder := strings.TrimPrefix(e.Name(), prefix)
		if !extensionDirMatchesPlatform(remainder, goos, goarch) {
			continue
		}
		version, _ := parseExtensionVersion(remainder)
		if bestName == "" || compareExtensionVersions(version, bestVersion) > 0 {
			bestName = e.Name()
			bestVersion = version
		}
	}
	return bestName
}

var extensionGoosByPlatform = map[string]string{
	"darwin": "darwin", "macos": "darwin",
	"linux": "linux", "alpine": "linux",
	"win32": "windows", "windows": "windows",
}

var extensionGoarchByArch = map[string]string{
	"x64": "amd64", "amd64": "amd64", "x86_64": "amd64",
	"arm64": "arm64", "aarch64": "arm64",
	"armhf": "arm", "arm": "arm",
	"ia32": "386", "x86": "386",
}

// extensionDirMatchesPlatform 校验扩展目录名尾部平台后缀（如 -darwin-arm64）
// 是否匹配本机。末两段不是可识别平台后缀时（旧式无后缀目录名）按匹配处理。
func extensionDirMatchesPlatform(remainder, goos, goarch string) bool {
	tokens := strings.Split(remainder, "-")
	if len(tokens) < 3 {
		return true
	}
	dirGoos, platKnown := extensionGoosByPlatform[strings.ToLower(tokens[len(tokens)-2])]
	dirGoarch, archKnown := extensionGoarchByArch[strings.ToLower(tokens[len(tokens)-1])]
	if !platKnown || !archKnown {
		return true
	}
	return dirGoos == goos && dirGoarch == goarch
}

// parseExtensionVersion 解析扩展目录名余部开头的主干版本号（如
// "2.1.278-darwin-arm64" → [2 1 278]）；无版本号时 ok=false。
func parseExtensionVersion(remainder string) ([]int, bool) {
	end := 0
	for end < len(remainder) && (remainder[end] == '.' || (remainder[end] >= '0' && remainder[end] <= '9')) {
		end++
	}
	numeric := strings.Trim(remainder[:end], ".")
	if numeric == "" {
		return nil, false
	}
	segments := strings.Split(numeric, ".")
	version := make([]int, 0, len(segments))
	for _, seg := range segments {
		n, err := strconv.Atoi(seg)
		if err != nil {
			return nil, false
		}
		version = append(version, n)
	}
	return version, true
}

// compareExtensionVersions 逐段数值比较，缺失段按 0 补齐（2.1 < 2.1.0 按 0 处理）。
func compareExtensionVersions(a, b []int) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		av, bv := 0, 0
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}
		if av != bv {
			if av > bv {
				return 1
			}
			return -1
		}
	}
	return 0
}

func dedupStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

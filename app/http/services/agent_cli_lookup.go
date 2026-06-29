package services

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// lookPathCLI 定位 AI CLI 可执行文件（claude/codex/opencode/claudecode）。
//
// 先走 exec.LookPath，覆盖 PATH 正常的普通启动场景（macOS 桌面、非 sudo 运行）。
// 失败时——典型情况是 Linux 以 sudo 或 systemd 启动，PATH 被 secure_path 重置成
// 不含用户级目录——退回扫描「当前用户 + 桌面登录用户」家目录下的常见 bin 子目录，
// 以及 nvm 的 ~/.nvm/versions/node/*/bin 和 macOS 的 /opt/homebrew/bin。
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

// lookPathInHomes 在给定家目录集合与平台全局目录中查找可执行文件 name。
// 抽出来便于单测注入临时目录，不依赖真实家目录。
func lookPathInHomes(homes []string, name string) (string, error) {
	for _, home := range homes {
		for _, candidate := range userBinCandidates(home, name) {
			if isExecutableFile(candidate) {
				return candidate, nil
			}
		}
	}
	for _, candidate := range globalBinCandidates(name) {
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

func globalBinCandidates(name string) []string {
	if runtime.GOOS == "darwin" {
		return []string{
			filepath.Join("/opt/homebrew/bin", name),
			filepath.Join("/usr/local/bin", name),
		}
	}
	return nil
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

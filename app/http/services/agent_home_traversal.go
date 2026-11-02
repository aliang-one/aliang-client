package services

import (
	"path/filepath"
	"strings"

	"aliang.one/nursorgate/internal/runtimepath"

	"github.com/google/shlex"
)

// 家目录根遍历拦截。
//
// 2026-11-01 线上事故的直接触发点：模型找不到项目时退化成
// `find ~ -maxdepth 4 -iname "*aliang*"` / `ls -d ~/*aliang* ~/*/*aliang*`。
// 这类命令会一路扫进 macOS 的隐私库（~/Music、~/Pictures、~/Desktop、
// ~/Documents、~/Downloads…），一碰就弹系统授权框，且**请求进程被同步阻塞**
// 直到有人点「允许」。手机驱动的 run 没人守在 Mac 前 → 工具卡死。
//
// 纵深防御已经有两层（exec 组杀、遍历跳过隐私目录），这里加的是**最前面那道**：
// 与其先弹一张审批卡、批准后再撞墙，不如直接告诉模型「别扫家目录，用项目根」。
// 拒绝理由会原样回到模型（runShell → "拒绝执行: <reason>"），模型能立刻改道。
//
// 判据刻意收得很窄：只拦「以家目录本身 / 根 / 用户目录容器为搜索根」的写法。
// `find ~/MyProgram/proj -name x` 这类**已经指明子目录**的搜索照旧放行——
// 那是正常的、范围明确的操作。

// homeTraversalCommands 是会递归/枚举目录的命令。
var homeTraversalCommands = map[string]bool{
	"find": true, "fd": true, "ls": true, "tree": true,
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true,
	"du": true, "locate": true, "mdfind": true, "rsync": true,
}

// rootTraversalPrefixes 是「扫了就等于扫整个家目录/整个盘」的路径前缀。
// 与家目录合并判定，避免把 /Users/<me> 这种写法漏掉。
var rootTraversalPrefixes = []string{"/", "/Users", "/Volumes", "/private", "/System", "/Library"}

// homeRootTraversalReason 判定命令是否以家目录根/盘根为搜索根。
// 返回非空字符串即拒绝理由（给模型看，也进日志）。
func homeRootTraversalReason(command string) string {
	segments, ok := splitShellCommandSegments(command)
	if !ok {
		// 解析不了就交给后面的常规规则（含 fail-safe 审批），不在这一层误杀。
		return ""
	}
	for _, segment := range segments {
		words, err := shlex.Split(segment)
		if err != nil || len(words) < 2 {
			continue
		}
		if !homeTraversalCommands[filepath.Base(words[0])] {
			continue
		}
		for _, word := range words[1:] {
			value := strings.TrimSpace(word)
			if value == "" || strings.HasPrefix(value, "-") || strings.Contains(value, "://") {
				continue
			}
			if isRootSearchScope(value) {
				return "命令把家目录/盘根当作搜索范围（" + value + "）。家目录下有 macOS 的隐私库" +
					"（音乐、照片、桌面、文稿、下载），扫描会弹出系统授权框并把命令卡死。" +
					"请改用项目目录或明确的子目录，例如 `find . -name ...`、" +
					"`grep -rn PATTERN ./src`；确实需要跨目录找项目时，请让用户在电脑前先确认授权。"
			}
		}
	}
	return ""
}

// isRootSearchScope 判断一个 shell 词是不是「家目录根或盘根」级别的搜索范围。
//
// 关键是要把 glob 也认出来：`~/*aliang*` 和 `~/*/*aliang*` 展开后都以家目录
// 开头，扫起来与 `find ~` 等价。所以先截掉通配符及其后的部分，再判前缀。
//
// `~` 的展开刻意**不走** agentcore.ExpandHome（它用 os.UserHomeDir）：判界
// 与展开必须是同一个家目录口径，否则 root 跑 core 时（HOME=/var/root）会出现
// 「按 /Users/x 判界、按 /var/root 展开」的静默失效。这里统一用
// runtimepath.EffectiveAgentHome，与 tccguard 同源。
func isRootSearchScope(value string) bool {
	home, err := agentcoreHomeDir()
	if err != nil {
		home = ""
	}
	expanded := expandHomeAgainst(home, value)
	if expanded == "" {
		return false
	}
	// 截到第一个通配符之前：`/Users/me/*/*aliang*` → `/Users/me/`
	if idx := strings.IndexAny(expanded, "*?[{"); idx >= 0 {
		expanded = expanded[:idx]
	}
	clean := filepath.Clean(expanded)

	if home != "" && clean == filepath.Clean(home) {
		return true
	}
	for _, prefix := range rootTraversalPrefixes {
		if filepath.Clean(prefix) == clean {
			return true
		}
	}
	return false
}

// expandHomeAgainst 用给定家目录展开前导 ~。家目录为空时不展开（返回原值）。
func expandHomeAgainst(home, value string) string {
	if home == "" {
		return value
	}
	switch {
	case value == "~":
		return home
	case strings.HasPrefix(value, "~/"):
		return filepath.Join(home, value[2:])
	case strings.HasPrefix(value, `~\`):
		return filepath.Join(home, value[2:])
	}
	return value
}

// agentcoreHomeDir 与 tccguard 同源取家目录（runtimepath.EffectiveAgentHome）。
// 包一层变量是为了测试可替换。
var agentcoreHomeDir = func() (string, error) { return runtimepath.EffectiveAgentHome() }

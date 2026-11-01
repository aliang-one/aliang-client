package agentcore

import (
	"errors"
	"path/filepath"
	"strings"

	"aliang.one/nursorgate/internal/runtimepath"
	"aliang.one/nursorgate/internal/tccguard"
)

// 隐私目录防护。
//
// 背景（2026-11-01 线上事故）：模型在找不到项目时会退化成全盘搜索，典型形态是
// `find ~ -maxdepth 4 …` 或 `ls -d ~/*aliang* ~/*/*aliang*`。家目录下的一级
// 子目录里有 macOS 的隐私库（~/Music、~/Pictures/Photos Library、~/Desktop、
// ~/Documents、~/Downloads…），一碰就由 tccd 弹系统授权框，且**请求进程会被
// 同步阻塞**直到有人点「允许/拒绝」。手机驱动的 run 没人守在 Mac 前，于是：
//
//	工具卡死 → 30 秒超时杀不掉（见 exec_unix.go）→ 1 小时看门狗才收场。
//
// 两道防线：
//  1. 遍历（Grep/Glob）：遇到隐私目录**整棵跳过**，纯字符串判定，绝不触发探测；
//  2. 显式路径（Read/Write/Edit/LS/Grep/Glob 的根）：走 tccguard.GuardPath，
//     无人值守时直接返回可读错误，而不是弹一个没人能回答的框。

// homePrivacyDirs 是家目录下应当「遍历时整棵跳过」的一级目录。
//
// 这些目录要么体量极大（Library、Pictures），要么隐私敏感/会触发 TCC 弹窗
// （Music=音乐库、Pictures=照片库、Desktop/Documents/Downloads=文件与文件夹）。
// 名字按大小写不敏感比较（macOS 默认卷不区分大小写）。
var homePrivacyDirs = []string{
	"Library",
	"Music",
	"Pictures",
	"Movies",
	"Desktop",
	"Documents",
	"Downloads",
	"Applications",
	".Trash",
}

// underHomePrivacyDir 判断 abs 是否位于家目录的隐私子目录内（纯字符串判定，
// 不碰磁盘、不触发任何授权框）。
//
// 家目录取 runtimepath.EffectiveAgentHome，与 tccguard 同源：agent 以 root
// 运行（Mac 上 core 是 root LaunchDaemon）时 HOME=/var/root，若这里改用
// os.UserHomeDir，字符串层会对真实用户目录全部判 false —— 覆盖 ~/Library、
// ~/Movies、~/Applications 的那一层就静默失效了。
func underHomePrivacyDir(abs string) bool {
	if strings.TrimSpace(abs) == "" {
		return false
	}
	home, err := runtimepath.EffectiveAgentHome()
	if err != nil || strings.TrimSpace(home) == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(home), filepath.Clean(abs))
	if err != nil {
		return false
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	first := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
	for _, dir := range homePrivacyDirs {
		if strings.EqualFold(first, dir) {
			return true
		}
	}
	return false
}

// skipPrivacyDir 是遍历回调里用的判定：隐私目录、外置卷、其他账户家目录一律
// 不下潜。用 StatAllowed 而不是 GuardPath —— 后者在「有人在电脑前」时会真的
// 去探测（弹框），遍历途中弹框是我们最不想要的行为。
func skipPrivacyDir(abs string) bool {
	if underHomePrivacyDir(abs) {
		return true
	}
	return errors.Is(tccguard.StatAllowed(abs), tccguard.ErrConsentRequired)
}

// shouldSkipWalkDir 是 Glob/Grep 遍历回调里「这个目录要不要整棵跳过」的统一
// 判定，抽出来是为了能被单测覆盖（遍历回调本身很难在测试里驱动）。
//
// 三种跳过理由：
//   - 体量大且几乎不可能是搜索目标的目录名（.git/node_modules/vendor…）；
//   - 隐私目录 / 外置卷 / 其他账户家目录（会弹 macOS 授权框并阻塞）；
//   - 超过 maxWalkDepth 层（防「根选错」时一路下潜）。
//
// root 自身永不跳过，**root 落在隐私目录内时其后代也不跳过**：用户显式点名要搜
// 的根必须真的被搜。仓库放在 ~/Documents、~/Desktop 或 iCloud 里是很常见的
// 习惯（tccguard 的报错文案本身就在建议用户换个位置，说明这类项目真实存在）；
// 若只豁免 root 本身、却把它的每个子目录都 SkipDir，Grep 会退化成「只搜顶层」，
// 而且因为没触发条目上限，连「结果不完整」都不会提示——静默给出错误答案。
func shouldSkipWalkDir(root, p string) bool {
	if p == root {
		return false
	}
	if shouldSkipDir(filepath.Base(p)) {
		return true
	}
	if walkDepth(root, p) > maxWalkDepth {
		return true
	}
	// root 已经在隐私目录里（用户显式点名），不再对其后代做隐私跳过。
	if underHomePrivacyDir(root) {
		return false
	}
	return skipPrivacyDir(p)
}

// guardToolPath 在工具真正触碰磁盘前做隐私闸。
//
// 与遍历不同，这里用 GuardPath：显式点名访问某个受保护路径时，如果用户就在
// Mac 前，让他确认一次是合理的（授权会被缓存）；无人值守则直接给出可执行的
// 中文错误，模型能据此换路径，手机也不会再看到「没反应」。
//
// 返回 nil 表示放行。返回的 error 已是为人类写好的文案，直接回给模型。
func guardToolPath(abs string) error {
	if strings.TrimSpace(abs) == "" {
		return nil
	}
	if err := tccguard.GuardPath(abs); err != nil {
		return err
	}
	return nil
}

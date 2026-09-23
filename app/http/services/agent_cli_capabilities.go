package services

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"
)

// 本文件实现「设备 AI CLI 能力探测」:定位到的每个 CLI(claude/codex/opencode/pi/
// gemini)探测其版本与支持的 effort 档,随 detectAgentTools 的 tools 快照一并上报云端,
// 供服务端在派发时按设备能力自动降级 effort、手机端置灰不支持的档位。
//
// 探测遵循仓内既有约定(见 probeClaudeRawVersion / codexAppServerAvailable):
//   - sync.Map + executableProbeCacheKey 缓存,key 含符号链接解析+mtime+size,
//     二进制更新后自动失效,无需 TTL;
//   - 失败不缓存(下次快照重试),exec 用 newBackgroundCommandContext 而非用户 shell;
//   - 探测失败一律视为「未知」,不设限制——宁缺勿错。

const (
	// effort 探针哨兵值:故意非法,让 CLI 在参数校验阶段立即报出合法清单。
	// 配合一个必然无效的模型名,保证新旧世代都在任何网络调用前秒退、零 API 成本。
	claudeEffortProbeSentinel = "__aliang_probe__"
	claudeEffortProbeModel    = "__aliang_probe_model__"
	claudeEffortProbePrompt   = "probe"

	// maxProbedEffortLevels 解析出的 effort 清单条目上限,防异常输出撑爆负载。
	maxProbedEffortLevels = 12

	claudeVersionProbeTimeout = 2 * time.Second
	claudeEffortProbeTimeout  = 5 * time.Second
)

// globalEffortLadder 全局 effort 降级阶梯(高→低)。三仓共享的权威顺序:
// 请求档不被设备支持时,沿阶梯从请求档位置(含)向下取第一个支持的档;
// 全不支持返回空串(交还 CLI 默认)。
var globalEffortLadder = []string{"ultracode", "max", "xhigh", "high", "medium", "low"}

// staticEffortLevelsByCLI 无运行时探针的 CLI 用静态清单:
//   - codex:effort 经 <base>-<effort> 模型名后缀传达(见 applyCodexEffortSuffix),
//     传输层合法集即 endsWithKnownAgentEffortSuffix 的后缀表;
//   - opencode:effort 映射 --variant,沿用服务端目录三档。
var staticEffortLevelsByCLI = map[string][]string{
	"codex":    {"none", "minimal", "low", "medium", "high", "xhigh", "extrahigh", "max"},
	"opencode": {"low", "medium", "high"},
}

var (
	// 旧世代(≤2.1.156 一类)commander 硬拒格式:
	// error: option '--effort <level>' argument 'ultracode' is invalid. It must be one of: low, medium, high, xhigh, max
	effortLevelListRejectRe = regexp.MustCompile(`(?i)it must be one of:\s*([^\r\n]+)`)
	// 新世代(2.1.280 一代)警告格式(CLI 自行忽略并继续):
	// Warning: Unknown --effort value 'x' — ignoring it ... Valid values: low, medium, high, xhigh, max.
	effortLevelListWarnRe = regexp.MustCompile(`(?i)valid values:\s*([^\r\n.]+)`)
	// 版本号:x.y.z,适配 "2.1.280 (Claude Code)" / "codex-cli 0.144.5" / "1.18.4"。
	cliVersionRe = regexp.MustCompile(`(\d+\.\d+\.\d+)`)
)

// parseCLIEffortLevels 从 CLI 输出(报错或警告)中解析合法 effort 清单。
// 两种世代格式都认;无清单返回 nil。
func parseCLIEffortLevels(output string) []string {
	for _, re := range []*regexp.Regexp{effortLevelListRejectRe, effortLevelListWarnRe} {
		m := re.FindStringSubmatch(output)
		if len(m) < 2 {
			continue
		}
		levels := splitEffortLevelList(m[1])
		if len(levels) > 0 {
			return levels
		}
	}
	return nil
}

// splitEffortLevelList 把 "low, medium, high" 归一为小写去重清单,截断到上限。
func splitEffortLevelList(raw string) []string {
	seen := make(map[string]bool)
	levels := make([]string, 0, maxProbedEffortLevels)
	for _, part := range strings.Split(raw, ",") {
		level := strings.ToLower(strings.TrimSpace(part))
		if level == "" || seen[level] {
			continue
		}
		seen[level] = true
		levels = append(levels, level)
		if len(levels) >= maxProbedEffortLevels {
			break
		}
	}
	if len(levels) == 0 {
		return nil
	}
	return levels
}

// isAgentAIEffortRejected 判定 stderr 是否为「旧世代 CLI 硬拒 effort」失败,
// 并返回其声明的合法清单。新世代的警告格式不算被拒——那条路上 CLI 会继续运行。
func isAgentAIEffortRejected(stderr string) (bool, []string) {
	if !effortLevelListRejectRe.MatchString(stderr) {
		return false, nil
	}
	levels := parseCLIEffortLevels(stderr)
	if len(levels) == 0 {
		return false, nil
	}
	return true, levels
}

// parseCLIVersion 从 CLI 版本输出提取 x.y.z;解析失败返回空串。
func parseCLIVersion(output string) string {
	m := cliVersionRe.FindStringSubmatch(output)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// ---- 探针(带缓存) ----

type cliCapabilityProbe struct {
	version string
	levels  []string
	ok      bool
}

var (
	cliVersionProbeCache sync.Map
	claudeEffortCache    sync.Map
)

// probeCLIVersion 返回 CLI 的 x.y.z 版本号;失败返回空串。成功按二进制内容缓存。
func probeCLIVersion(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	cacheKey := executableProbeCacheKey(path)
	if cached, ok := cliVersionProbeCache.Load(cacheKey); ok {
		probe, _ := cached.(cliCapabilityProbe)
		return probe.version
	}
	ctx, cancel := context.WithTimeout(context.Background(), claudeVersionProbeTimeout)
	defer cancel()
	out, err := newBackgroundCommandContext(ctx, path, "--version").CombinedOutput()
	if err != nil {
		return ""
	}
	version := parseCLIVersion(string(out))
	if version == "" {
		return ""
	}
	cliVersionProbeCache.Store(cacheKey, cliCapabilityProbe{version: version, ok: true})
	return version
}

// probeClaudeEffortLevels 用哨兵 effort + 必然无效的模型名探 claude 支持的 effort
// 清单:旧世代 commander 在参数校验阶段硬拒并枚举合法值;新世代打印警告枚举合法值后
// 因无效模型名快退。两条路都不触网。返回 nil 表示探不到(未知,不设限)。
func probeClaudeEffortLevels(path string) []string {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	cacheKey := executableProbeCacheKey(path)
	if cached, ok := claudeEffortCache.Load(cacheKey); ok {
		probe, _ := cached.(cliCapabilityProbe)
		if !probe.ok {
			return nil
		}
		return probe.levels
	}
	ctx, cancel := context.WithTimeout(context.Background(), claudeEffortProbeTimeout)
	defer cancel()
	out, _ := newBackgroundCommandContext(ctx, path,
		"--print", "--model", claudeEffortProbeModel, "--effort", claudeEffortProbeSentinel, claudeEffortProbePrompt,
	).CombinedOutput()
	levels := parseCLIEffortLevels(string(out))
	if levels == nil {
		// 失败不缓存:下次快照重试(受 timeout 约束)。
		claudeEffortCache.Store(cacheKey, cliCapabilityProbe{ok: false})
		return nil
	}
	claudeEffortCache.Store(cacheKey, cliCapabilityProbe{levels: levels, ok: true})
	return levels
}

// agentToolEffortLevels 返回某 CLI 上报用的 effort 清单:claude 走运行时探针,
// 其余查静态表;未知 CLI 返回 nil(不设限)。
func agentToolEffortLevels(id, path string) []string {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "claude", "claudecode":
		return probeClaudeEffortLevels(path)
	case "codex":
		return append([]string(nil), staticEffortLevelsByCLI["codex"]...)
	case "opencode":
		return append([]string(nil), staticEffortLevelsByCLI["opencode"]...)
	default:
		return nil
	}
}

// clampEffortToSupported 把请求的 effort 沿全局阶梯降级到 supported 内:
// 请求为空或清单为空→原样;请求已支持→用清单里的原始拼写;否则从请求档(请求不在
// 阶梯上时从阶梯顶)向下取第一个支持的;全不支持→空串(交还 CLI 默认)。
func clampEffortToSupported(requested string, supported []string) string {
	requested = strings.TrimSpace(requested)
	if requested == "" || len(supported) == 0 {
		return requested
	}
	supportedSet := make(map[string]string, len(supported))
	for _, s := range supported {
		key := strings.ToLower(strings.TrimSpace(s))
		if key != "" {
			supportedSet[key] = s
		}
	}
	if original, ok := supportedSet[strings.ToLower(requested)]; ok {
		return original
	}
	start := 0
	for i, rung := range globalEffortLadder {
		if rung == strings.ToLower(requested) {
			start = i
			break
		}
	}
	for i := start; i < len(globalEffortLadder); i++ {
		if original, ok := supportedSet[globalEffortLadder[i]]; ok {
			return original
		}
	}
	return ""
}

// agentAIEffortRetryPlan 是 effort 被硬拒后的重试决策。
type agentAIEffortRetryPlan struct {
	retry       bool
	retryEffort string
}

// planAgentAIEffortRetry 由被拒时 CLI 声明的合法清单推导重试档:沿全局阶梯钳到
// 受支持的档(空串=去掉 --effort 交还 CLI 默认,同样算可重试);清单缺失或请求为
// 空时无法钳制,不重试,由调用方补发 ai.error。
func planAgentAIEffortRetry(requested string, levels []string) agentAIEffortRetryPlan {
	if len(levels) == 0 || strings.TrimSpace(requested) == "" {
		return agentAIEffortRetryPlan{}
	}
	clamped := clampEffortToSupported(requested, levels)
	return agentAIEffortRetryPlan{
		retry:       clamped != strings.TrimSpace(requested),
		retryEffort: clamped,
	}
}

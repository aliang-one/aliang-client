package services

import (
	"context"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// 本文件实现「设备 AI CLI 能力探测」:定位到的每个 CLI(claude/codex/opencode/pi/
// gemini)探测其版本与支持的 effort 档,随 detectAgentTools 的 tools 快照一并上报云端,
// 供服务端在派发时按设备能力自动降级 effort、手机端置灰不支持的档位。
//
// 探测遵循仓内既有约定(见 probeClaudeRawVersion / codexAppServerAvailable):
//   - sync.Map + executableProbeCacheKey 缓存,key 含符号链接解析+mtime+size,
//     二进制更新后自动失效,无需 TTL;并发首探用 singleflight 折叠;
//   - 失败负缓存(--version 按 cliVersionProbeFailureTTL 窗口,effort 按二进制
//     内容缓存直至更新),exec 用 newBackgroundCommandContext 而非用户 shell;
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
	// 极老世代(2026-01 的 claude 2.1.17 一类)根本没注册 --effort:
	// error: unknown option '--effort'
	// 这不是"值被拒"(没有合法清单可钳),而是"flag 不存在"(重试应去掉 flag)。
	// 2026-09-29 事故:agent 解析到 ~/.local/bin claude 2.1.17,--effort max 秒挂,
	// 探测与运行时两道防线都因不认这个格式而当成"未知不设限"放行。
	effortFlagUnknownRe = regexp.MustCompile(`(?i)unknown option '--effort'`)
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

// agentAIEffortRejection 描述运行时 stderr 的 effort 拒绝分类。
type agentAIEffortRejection struct {
	// rejected 表示 CLI 因 effort 参数本身失败(exit 1、无 assistant 输出)。
	rejected bool
	// flagUnsupported 表示 CLI 根本不认识 --effort 这个 flag(极老世代),
	// 此时没有合法清单,重试应直接去掉 --effort 而非降档。
	flagUnsupported bool
	// levels 是旧世代硬拒时 CLI 声明的合法档清单。
	levels []string
}

// isAgentAIEffortRejected 判定 stderr 是否为「CLI 拒绝 effort」失败。三种世代:
// 极老世代 flag 本身不存在(unknown option);旧世代硬拒值并枚举合法清单;
// 新世代只警告并继续——警告不算被拒,那条路上 CLI 会继续运行。
func isAgentAIEffortRejected(stderr string) agentAIEffortRejection {
	if effortFlagUnknownRe.MatchString(stderr) {
		return agentAIEffortRejection{rejected: true, flagUnsupported: true}
	}
	if !effortLevelListRejectRe.MatchString(stderr) {
		return agentAIEffortRejection{}
	}
	levels := parseCLIEffortLevels(stderr)
	if len(levels) == 0 {
		return agentAIEffortRejection{}
	}
	return agentAIEffortRejection{rejected: true, levels: levels}
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
	// flagUnsupported 表示该二进制不认识 --effort 这个 flag(极老世代),
	// 与 levels 互斥:flag 都没有时谈不上合法清单。
	flagUnsupported bool
	ok              bool
	// failedAt 是 --version 探测失败的负缓存时间戳;ok=true 时无意义。
	failedAt time.Time
}

var (
	cliVersionProbeCache sync.Map
	claudeEffortCache    sync.Map
	// 并发首探合并:CLI 升级后(缓存按二进制内容失效)多个并发回合会同时
	// 首探,singleflight 按 cacheKey 折叠成一次真实 spawn,避免惊群。
	cliVersionProbeGroup   singleflight.Group
	claudeEffortProbeGroup singleflight.Group
)

// cliVersionProbeFailureTTL 是 --version 探测失败的负缓存窗口:窗口内不重复
// spawn(探针会被 fallback lookup 与日志路径调用,持久失败的二进制每次重探最多
// 阻塞 2s),过期后自愈重探。
const cliVersionProbeFailureTTL = 5 * time.Minute

// probeCLIVersion 返回 CLI 的 x.y.z 版本号;失败返回空串。成功按二进制内容
// 永久缓存,失败按 cliVersionProbeFailureTTL 负缓存。
func probeCLIVersion(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	cacheKey := executableProbeCacheKey(path)
	if cached, ok := cliVersionProbeCache.Load(cacheKey); ok {
		probe, _ := cached.(cliCapabilityProbe)
		if probe.ok {
			return probe.version
		}
		if time.Since(probe.failedAt) < cliVersionProbeFailureTTL {
			return ""
		}
	}
	version, _, _ := cliVersionProbeGroup.Do(cacheKey, func() (interface{}, error) {
		ctx, cancel := context.WithTimeout(context.Background(), claudeVersionProbeTimeout)
		defer cancel()
		out, err := newBackgroundCommandContext(ctx, path, "--version").CombinedOutput()
		if err != nil {
			cliVersionProbeCache.Store(cacheKey, cliCapabilityProbe{failedAt: time.Now()})
			return "", nil
		}
		version := parseCLIVersion(string(out))
		if version == "" {
			cliVersionProbeCache.Store(cacheKey, cliCapabilityProbe{failedAt: time.Now()})
			return "", nil
		}
		cliVersionProbeCache.Store(cacheKey, cliCapabilityProbe{version: version, ok: true})
		return version, nil
	})
	return version.(string)
}

// cachedCLIVersion 只读窥视已缓存的版本(仅成功缓存),绝不 spawn——供
// ai.run.cli 日志行等 spawn 关键路径使用;未缓存返回空串。
func cachedCLIVersion(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if cached, ok := cliVersionProbeCache.Load(executableProbeCacheKey(path)); ok {
		probe, _ := cached.(cliCapabilityProbe)
		if probe.ok {
			return probe.version
		}
	}
	return ""
}

// probeClaudeEffortLevels 用哨兵 effort + 必然无效的模型名探 claude 支持的 effort
// 清单:旧世代 commander 在参数校验阶段硬拒并枚举合法值;新世代打印警告枚举合法值后
// 因无效模型名快退。两条路都不触网。返回 nil 表示探不到(未知,不设限)。
func probeClaudeEffortLevels(path string) []string {
	probe, ok := probeClaudeEffortCapability(path)
	if !ok || probe.flagUnsupported {
		return nil
	}
	return probe.levels
}

// probeClaudeEffortCapability 探测并归类该二进制的 effort 能力:
// 合法清单 / flag 本身不受支持(极老世代,unknown option)/ 未知。ok=false 表示
// 未知(探测失败,按二进制内容缓存为失败直至二进制更新);ok=true 且
// flagUnsupported 表示 flag 级缺失已被确证并缓存。
func probeClaudeEffortCapability(path string) (cliCapabilityProbe, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return cliCapabilityProbe{}, false
	}
	cacheKey := executableProbeCacheKey(path)
	if cached, ok := claudeEffortCache.Load(cacheKey); ok {
		probe, _ := cached.(cliCapabilityProbe)
		return probe, probe.ok
	}
	probe, _, _ := claudeEffortProbeGroup.Do(cacheKey, func() (interface{}, error) {
		// 并发加入同一次飞行后复查:另一调用方可能已落缓存。
		if cached, ok := claudeEffortCache.Load(cacheKey); ok {
			if probe, _ := cached.(cliCapabilityProbe); probe.ok {
				return probe, nil
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), claudeEffortProbeTimeout)
		defer cancel()
		// 探测隔离：哨兵参数只保证旧世代 CLI 秒退；新世代（2.1.280+）对非法
		// effort/model 只警告并继续，会真的跑一个模型回合并把 jsonl 写进
		// ~/.claude/projects，被 inventory 扫描上报成导入会话（2026-09-23
		// vibe-on-phone-75 事故）。把 CLAUDE_CONFIG_DIR 指向一次性目录，探测进程的
		// 一切会话痕迹都落在扫描树外，探测结束即清理。
		probeHome, probeErr := os.MkdirTemp("", "aliang-cli-probe-")
		if probeErr != nil {
			return preserveOrProbeFailure(cacheKey), nil
		}
		defer os.RemoveAll(probeHome)
		cmd := newBackgroundCommandContext(ctx, path,
			"--print", "--model", claudeEffortProbeModel, "--effort", claudeEffortProbeSentinel, claudeEffortProbePrompt,
		)
		cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+probeHome)
		out, _ := cmd.CombinedOutput()
		levels, flagUnsupported := classifyCLIEffortProbeOutput(string(out))
		switch {
		case len(levels) > 0:
			probe := cliCapabilityProbe{levels: levels, ok: true}
			claudeEffortCache.Store(cacheKey, probe)
			return probe, nil
		case flagUnsupported:
			probe := cliCapabilityProbe{flagUnsupported: true, ok: true}
			claudeEffortCache.Store(cacheKey, probe)
			return probe, nil
		}
		return preserveOrProbeFailure(cacheKey), nil
	})
	p, _ := probe.(cliCapabilityProbe)
	return p, p.ok
}

// preserveOrProbeFailure 探测失败时的落地:已有确证/运行时学习到的
// flagUnsupported 结论则原样保留——并发下探测可能晚于学习落地,失败标记不得
// 把它冲掉(冲掉的代价=该二进制多死一次 spawn);否则缓存 {ok:false},同一
// 二进制内容不再重复探测,二进制更新(缓存键含 mtime/size)后自动失效重探。
func preserveOrProbeFailure(cacheKey string) cliCapabilityProbe {
	if cached, ok := claudeEffortCache.Load(cacheKey); ok {
		if p, _ := cached.(cliCapabilityProbe); p.ok && p.flagUnsupported {
			return p
		}
	}
	failure := cliCapabilityProbe{ok: false}
	claudeEffortCache.Store(cacheKey, failure)
	return failure
}

// classifyCLIEffortProbeOutput 归类探测输出:合法清单 / flag 本身不受支持 / 未知。
func classifyCLIEffortProbeOutput(output string) (levels []string, flagUnsupported bool) {
	if levels = parseCLIEffortLevels(output); len(levels) > 0 {
		return levels, false
	}
	return nil, effortFlagUnknownRe.MatchString(output)
}

// claudeEffortFlagUnsupportedCached 只读缓存地判断该二进制是否已知不认 --effort
// (探针确证过,或运行时被拒学习过,见 rememberClaudeEffortFlagUnsupported)。
// 缓存未命中返回 false——spawn 路径绝不现场探测阻塞回合。
func claudeEffortFlagUnsupportedCached(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	if cached, ok := claudeEffortCache.Load(executableProbeCacheKey(path)); ok {
		probe, _ := cached.(cliCapabilityProbe)
		return probe.ok && probe.flagUnsupported
	}
	return false
}

// rememberClaudeEffortFlagUnsupported 把运行时抓到的 flag 级拒绝写回探测缓存:
// 第一个回合死在参数校验上,后续 spawn 凭这条记录直接去掉 --effort,不再重演。
func rememberClaudeEffortFlagUnsupported(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	claudeEffortCache.Store(executableProbeCacheKey(path), cliCapabilityProbe{flagUnsupported: true, ok: true})
}

// effortDropForBinary 返回 spawn 前实际下发的 effort:该二进制已知不认 --effort
// 时返回空串(交还 CLI 默认),避免每个回合都先死一次再靠重试自愈。
func effortDropForBinary(effort, cliPath string) string {
	if strings.TrimSpace(effort) != "" && claudeEffortFlagUnsupportedCached(cliPath) {
		return ""
	}
	return effort
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

// planAgentAIEffortRetry 由拒绝分类推导重试决策:
//   - flag 本身不受支持:降档无意义,去掉 --effort 重试;
//   - 旧世代硬拒:沿全局阶梯钳到受支持的档(空串=去掉 --effort 交还 CLI 默认,
//     同样算可重试);
//   - 未被拒或请求为空:不重试,由调用方补发 ai.error。
func planAgentAIEffortRetry(requested string, rej agentAIEffortRejection) agentAIEffortRetryPlan {
	if !rej.rejected || strings.TrimSpace(requested) == "" {
		return agentAIEffortRetryPlan{}
	}
	if rej.flagUnsupported {
		return agentAIEffortRetryPlan{retry: true, retryEffort: ""}
	}
	clamped := clampEffortToSupported(requested, rej.levels)
	return agentAIEffortRetryPlan{
		retry:       clamped != strings.TrimSpace(requested),
		retryEffort: clamped,
	}
}

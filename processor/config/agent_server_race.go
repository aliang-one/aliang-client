package config

// agent 侧多通道竞速(2026-11-21 terminal.aliang.one 四线竞速的 agent 侧移植):
// 候选来自 core.aliangServer.agent_servers(数组;旧单值 core.agent_server /
// aliangServer.agent_server 自动成为首个候选)。周期探测(真实 GET
// {base}/healthz,3s 超时,2 样本均值)取最快;30% 滞回护现任(挑战者须快
// ≥30% 才夺位);每轮打一条 [AGENT-SERVER-RACE] 观测日志。语义对齐
// terminal-web/src/ws(channelProber/channelSelector/useWsChannel)。
//
// 生效机制:竞速胜者写入进程内 override,AgentBaseURL() 优先读它——
// 所有 agent→server 流量(注册/sync/transfer/mesh/远端 WS 拨号)天然跟随,
// 远端 WS 每次重拨重建 URL,切换零额外接线。
//
// 候选 <2 时竞速自动禁用(存量设备零行为变化);全部探败保持现任粘滞
// (last-known-good),不自动回退默认值。v1 仅探测驱动:真实请求的传输层
// 失败不参与翻转(callAgentServer 已有 per-endpoint 退避,职责分离)。

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aliang.one/nursorgate/common/logger"
)

const (
	// agentRaceProbeTimeoutMs 单样本超时(与 terminal-web PROBE_TIMEOUT_MS 同语义)。
	agentRaceProbeTimeoutMs = 3000
	// agentRaceSamples 每轮每候选样本数(串行,取非空均值)。
	agentRaceSamples = 2
	// agentRaceIntervalMs 重探周期(与 useWsChannel REPROBE_INTERVAL_MS 同语义)。
	agentRaceIntervalMs = 10 * 60 * 1000
	// agentRaceHysteresisPct 现任滞回百分比:挑战者须 ≤ 现任的 70% 才夺位。
	agentRaceHysteresisPct = 70
	// agentRaceProbePath 探测路径:轻量端点(407B 文本)。
	agentRaceProbePath = "/healthz"
)

// agentServerOverride 竞速胜者(进程内原子态);空串=未竞速,走配置原值。
var agentServerOverride atomic.Value

// SetAgentServerOverride 供竞速器写胜者;测试亦可注入后清理。
func SetAgentServerOverride(base string) { agentServerOverride.Store(strings.TrimRight(strings.TrimSpace(base), "/")) }

// AgentServerOverride 返回当前胜者(未竞速返回空串)。
func AgentServerOverride() string {
	v, _ := agentServerOverride.Load().(string)
	return v
}

// AgentServerCandidates 解析候选表:aliangServer.agent_servers 数组优先,
// 缺省回落单值(agent_server / aliangServer.agent_server / 默认值)。
// 保序去重;竞速需要 ≥2 条,调用方自行判断。
func (c *Config) AgentServerCandidates() []string {
	var raw []string
	if c != nil && c.Core != nil && c.Core.AliangServer != nil && len(c.Core.AliangServer.AgentServers) > 0 {
		raw = c.Core.AliangServer.AgentServers
	} else if base := c.AgentBaseURL(); strings.TrimSpace(base) != "" {
		raw = []string{base}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(raw))
	for _, u := range raw {
		u = strings.TrimRight(strings.TrimSpace(u), "/")
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	return out
}

// pickAgentServer 竞速裁决:最快者胜;30% 滞回护现任(现任探败则滞回不适用,
// 直接让位);全部探败返回空串(调用方保持现任粘滞)。probes 里 ttfbMs<0 = 探败。
func pickAgentServer(probes []agentRaceProbe, incumbent string) string {
	type okProbe struct {
		base   string
		ttfbMs int64
	}
	var ok []okProbe
	incumbentTTFB := int64(-1)
	for _, p := range probes {
		if p.ttfbMs < 0 {
			if p.base == incumbent {
				incumbentTTFB = -1 // 现任本轮探败:滞回失效
			}
			continue
		}
		ok = append(ok, okProbe{base: p.base, ttfbMs: p.ttfbMs})
		if p.base == incumbent {
			incumbentTTFB = p.ttfbMs
		}
	}
	if len(ok) == 0 {
		return ""
	}
	best := ok[0]
	for _, p := range ok[1:] {
		if p.ttfbMs < best.ttfbMs {
			best = p
		}
	}
	if incumbent != "" && incumbentTTFB >= 0 && best.base != incumbent {
		// 30% 滞回:挑战者未快过现任的 70% → 现任保持(防横跳)。
		if best.ttfbMs > incumbentTTFB*int64(agentRaceHysteresisPct)/100 {
			return incumbent
		}
	}
	return best.base
}

type agentRaceProbe struct {
	base   string
	ttfbMs int64 // -1 = 探败
}

// agentRaceNewClient 每次探测新建 client:DisableKeepAlives 强制每样本走全新
// TCP+TLS 握手(与网页端全握手探测同语义,避免样本2复用连接偏乐观);
// CheckRedirect 禁跟随(3xx 计探败,不给重定向目标投票)。
func agentRaceNewClient() *http.Client {
	return &http.Client{
		Timeout: time.Duration(agentRaceProbeTimeoutMs) * time.Millisecond,
		Transport: &http.Transport{DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// probeAgentServer 对候选基址做真实 GET {base}/healthz,samples 次串行取非空均值;
// 全败返回 -1。样本=开始→响应首字节(DNS/TCP/TLS/服务端一竿到底)。
func probeAgentServer(base string, samples int) int64 {
	if samples <= 0 {
		samples = agentRaceSamples
	}
	client := agentRaceNewClient()
	var total, hits int64
	for i := 0; i < samples; i++ {
		start := time.Now()
		resp, err := client.Get(strings.TrimRight(base, "/") + agentRaceProbePath)
		if err != nil {
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		// 只认 2xx:3xx(重定向被禁跟随)/4xx(停放页/误配 404)一律探败,
		// 防止把流量选到不能服务 /api 的主机(审计 #10)。
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			continue
		}
		total += time.Since(start).Milliseconds()
		hits++
	}
	if hits == 0 {
		return -1
	}
	return total / hits
}

// StartAgentServerRacer 启动竞速循环(进程级单飞;候选<2 时空转 30s 轮询配置,
// 支持配置热加载后自动启用)。ctx 取消即退出。
func StartAgentServerRacer() {
	go agentServerRaceLoop()
}

var (
	agentServerRaceOnce         sync.Once
	agentServerDisabledLogged   atomic.Bool
)

// StartAgentServerRacerOnce 进程级幂等入口(NewAgentService 装配调用)。
func StartAgentServerRacerOnce() {
	agentServerRaceOnce.Do(func() {
		StartAgentServerRacer()
	})
}

func agentServerRaceLoop() {
	for {
		cfg := GetGlobalConfig()
		candidates := cfg.AgentServerCandidates()
		if len(candidates) < 2 {
			// 停摆态也必须清 stale override(审计 #2):运维删数组回退单通道后,
			// 旧胜者若不清,AgentBaseURL 永远指旧地址,配置改动零生效。
			if stale := AgentServerOverride(); stale != "" {
				SetAgentServerOverride("")
				logger.Info(fmt.Sprintf("[AGENT-SERVER-RACE] disabled (candidates<2), cleared stale override=%s", stale))
			} else if !agentServerDisabledLogged.Swap(false) {
				logger.Info("[AGENT-SERVER-RACE] disabled (candidates<2), racing off")
			}
			agentServerDisabledLogged.Store(true)
			time.Sleep(30 * time.Second)
			continue
		}
		agentServerDisabledLogged.Store(false)
		incumbent := AgentServerOverride()
		if incumbent == "" {
			// 首轮:现任=配置实际基座(AgentBaseURL 在 override 为空时返回配置
			// 解析值——它才是此刻流量真正在走的线;候选含它则滞回有据,
			// 不含则首轮按「无现任」裁决)。此前误种 candidates[0],实测
			// (liang-dev 首轮 kept)导致胜者永不落 override、流量原地踏步。
			incumbent = cfg.AgentBaseURL()
		}
		// 并行探测(与网页端 Promise.all 同语义;串行时单个黑洞候选拖满
		// 6s×N 会把整轮拖到数十秒——审计 #12)。
		probes := make([]agentRaceProbe, len(candidates))
		var wg sync.WaitGroup
		for i, base := range candidates {
			wg.Add(1)
			go func(i int, base string) {
				defer wg.Done()
				probes[i] = agentRaceProbe{base: base, ttfbMs: probeAgentServer(base, agentRaceSamples)}
			}(i, base)
		}
		wg.Wait()
		winner := pickAgentServer(probes, incumbent)
		ttfbLog := make([]string, 0, len(probes))
		for _, p := range probes {
			if p.ttfbMs < 0 {
				ttfbLog = append(ttfbLog, fmt.Sprintf("%s=fail", p.base))
			} else {
				ttfbLog = append(ttfbLog, fmt.Sprintf("%s=%dms", p.base, p.ttfbMs))
			}
		}
		if winner == "" {
			// 全败:保持现任粘滞(last-known-good),仅记日志。
			logger.Info(fmt.Sprintf("[AGENT-SERVER-RACE] all candidates failed, keeping incumbent=%s probes=%v", incumbent, ttfbLog))
		} else {
			if AgentServerOverride() != winner {
				// kept 分支也必须落 override:首轮 incumbent(配置基座)≠胜者时,
				// override 为空,不写则 AgentBaseURL 永远回配置值(liang-dev 实测教训)。
				SetAgentServerOverride(winner)
				logger.Info(fmt.Sprintf("[AGENT-SERVER-RACE] active=%s ttfb=%v (switched from %s)", winner, ttfbLog, incumbent))
			} else {
				logger.Info(fmt.Sprintf("[AGENT-SERVER-RACE] active=%s ttfb=%v (incumbent kept)", winner, ttfbLog))
			}
		}
		time.Sleep(time.Duration(agentRaceIntervalMs) * time.Millisecond)
	}
}

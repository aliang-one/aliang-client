package services

import (
	"sync"
	"time"
)

// agentVibeDigestHelloMinInterval 限制「指纹变更」触发的全量 hello 最小间隔。
//
// 背景:vibe digest 每 10s 对 recent-N 转录文件做 path+mtime+size 指纹,活跃
// 机器上几乎每个 tick 都在变——2026-10-29 生产实测 3367 次 hello/天(其中
// 2496 次因 vibe_digest_change),server 端对每次 hello 扇出 ai.session.detail
// 刷新,反复 upsert 上百条消息把 ai_messages 写路径拖入 PG 死锁。
//
// 取 30s:变更推送从 ~10s 退到 ≥30s,单设备从 6 次/分 降到 ≤2 次/分,窗口内
// 连续变更合并为一次;标题/运行态类低频变化仍在一个窗口内可见,60s periodic
// 兜底不变。connect/periodic/rename_event/server_requested 等其他 reason 的
// hello 不经过本限频,照常立即发送。
const agentVibeDigestHelloMinInterval = 30 * time.Second

// agentRemoteHelloGate 串行化全量 agent.hello 的变更推送节奏。
//
// 状态挂在 AgentService 上、跨重连存活。sendAgentHello 是所有 reason
// (connect/periodic/rename_event/server_requested/vibe_digest_change)的
// 单一咽喉:任何成功 hello 都会经 noteSent 刷新「已发送指纹基线 + 时间窗」,
// 因此 rename/periodic 后不会紧接着再发一次重复的 digest hello,断线重连后
// 首个变更也不会被陈旧时间窗压制。
type agentRemoteHelloGate struct {
	mu sync.Mutex
	// lastSentDigest 是最近一次成功 hello 所覆盖的指纹。空串=尚无基线
	// (进程刚起/connect 时 home 为空),首个 digest tick 只记录基线。
	lastSentDigest string
	// lastHelloAt 是最近一次成功 hello(任意 reason)的时刻;零值=没发过,
	// 此时限频不生效。
	lastHelloAt time.Time
	// deferred 标记「指纹已变但被限频挂起」。仅用于跨 tick 的去重告警日志,
	// 不参与放行判定:放行与否只看 lastSentDigest != digest + 时间窗。
	deferred bool
}

// baseline 在 digest tick 首次见到指纹时记录基线(connect hello 应已携带该
// 状态)。返回 true 表示本次仅是基线记录,tick 不应发送。
func (g *agentRemoteHelloGate) baseline(digest string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.lastSentDigest == "" {
		g.lastSentDigest = digest
		return true
	}
	return false
}

// unchanged 报告 digest 是否与最近一次成功 hello 覆盖的指纹一致;一致时顺带
// 清除挂起标记(内容翻回去掉,如 mtime 抖动回退)。
func (g *agentRemoteHelloGate) unchanged(digest string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if digest == g.lastSentDigest {
		g.deferred = false
		return true
	}
	return false
}

// allow 在「指纹已变」时判定是否放行。now 由调用方注入以便测试;窗口未到则
// 记挂起并返回 firstDefer=true(仅首次,供调用方打一条去重日志)。
func (g *agentRemoteHelloGate) allow(digest string, now time.Time, minInterval time.Duration) (ok, firstDefer bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.lastHelloAt.IsZero() && now.Sub(g.lastHelloAt) < minInterval {
		firstDefer = !g.deferred
		g.deferred = true
		return false, firstDefer
	}
	return true, false
}

// deferredChanged 供外部查询当前是否处于挂起态(测试与观测用)。
func (g *agentRemoteHelloGate) deferredChanged() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.deferred
}

// noteSent 在任意 reason 的全量 hello 成功写出后刷新基线与时间窗。digest 取
// 发送时刻的即时指纹(空串=该环境无指纹可算,保留旧基线但时间窗照常刷新)。
// 与 tick 侧先后竞争造成的极小错位(hello 写出后~指纹重算前落盘的变更)最多
// 让该变更等到下个变更或 60s periodic 兜底,有界且可接受。
func (g *agentRemoteHelloGate) noteSent(digest string, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if digest != "" {
		g.lastSentDigest = digest
	}
	g.lastHelloAt = now
	g.deferred = false
}

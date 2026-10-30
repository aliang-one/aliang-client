package services

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// 基准时刻:所有用例共享同一 t0,通过偏移构造「窗口内/窗口边界」场景,
// 不依赖真实时间睡眠。
var gateT0 = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func TestAgentRemoteHelloGateBatchesDigestChanges(t *testing.T) {
	g := &agentRemoteHelloGate{}
	min := agentVibeDigestHelloMinInterval // 30s

	// 任意 reason 的成功 hello(connect/periodic)刷新基线与时间窗。
	g.noteSent("A", gateT0)
	assert.True(t, g.unchanged("A"), "与最近一次成功 hello 同指纹应判 unchanged")

	// t0+5s 指纹翻转为 B:窗口内 → 挂起,且是首次挂起(供去重日志)。
	ok, firstDefer := g.allow("B", gateT0.Add(5*time.Second), min)
	assert.False(t, ok, "30s 窗口内的 digest 变更必须合批挂起")
	assert.True(t, firstDefer, "首次挂起要打一次日志")

	// t0+15s 指纹继续变到 C:合批语义,不重置、不重复报日志。
	ok, firstDefer = g.allow("C", gateT0.Add(15*time.Second), min)
	assert.False(t, ok)
	assert.False(t, firstDefer, "连续挂起不得重复打日志")

	// t0+30s(恰好到达窗口边界):放行最新指纹 D。
	ok, _ = g.allow("D", gateT0.Add(30*time.Second), min)
	assert.True(t, ok, "≥30s 后再变应放行")

	// 发送成功后基线推进,同指纹判 unchanged。
	g.noteSent("D", gateT0.Add(30*time.Second))
	assert.True(t, g.unchanged("D"))
	assert.False(t, g.deferredChanged(), "放行发送后挂起标记应清除")
}

func TestAgentRemoteHelloGatePeriodicSuppressesFollowup(t *testing.T) {
	g := &agentRemoteHelloGate{}
	min := agentVibeDigestHelloMinInterval

	// periodic hello 携带了当前状态,发送后 30s 内指纹再变也应合批,
	// 不得紧跟着重复发一次 digest hello。
	g.noteSent("A", gateT0)
	ok, _ := g.allow("B", gateT0.Add(10*time.Second), min)
	assert.False(t, ok, "periodic hello 后 30s 内的 digest 变更必须等窗口")
	ok, _ = g.allow("B", gateT0.Add(30*time.Second), min)
	assert.True(t, ok)
}

func TestAgentRemoteHelloGateUnchangedClearsDeferred(t *testing.T) {
	g := &agentRemoteHelloGate{}
	g.noteSent("A", gateT0)
	_, _ = g.allow("B", gateT0.Add(1*time.Second), agentVibeDigestHelloMinInterval)
	assert.True(t, g.deferredChanged())

	// 内容又翻回最近已发送的指纹(如 mtime 抖动回退):挂起标记应清除,
	// 后续不产生「仍挂起」的误导日志。
	assert.True(t, g.unchanged("A"))
	assert.False(t, g.deferredChanged())
}

func TestAgentRemoteHelloGateEmptyDigestKeepsBaseline(t *testing.T) {
	g := &agentRemoteHelloGate{}
	g.noteSent("A", gateT0)

	// 空指纹(无 home/无会话)不得抹掉基线,但时间窗仍要刷新——
	// 那次 hello 是真实发出的。
	g.noteSent("", gateT0.Add(1*time.Second))
	assert.True(t, g.unchanged("A"))
	assert.False(t, g.unchanged("B"), "基线 A 不应被空指纹覆盖")
	ok, _ := g.allow("B", gateT0.Add(5*time.Second), agentVibeDigestHelloMinInterval)
	assert.False(t, ok, "空指纹 hello 的时间窗仍需生效")
}

func TestAgentRemoteHelloGateReconnectNotThrottled(t *testing.T) {
	g := &agentRemoteHelloGate{}
	// 上一次 hello 在 2 小时前(断线期间):重连后首个 digest 变更立即放行,
	// 不被旧窗口压制。
	g.noteSent("A", gateT0.Add(-2*time.Hour))
	ok, _ := g.allow("B", gateT0, agentVibeDigestHelloMinInterval)
	assert.True(t, ok, "断线重连后的首个 digest 变更不应被旧 hello 时间窗压制")
}

package services

import (
	"testing"
)

// 2026-11-07 S1 实时性:create-only 会话不在六类磁盘指纹面,digest 永远不变,
// hello 只能等 60s 周期。create 纳管(registerAISessionLocked)必须置内存脏标,
// digest tick 消费一次放行即时 hello(仍受 30s 最小间隔门约束)。
func TestAgentVibeMemoryDirtyFlagRoundTrip(t *testing.T) {
	consumeAgentVibeMemorySnapshotDirty() // 清测试残留
	if consumeAgentVibeMemorySnapshotDirty() {
		t.Fatalf("dirty flag should be false initially")
	}
	markAgentVibeMemorySnapshotDirty()
	if !consumeAgentVibeMemorySnapshotDirty() {
		t.Fatalf("dirty flag should be true after mark")
	}
	if consumeAgentVibeMemorySnapshotDirty() {
		t.Fatalf("dirty flag should be consumed once")
	}
}

func TestRegisterAISessionMarksMemorySnapshotDirty(t *testing.T) {
	consumeAgentVibeMemorySnapshotDirty()
	manager := newAgentAIManager()
	manager.mu.Lock()
	manager.registerAISessionLocked(&agentAISession{id: "local-dirty", provider: "agentcore"})
	manager.mu.Unlock()
	if !consumeAgentVibeMemorySnapshotDirty() {
		t.Fatalf("registerAISessionLocked must mark memory snapshot dirty")
	}
}

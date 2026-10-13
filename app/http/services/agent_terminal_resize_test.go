package services

import (
	"sync/atomic"
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

// 2026-10-13 卡顿定位 P0-B: server 的 resize 合并闸之后仍可能送来与当前
// 几何一致的 terminal.resize(竞态/旧帧);再打 PTY ioctl 就是白送的
// SIGWINCH→TUI 全屏重绘放大器。本组用例钉死 agent 侧行为:
//   同值(含归一化后同值) → 只回执 resized,不碰 PTY;
//   不同值 → 照常 resize 并更新记录几何。
func TestAgentTerminalResizeDedupesSameGeometry(t *testing.T) {
	m := newAgentTerminalManager()
	coll, write := newPayloadCollector()

	var resizeCalls int32
	live := newAttachTestSession("t-dedup", newTerminalRingBuffer(1<<20))
	live.resizer = func(rows, cols int) error {
		atomic.AddInt32(&resizeCalls, 1)
		return nil
	}
	m.mu.Lock()
	m.sessions["t-dedup"] = live
	m.mu.Unlock()

	resizeAcks := func() int {
		return len(coll.ofTypes(models.AgentEventTerminalResized))
	}

	// 1. 不同值 → resizer 恰好一次 + resized 回执 + 记录几何更新
	m.resize(map[string]interface{}{
		"type":       models.AgentEventTerminalResize,
		"session_id": "t-dedup",
		"rows":       40,
		"cols":       120,
	}, write)
	if got := atomic.LoadInt32(&resizeCalls); got != 1 {
		t.Fatalf("resizeCalls = %d, want 1 (不同值必须真实 resize)", got)
	}
	m.mu.Lock()
	rows, cols := live.rows, live.cols
	m.mu.Unlock()
	if rows != 40 || cols != 120 {
		t.Fatalf("recorded geometry = %dx%d, want 40x120", rows, cols)
	}
	if resizeAcks() != 1 {
		t.Fatalf("resized acks = %d, want 1", resizeAcks())
	}

	// 2. 同值 → resizer 不再被调用,但仍回执 resized(协议兼容)
	m.resize(map[string]interface{}{
		"type":       models.AgentEventTerminalResize,
		"session_id": "t-dedup",
		"rows":       40,
		"cols":       120,
	}, write)
	if got := atomic.LoadInt32(&resizeCalls); got != 1 {
		t.Fatalf("resizeCalls = %d, want 1 (同值不得再打 PTY)", got)
	}
	if got := resizeAcks(); got != 2 {
		t.Fatalf("resized acks = %d, want 2 (同值也必须回执)", got)
	}

	// 3. 真实变化到归一化上限,再送越界值 → 归一化后同值,同样去重
	m.resize(map[string]interface{}{
		"type":       models.AgentEventTerminalResize,
		"session_id": "t-dedup",
		"rows":       500,
		"cols":       500,
	}, write)
	if got := atomic.LoadInt32(&resizeCalls); got != 2 {
		t.Fatalf("resizeCalls = %d, want 2", got)
	}
	m.resize(map[string]interface{}{
		"type":       models.AgentEventTerminalResize,
		"session_id": "t-dedup",
		"rows":       9999, // 越界 → 归一回 500,与当前几何一致
		"cols":       9999,
	}, write)
	if got := atomic.LoadInt32(&resizeCalls); got != 2 {
		t.Fatalf("resizeCalls = %d, want 2 (归一化同值不得再打 PTY)", got)
	}
	if got := resizeAcks(); got != 4 {
		t.Fatalf("resized acks = %d, want 4 (每次 resize 都必须回执)", got)
	}
}

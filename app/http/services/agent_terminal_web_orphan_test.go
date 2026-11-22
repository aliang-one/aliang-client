package services

import (
	"io"
	"sync/atomic"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

// shrinkWebOrphanDelay points the web-leg orphan delay at a tiny test value and
// restores it when the test ends. Plain var (process-global), so no t.Parallel.
func shrinkWebOrphanDelay(t *testing.T, d time.Duration) {
	t.Helper()
	orig := agentWebOrphanCloseDelay
	agentWebOrphanCloseDelay = d
	t.Cleanup(func() { agentWebOrphanCloseDelay = orig })
}

// TestWebOrphanCloseKillsAfterDelay 连接退出调度的孤儿到期即杀。占坑根因:
// PTY 空闲无输出→写失败探测永不触发→旧死 WS 会话占 agentMaxTerminalSessions
// (全局 4 坑,手机共享)直到 30m idle——刷新恢复壳的并发 create 全撞上限。
func TestWebOrphanCloseKillsAfterDelay(t *testing.T) {
	shrinkWebOrphanDelay(t, 50*time.Millisecond)
	m := newAgentTerminalManager()
	live := newAttachTestSession("t-orphan", newTerminalRingBuffer(4096))
	kills := killRecorder(live)
	m.mu.Lock()
	m.sessions["t-orphan"] = live
	m.mu.Unlock()

	m.ScheduleWebOrphanClose("t-orphan")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(kills) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt32(kills); got == 0 {
		t.Fatalf("orphan reap must kill the session after the delay (kills=%d)", got)
	}
	m.mu.Lock()
	_, timerStill := m.orphanTimers["t-orphan"]
	m.mu.Unlock()
	if timerStill {
		t.Fatalf("fired timer entry must be dropped from orphanTimers")
	}
}

// TestWebOrphanCloseCancelLeavesSession 取消(显式 close 出册路径)后到期不杀:
// 会话走正常 close,孤儿回收不得误触。
func TestWebOrphanCloseCancelLeavesSession(t *testing.T) {
	shrinkWebOrphanDelay(t, 50*time.Millisecond)
	m := newAgentTerminalManager()
	live := newAttachTestSession("t-orphan-cancel", newTerminalRingBuffer(4096))
	kills := killRecorder(live)
	m.mu.Lock()
	m.sessions["t-orphan-cancel"] = live
	m.mu.Unlock()

	m.ScheduleWebOrphanClose("t-orphan-cancel")
	m.cancelWebOrphanClose("t-orphan-cancel")

	time.Sleep(3 * agentWebOrphanCloseDelay)
	if got := atomic.LoadInt32(kills); got != 0 {
		t.Fatalf("cancelled orphan reap must not kill, kills=%d", got)
	}
	m.mu.Lock()
	_, timerStill := m.orphanTimers["t-orphan-cancel"]
	m.mu.Unlock()
	if timerStill {
		t.Fatalf("cancelled timer entry must be dropped")
	}
}

// TestWebOrphanTimerDroppedOnFreshCreate 同 ID 新建落地即清残留孤儿定时条目,
// 斩断「timer 回调删条目→get」与「create 注册会话」交错时回调误杀新会话的
// 竞态(create 在同锁内直清,不等回调)。
func TestWebOrphanTimerDroppedOnFreshCreate(t *testing.T) {
	m := newAgentTerminalManager()
	// 前会话已不在册(被回收),但孤儿定时条目因回调竞态尚未落地删除的场景。
	// 手工放一个永不触发的假条目(真 timer 到期即删,这里用长延迟模拟未到期)。
	m.mu.Lock()
	m.orphanTimers["t-fresh"] = time.AfterFunc(time.Hour, func() {})
	m.mu.Unlock()
	t.Cleanup(func() {
		m.mu.Lock()
		if old := m.orphanTimers["t-fresh"]; old != nil {
			old.Stop()
		}
		m.mu.Unlock()
	})

	m.startProcess = func(sessionID string, shell string, cwd string, rows int, cols int) (*agentTerminalSession, []io.Reader, error) {
		s := newAttachTestSession(sessionID, newTerminalRingBuffer(4096))
		s.shell = shell
		s.cwd = cwd
		return s, nil, nil
	}

	writeJSON := func(payload interface{}) error { return nil }
	m.create(map[string]interface{}{
		"type":       models.AgentEventTerminalCreate,
		"session_id": "t-fresh",
	}, writeJSON)

	m.mu.Lock()
	_, timerStill := m.orphanTimers["t-fresh"]
	_, sessionLive := m.sessions["t-fresh"]
	m.mu.Unlock()
	if timerStill {
		t.Fatalf("fresh create must drop the stale orphan timer entry")
	}
	if !sessionLive {
		t.Fatalf("fresh create must register the new session")
	}
}

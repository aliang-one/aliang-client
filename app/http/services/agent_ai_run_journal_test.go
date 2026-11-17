package services

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"aliang.one/nursorgate/app/http/models"
)

// 这组测试锁死 run 终态账本的两个契约（审计 2026-11-17）：
//
//  1. 持久化失败绝不能拦下帧本身——emit 早退=终态帧对全端静默丢失，健康连接
//     数天不重连=云端永卡 running。正确形态：内存账已落（重连重放兜底），帧
//     照常上行，持久化降级成响亮日志。
//  2. ack 清账带 seq 防回退：accepted_seq 低于终态 seq 不得清账（防旧 ack 掉新账）。
func TestEmitStillWritesTerminalWhenPersistFails(t *testing.T) {
	orig := agentAIRunJournalPathFn
	agentAIRunJournalPathFn = func() (string, error) {
		return "", errors.New("simulated ENOSPC")
	}
	t.Cleanup(func() { agentAIRunJournalPathFn = orig })

	m := newAgentAIManager()
	m.runJournalEnabled = true

	var written []map[string]interface{}
	write := agentTerminalWriter(func(v interface{}) error {
		if p, ok := v.(map[string]interface{}); ok {
			written = append(written, p)
		}
		return nil
	})
	run := agentAIRun{sessionID: "sess-persist-fail", runID: "run-persist-fail", messageID: "msg-persist-fail"}
	e := m.runEmitter(run, write)

	payload := map[string]interface{}{"type": models.AgentEventAIDone, "status": "success"}
	if err := e.emit(payload); err != nil {
		t.Fatalf("持久化失败时 emit 不应失败: %v", err)
	}
	if len(written) == 0 {
		t.Fatal("终态帧必须在持久化失败时仍然写线——早退等于对全端静默丢失")
	}

	m.mu.Lock()
	_, inMemory := m.pendingTerminals["run-persist-fail"]
	m.mu.Unlock()
	if !inMemory {
		t.Fatal("内存账应保留（重连重放是持久化失败后的唯一兜底）")
	}
}

func TestAcknowledgePendingTerminalKeepsOldAck(t *testing.T) {
	// 清账路径要求 persist 成功（persist 失败时实现会故意回滚内存清账，防 crash
	// 后复活——那是另一条已锁死的契约）。这里给可写的临时文件走成功路径。
	orig := agentAIRunJournalPathFn
	agentAIRunJournalPathFn = func() (string, error) {
		return t.TempDir() + "/ai_run_terminals.json", nil
	}
	t.Cleanup(func() { agentAIRunJournalPathFn = orig })

	m := newAgentAIManager()
	m.runJournalEnabled = true
	payload := map[string]interface{}{"type": models.AgentEventAIDone, "run_id": "run-seq", "event_seq": 7}
	if err := m.rememberPendingTerminal(payload); err != nil {
		t.Fatalf("remember 不应失败: %v", err)
	}

	// 旧 ack（seq=3 < 7）不得清账。
	m.acknowledgePendingTerminal("run-seq", 3)
	m.mu.Lock()
	_, kept := m.pendingTerminals["run-seq"]
	m.mu.Unlock()
	if !kept {
		t.Fatal("accepted_seq < terminal_seq 的旧 ack 不得清账")
	}

	// 达到或超过终态 seq 的 ack 才清。
	m.acknowledgePendingTerminal("run-seq", 7)
	m.mu.Lock()
	_, kept = m.pendingTerminals["run-seq"]
	m.mu.Unlock()
	if kept {
		t.Fatal("accepted_seq >= terminal_seq 的 ack 应清账")
	}
}

// TestDiscardPendingTerminal：服务端拒收（终局裁决）必须强制清账——否则 seq 防
// 回退守卫把毒帧永久留在账上，每 10s 心跳全量重放永不收敛（2026-11-17 审计 P2）。
func TestDiscardPendingTerminal(t *testing.T) {
	orig := agentAIRunJournalPathFn
	agentAIRunJournalPathFn = func() (string, error) {
		return t.TempDir() + "/ai_run_terminals.json", nil
	}
	t.Cleanup(func() { agentAIRunJournalPathFn = orig })

	m := newAgentAIManager()
	m.runJournalEnabled = true
	payload := map[string]interface{}{"type": models.AgentEventAIDone, "run_id": "run-poison", "event_seq": 3}
	if err := m.rememberPendingTerminal(payload); err != nil {
		t.Fatalf("remember 不应失败: %v", err)
	}

	// 空 run_id 无害短路。
	m.discardPendingTerminal("", "rejected")

	m.discardPendingTerminal("run-poison", "rejected")
	m.mu.Lock()
	_, kept := m.pendingTerminals["run-poison"]
	m.mu.Unlock()
	if kept {
		t.Fatal("拒收后账目必须清空——重放无出路")
	}
}

// TestTruncateForCloudRuneSafe（2026-11-17 审计 P2）：字节下标裸切会把多字节
// rune 劈成非法 UTF-8 尾巴（json.Marshal 消毒成 U+FFFD=截点花屏）。锁死：
// 截断结果恒为合法 UTF-8，且恰在 8KiB 边界的多字节字符不被劈开。
func TestTruncateForCloudRuneSafe(t *testing.T) {
	// CJK 三字节 rune 铺满并跨越 8192 边界。
	value := strings.Repeat("中", 4000) // 12000 字节
	out := truncateForCloud(value)
	if !utf8.ValidString(out) {
		t.Fatal("截断结果含非法 UTF-8")
	}
	if !strings.HasSuffix(out, "…<truncated>") {
		t.Fatalf("应带截断标记: %q", out[len(out)-20:])
	}
	if got := len([]rune(strings.TrimSuffix(out, "…<truncated>"))); got != 2730 {
		// 8192/3 = 2730.67 → 2730 个完整 rune（8190 字节），回退 2 字节。
		t.Fatalf("rune 数 = %d, want 2730（按 rune 边界回退）", got)
	}

	// 恰好卡边：8191 字节 ASCII + 一个 3 字节 rune。
	edge := strings.Repeat("a", 8191) + "中"
	out = truncateForCloud(edge)
	if !utf8.ValidString(out) {
		t.Fatal("边界截断含非法 UTF-8")
	}
	if !strings.HasSuffix(out, "…<truncated>") {
		t.Fatalf("边界值应被截断并标记: %q", out[len(out)-20:])
	}

	// 未超限原样返回。
	if got := truncateForCloud("短文本"); got != "短文本" {
		t.Fatalf("未超限应原样返回: %q", got)
	}
}

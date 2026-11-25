package services

import (
	"sync/atomic"
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

// 2026-11-24 几何失偶修复(server geometryReconcile.ts 配对):attachLive 拽回
// PTY 后必须回执 terminal.resized。生产实证:attach 拽回若落在网页首次 fit
// resize 之后,server 记录(A=网页几何)与 PTY 实际(B=拽回几何)两端失偶,而
// 合并闸的同值去重又把网页此后重发同值 A 的修复 resize 永久吞掉——网页上
// 「使用本浏览器分辨率」点多少次都无效。回执是 server 对账的唯一事实源。
func TestAgentTerminalAttachLiveEmitsResizedAck(t *testing.T) {
	m := newAgentTerminalManager()
	coll, write := newPayloadCollector()

	var resizeCalls int32
	live := newAttachTestSession("t-attach-ack", newTerminalRingBuffer(1<<20))
	live.resizer = func(rows, cols int) error {
		atomic.AddInt32(&resizeCalls, 1)
		return nil
	}
	m.mu.Lock()
	m.sessions["t-attach-ack"] = live
	m.mu.Unlock()

	// attach 到 live 会话(PTY 46x21 记录,attach 载荷 40x120——网页授权
	// 后 server 以存储几何重驱 attach 的真实形状)。
	if !m.attachLive("t-attach-ack", 40, 120, "", write, struct{}{}) {
		t.Fatalf("attachLive = false, want true")
	}
	if got := atomic.LoadInt32(&resizeCalls); got != 1 {
		t.Fatalf("resizeCalls = %d, want 1 (attach 拽回必须真实 resize PTY)", got)
	}

	// created 与 resized 两帧都必须在场;resized 是 server 对账触发器。
	created := coll.ofTypes(models.AgentEventTerminalCreated)
	if len(created) != 1 {
		t.Fatalf("created frames = %d, want 1", len(created))
	}
	resized := coll.ofTypes(models.AgentEventTerminalResized)
	if len(resized) != 1 {
		t.Fatalf("resized acks = %d, want 1 (attach 拽回必须回执几何对账帧)", len(resized))
	}
	ack := resized[0]
	if ack["session_id"] != "t-attach-ack" {
		t.Fatalf("ack session_id = %v, want t-attach-ack", ack["session_id"])
	}
	if ack["rows"] != 40 || ack["cols"] != 120 {
		t.Fatalf("ack geometry = %vx%v, want 40x120 (必须回执 PTY 实际几何)", ack["cols"], ack["rows"])
	}
	m.mu.Lock()
	rows, cols := live.rows, live.cols
	m.mu.Unlock()
	if rows != 40 || cols != 120 {
		t.Fatalf("recorded geometry = %dx%d, want 40x120", rows, cols)
	}

	// 顺序不变量:resized 必须晚于 created(网页端把 created 视为 attach
	// 完成;对账广播若早于回放确认抵达会造成显示闪烁)。
	if iCreated, iResized := coll.indexOf(func(p map[string]interface{}) bool {
		return p["type"] == models.AgentEventTerminalCreated
	}), coll.indexOf(func(p map[string]interface{}) bool {
		return p["type"] == models.AgentEventTerminalResized
	}); iCreated == -1 || iResized == -1 || iResized < iCreated {
		t.Fatalf("resized (idx=%d) must come after created (idx=%d)", iResized, iCreated)
	}
}

// pipe fallback(无 resizer)时几何本来就未变,但同样回执——server 只信任
// resized 帧做对账,缺回执会退回用 attach 载荷盲信几何(旧协议)。
func TestAgentTerminalAttachLiveEmitsResizedAckWithoutResizer(t *testing.T) {
	m := newAgentTerminalManager()
	coll, write := newPayloadCollector()

	live := newAttachTestSession("t-attach-pipe", newTerminalRingBuffer(1<<20))
	live.resizer = nil // pipe fallback
	m.mu.Lock()
	m.sessions["t-attach-pipe"] = live
	m.mu.Unlock()

	if !m.attachLive("t-attach-pipe", 40, 120, "", write, struct{}{}) {
		t.Fatalf("attachLive = false, want true")
	}
	m.mu.Lock()
	rows, cols := live.rows, live.cols
	m.mu.Unlock()
	if rows != 24 || cols != 80 {
		t.Fatalf("recorded geometry = %dx%d, want 24x80 (无 resizer 不得改几何)", cols, rows)
	}
	resized := coll.ofTypes(models.AgentEventTerminalResized)
	if len(resized) != 1 {
		t.Fatalf("resized acks = %d, want 1 (pipe fallback 也要回执当前几何)", len(resized))
	}
	if ack := resized[0]; ack["rows"] != 24 || ack["cols"] != 80 {
		t.Fatalf("ack geometry = %vx%v, want 24x80", ack["cols"], ack["rows"])
	}
}

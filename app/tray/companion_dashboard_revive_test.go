//go:build !windows

package tray

import (
	"testing"
	"time"

	"aliang.one/nursorgate/internal/ipc"
)

func TestDashboardReviveGate_AllowThenCooldown(t *testing.T) {
	var g dashboardReviveGate
	now := time.Now()

	// 零值=从未补发,立即放行。
	if !g.allow(now) {
		t.Fatal("zero-value gate must allow immediately")
	}
	g.mark(now)

	// 冷却窗内一律拒绝(窗口边界前 1ms)。
	if g.allow(now.Add(dashboardReviveCooldown - time.Millisecond)) {
		t.Fatal("gate must stay closed within cooldown window")
	}
	// 冷却窗满放行。
	if !g.allow(now.Add(dashboardReviveCooldown)) {
		t.Fatal("gate must reopen after cooldown window")
	}
}

func TestMaybeReviveDashboard_Guards(t *testing.T) {
	// 守卫路径(http_enabled 缺席/为 true)在触碰 ipcClient 之前返回——
	// 用零值 ipcClient 断言不 panic、不标记 gate。
	a := &CompanionApp{httpURL: "http://127.0.0.1:56431"}
	now := time.Now()

	a.maybeReviveDashboard(map[string]interface{}{"is_running": true}, now)
	if !a.dashReviveGate.allow(now.Add(time.Second)) {
		t.Fatal("absent http_enabled must not mark the gate")
	}

	a.maybeReviveDashboard(map[string]interface{}{"http_enabled": true}, now)
	if !a.dashReviveGate.allow(now.Add(time.Second)) {
		t.Fatal("http_enabled=true must not mark the gate")
	}

	// http_enabled=false 走补发路径:ipcClient 为 nil 会 panic,构造一个
	// 真客户端(dial/HTTP 回退在测试环境失败被 maybeReviveDashboard 吞掉,
	// 只标 gate)。
	a.ipcClient = ipc.NewClient()
	a.maybeReviveDashboard(map[string]interface{}{"http_enabled": false}, now)
	if a.dashReviveGate.allow(now.Add(time.Second)) {
		t.Fatal("http_enabled=false must mark the gate (attempt recorded)")
	}
}

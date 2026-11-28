//go:build !windows

package tray

import (
	"fmt"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/internal/ipc"
)

// dashboardReviveCooldown 限制补发 start_http 的节奏(get_status 2s 一轮,
// 冷却窗防 core 长期不可用时形成发送风暴)。
const dashboardReviveCooldown = 30 * time.Second

// dashboardReviveGate 是「core 可达但 dashboard 未起」补发的纯决策器:零值=
// 从未补发立即放行;mark 记录一次尝试(无论成败),冷却窗内一律拒绝。
// dashboard 恢复后无需 reset——下一轮 http_enabled=true 直接短路,冷却窗
// 自然过期放行应对下一次掉线。与 reconcileGate 同款纯类型 + 单测风格。
type dashboardReviveGate struct {
	lastAttempt time.Time
}

func (g *dashboardReviveGate) allow(now time.Time) bool {
	return g.lastAttempt.IsZero() || now.Sub(g.lastAttempt) >= dashboardReviveCooldown
}

func (g *dashboardReviveGate) mark(now time.Time) { g.lastAttempt = now }

// maybeReviveDashboard 在 syncState 周期里探测「core 可达但其 dashboard 未起」
// 并补发 start_http。背景:companion 只在自身启动时发一次 start_http,core
// 换代(服务重启/崩溃自愈)后无人重发,macOS 的 core 又刻意不自起 dashboard
// (TrayApp 进程内宿主防双宿主)——2026-11-16 Windows 事故的同族缺口,Windows
// 已由 core 侧自起修复,macOS 走本补发路径。http_enabled 缺席(旧 core)不
// 动作;补发成败都交给下一轮 get_status 收敛。
func (a *CompanionApp) maybeReviveDashboard(status map[string]interface{}, now time.Time) {
	httpEnabled, present := status["http_enabled"].(bool)
	if !present || httpEnabled {
		return
	}
	if !a.dashReviveGate.allow(now) {
		return
	}
	a.dashReviveGate.mark(now)
	resp, err := a.ipcClient.Send(ipc.ActionStartHTTP, nil)
	if err != nil {
		logger.Debug(fmt.Sprintf("dashboard revive start_http failed: %v", err))
		return
	}
	if !resp.OK {
		logger.Debug(fmt.Sprintf("dashboard revive start_http rejected: %s", resp.Error))
		return
	}
	a.applyDashboardStartResponse(resp)
	logger.Info(fmt.Sprintf("Dashboard revived via start_http at %s", a.getHTTPURL()))
}

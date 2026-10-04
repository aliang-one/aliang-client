package tray

import (
	"fmt"
	"strings"
	"time"

	"aliang.one/nursorgate/app/agentruntime"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/internal/ipc"
)

// user-agent 看护节奏。写成常量方便按实测调整；不引入配置项，避免给 core 配置面
// 再加一个几乎不需要动的字段。
const (
	agentWatchdogInterval      = 10 * time.Second // 探活周期
	agentWatchdogFailThreshold = 3                // 连续失败到此次数才 EnsureStarted（≈30s 容错）
	agentWatchdogProbeTimeout  = 2 * time.Second  // 单次探活超时

	// auth-reconcile 失败后的冷却窗。白名单扩到 refresh_invalid 等粘性禁用态
	// 后,core 自己也没有有效会话时 reconcile 会持续失败——若每个探活 tick 都
	// 重试就是 10s 一次的失败风暴;成功后 needsSync 翻转前也靠它防重复触发。
	agentWatchdogReconcileRetryInterval = 5 * time.Minute
)

// reconcileGate 限制 auth-reconcile 的重试节奏:零值=从未尝试,立即放行;
// mark 记录一次尝试(无论成败),之后冷却窗内一律拒绝;reset 只在探测确认
// 状态已不再需要同步时调用,恢复立即放行。
type reconcileGate struct {
	lastAttempt time.Time
}

func (g *reconcileGate) allow(now time.Time) bool {
	return g.lastAttempt.IsZero() || now.Sub(g.lastAttempt) >= agentWatchdogReconcileRetryInterval
}

func (g *reconcileGate) mark(now time.Time) { g.lastAttempt = now }

func (g *reconcileGate) reset() { g.lastAttempt = time.Time{} }

// agentWatchdogLoop 周期探 user-agent 健康，连续失败超阈值则 EnsureStarted 拉起。
// 它只负责节奏与防抖：检测复用 agentruntime.SupportsCurrentAgentAPI，拉起复用
// agentruntime.EnsureStarted（已含进程内 mutex + flock/pidfile 防护，跨进程单实例）。
//
// 覆盖「两次 auth-success 之间 user-agent 中途崩溃 / 被误杀，且没有新登录事件触发
// EnsureStarted」的场景。退出跟随 companion 的 a.done（companion 退出时 loop 停）。
//
// darwin/linux 与 windows 的 CompanionApp 同名，本方法在两平台编译时自动附加到各自的类型。
func (a *CompanionApp) agentWatchdogLoop() {
	ticker := time.NewTicker(agentWatchdogInterval)
	defer ticker.Stop()

	consecutiveFails := 0
	var reconciles reconcileGate
	for {
		select {
		case <-a.done:
			return
		case <-ticker.C:
			if agentruntime.SupportsCurrentAgentAPI(agentWatchdogProbeTimeout) {
				if consecutiveFails > 0 {
					logger.Info(fmt.Sprintf("[AGENT-WATCHDOG] recovered after %d failures", consecutiveFails))
				}
				consecutiveFails = 0
				needsSync := agentruntime.NeedsAuthenticatedSync(agentWatchdogProbeTimeout)
				if !needsSync {
					reconciles.reset()
					continue
				}
				// 首次立即 reconcile;之后无论成败都冷却——失败等 core 侧会话
				// 恢复,成功等 agent 状态翻转(下一 tick 的 !needsSync 分支才会
				// reset)。原来的「失败即下个 tick 重试」在白名单扩容后会变成
				// 10s 一次的失败风暴。
				if !reconciles.allow(time.Now()) {
					continue
				}
				reconciles.mark(time.Now())
				logger.Warn("[AGENT-WATCHDOG] healthy process has stale auth-disabled state, requesting core reconciliation")
				if err := a.syncAgentAuthFromCore("watchdog_auth_reconcile"); err != nil {
					logger.Warn(fmt.Sprintf("[AGENT-WATCHDOG] auth_reconcile_failed (will retry after %s): %v", agentWatchdogReconcileRetryInterval, err))
				}
				continue
			}
			// 探活失败说明 agent 进程本身出问题了;此前若刚做过 reconcile,
			// 冷却作废——拉起后的首个健康 tick 应立即重新评估同步需求。
			reconciles.reset()
			consecutiveFails++
			logger.Warn(fmt.Sprintf("[AGENT-WATCHDOG] health_check failed (%d/%d)", consecutiveFails, agentWatchdogFailThreshold))
			if consecutiveFails < agentWatchdogFailThreshold {
				continue
			}
			logger.Warn("[AGENT-WATCHDOG] threshold_reached, calling EnsureStarted")
			if err := agentruntime.EnsureStarted(); err != nil {
				logger.Warn(fmt.Sprintf("[AGENT-WATCHDOG] ensure_failed (will retry next cycle): %v", err))
			} else if err := a.syncAgentAuthFromCore("watchdog_agent_restarted"); err != nil {
				logger.Warn(fmt.Sprintf("[AGENT-WATCHDOG] auth_sync_failed (will retry next cycle): %v", err))
			}
			// 无论 EnsureStarted 成败都复位计数：
			// 成功 → agent 已起，下一周期确认健康；
			// 失败 → 避免每个 tick 都触发 spawn，降为每 (interval×threshold) ≈30s 重试一次，防抖动。
			consecutiveFails = 0
		}
	}
}

func (a *CompanionApp) syncAgentAuthFromCore(reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "agent_started"
	}
	resp, err := a.ipcClient.Send(ipc.ActionSyncAgentAuth, ipc.SyncAgentAuthArgs{Reason: reason})
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("core rejected agent auth sync: %s", resp.Error)
	}
	return nil
}

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
)

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
				continue
			}
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

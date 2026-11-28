package services

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"aliang.one/nursorgate/common/logger"
)

type agentAIActivity struct {
	lastActivityAt atomic.Int64
	// lastProgressAt tracks MEANINGFUL progress (assistant text/thinking via
	// emitAIDelta, file changes via fileSink, a completed Codex work item) —
	// NOT raw output. A degenerate model loop (e.g. filler `echo` tool calls
	// every few seconds for hours) keeps lastActivityAt fresh, so only the
	// progress clock can distinguish "alive" from "getting anywhere".
	lastProgressAt atomic.Int64
	// pendingApprovals 是并发的审批请求数（0=无人在等）。用计数而不是 bool，
	// 是因为一个回合可能有多个审批重叠：A、B 同时在等时 A 先结束，B 仍需要
	// 保住审批钟与 idlePaused——bool 会被 A 的 false 提前清掉，随后
	// pendingToolUses>0 又把工具钟臂起，合法的人工等待会被工具天花板误杀。
	pendingApprovals atomic.Int64
	pendingToolUses  atomic.Int64
	// approvalWaitStartedAt 是当前这段「等人工决策」的开始时刻（0=不在等）。
	// 计数版下它不保证与 toolWaitStartedAt 互斥：第一个审批开启时清工具钟，
	// 但期间若工具重新臂起（或第二个重叠审批不清工具钟），两钟可同时非零；
	// 这不误判，因为 staleDeathReason 的 awaiting 分支优先于工具钟分支。
	// 审批等待由 agentAIApprovalTimeout 单独约束，stale 扫描器也要能读到它。
	approvalWaitStartedAt atomic.Int64
	// toolWaitStartedAt is the UnixNano instant the current CONTIGUOUS
	// tool/subagent wait began (0 = not waiting). The tool-wait watchdog uses
	// it to bound hung tools/subagents that would otherwise pin the run
	// indefinitely (the hard ceiling no longer counts wait ticks) while the
	// progress heartbeat keeps telling the server the run is alive. Cleared
	// while a human approval is pending so approval time never counts against
	// the ceiling (approvals are bounded separately by
	// agentAIApprovalTimeout).
	toolWaitStartedAt atomic.Int64
	// activeSubagents counts in-flight subagent records (task_started …
	// terminal frame / teardown). While >0 the idle and no-progress guards are
	// paused (idlePaused) and agentAISubagentWaitCeiling bounds the contiguous
	// stretch instead — subagent work is legitimately silent on the parent's
	// stdout (sync mode: no frames between launch and completion).
	activeSubagents       atomic.Int64
	subagentWaitStartedAt atomic.Int64
	runStart              time.Time
	killReasonMu          sync.RWMutex
	killReason            string
}

func newAgentAIActivity() *agentAIActivity {
	a := &agentAIActivity{runStart: time.Now()}
	a.lastActivityAt.Store(a.runStart.UnixNano())
	a.lastProgressAt.Store(a.runStart.UnixNano())
	return a
}

func (a *agentAIActivity) bump() {
	if a == nil {
		return
	}
	a.lastActivityAt.Store(time.Now().UnixNano())
}

// markProgress records meaningful progress and counts as activity. Callers:
// emitAIDelta (assistant text/thinking), the claude/agentcore thinking stream
// in emitClaudeStructuredEvents (内核把 thinking_delta 透传成帧后，纯推理期间
// 这是服务层唯一活性信号——L3 stale 扫描器 5 分钟就判死), the run's fileSink
// (file written or edited), and the Codex item/completed handler (one work
// unit finished).

func (a *agentAIActivity) markProgress() {
	if a == nil {
		return
	}
	now := time.Now().UnixNano()
	a.lastActivityAt.Store(now)
	a.lastProgressAt.Store(now)
}

// progressIdleFor is how long the run has produced NO meaningful progress.
// Unlike idleFor it is NOT reset by raw output — a run can be loudly alive
// while going nowhere.

func (a *agentAIActivity) progressIdleFor() time.Duration {
	if a == nil {
		return 0
	}
	last := a.lastProgressAt.Load()
	if last == 0 {
		return 0
	}
	return time.Since(time.Unix(0, last))
}

// setAwaitingApproval 登记/解除一个审批等待，并 bump 活动时间，让审批结束时
// 拿到一个新的 idle 窗口。
//
// 用计数而非 bool：一个回合可能同时有多个审批（A、B 重叠）。只有第一个审批
// 才起审批钟/清工具钟（后续重叠审批不能把人工等待上限往后推）；只有最后一个
// 审批结束才清钟，并在工具仍在飞时重新臂起工具钟。

func (a *agentAIActivity) setAwaitingApproval(v bool) {
	if a == nil {
		return
	}
	if v {
		if a.pendingApprovals.Add(1) == 1 {
			// Approval time is human time, not tool time: pause the tool-wait
			// clock so a slow decision never trips the tool-wait ceiling.
			a.approvalWaitStartedAt.Store(time.Now().UnixNano())
			a.toolWaitStartedAt.Store(0)
		}
	} else {
		// CAS 递减且不允许减到负数：多余的 false（重复解除）在 0 处直接 break。
		for {
			current := a.pendingApprovals.Load()
			if current <= 0 {
				// 多余 false 本身无害，但计数失衡（例如漏了 true 却发 false）
				// 会让 awaiting() 长期为真，把该 run 拖到 24h 审批天花板，
				// 所以要留痕。agentAIActivity 无会话身份，日志不带 id。
				logger.Warn("[ai.activity] approval_release_without_pending")
				break
			}
			if a.pendingApprovals.CompareAndSwap(current, current-1) {
				if current-1 == 0 {
					a.approvalWaitStartedAt.Store(0)
					if a.pendingToolUses.Load() > 0 {
						// Decision resolved but the tool is still in flight:
						// re-arm with a fresh ceiling window for the remaining
						// execution.
						a.toolWaitStartedAt.Store(time.Now().UnixNano())
					}
				}
				break
			}
		}
	}
	a.bump()
}

// approvalWaitFor 报告当前这段审批等待已持续多久，0 = 不在等待。

func (a *agentAIActivity) approvalWaitFor() time.Duration {
	if a == nil {
		return 0
	}
	ts := a.approvalWaitStartedAt.Load()
	if ts == 0 {
		return 0
	}
	return time.Since(time.Unix(0, ts))
}

func (a *agentAIActivity) awaiting() bool {
	if a == nil {
		return false
	}
	return a.pendingApprovals.Load() > 0
}

// toolWaitFor reports how long the current contiguous tool/subagent wait has
// been in flight, excluding approval-pending time. 0 when not waiting.

func (a *agentAIActivity) toolWaitFor() time.Duration {
	if a == nil {
		return 0
	}
	ts := a.toolWaitStartedAt.Load()
	if ts == 0 {
		return 0
	}
	return time.Since(time.Unix(0, ts))
}

// beginToolUseWait marks that Claude has emitted a tool_use (Task/subagent,
// Bash, etc.) and the run is legitimately silent until a matching tool_result
// arrives. It bumps activity so the run gets a fresh idle window once the tool
// completes.

func (a *agentAIActivity) beginToolUseWait() {
	if a == nil {
		return
	}
	if a.pendingToolUses.Add(1) == 1 {
		a.toolWaitStartedAt.Store(time.Now().UnixNano())
	}
	a.bump()
}

func (a *agentAIActivity) endToolUseWait() {
	if a == nil {
		return
	}
	for {
		current := a.pendingToolUses.Load()
		if current <= 0 {
			break
		}
		if a.pendingToolUses.CompareAndSwap(current, current-1) {
			if current-1 == 0 {
				a.toolWaitStartedAt.Store(0)
			}
			break
		}
	}
	a.bump()
}

func (a *agentAIActivity) idlePaused() bool {
	if a == nil {
		return false
	}
	return a.awaiting() || a.pendingToolUses.Load() > 0 || a.activeSubagents.Load() > 0
}

// staleDeathReason 判定这个 run 是否已经「无响应死」，返回原因码与结论。
//
// 口径（spec §3.3.1/§3.3.2）：
//   - 审批等待态：只服 approvalTimeout，超过即判死。工具/子代理天花板在这里
//     不参与评估——进入审批时工具钟已被清零（审批是人的时间，不是工具时间），
//     所以「审批 + 子代理同时活跃」的收敛上界是 approvalTimeout。
//   - 工具/子代理等待态（非审批）：不按 stale 阈值判死，只服各自天花板——
//     toolCeiling、subagentCeiling。
//   - 非等待态：lastActivityAt 距今超过 staleThreshold → stalled。activity 只由
//     真实事件帧/审批帧/steer ack 刷新，ai.run.progress 心跳不参与（否则心跳
//     会把一切判死判据击穿）。

func (a *agentAIActivity) staleDeathReason(staleThreshold, approvalTimeout, toolCeiling, subagentCeiling time.Duration) (string, bool) {
	if a == nil {
		return "", false
	}
	if a.awaiting() {
		if approvalTimeout > 0 && a.approvalWaitFor() > approvalTimeout {
			return "approval_timeout", true
		}
		return "", false
	}
	if a.pendingToolUses.Load() > 0 || a.activeSubagents.Load() > 0 {
		if toolCeiling > 0 && a.toolWaitFor() > toolCeiling {
			return "tool_wait_timeout", true
		}
		if subagentCeiling > 0 && a.subagentWaitFor() > subagentCeiling {
			return "subagent_wait_timeout", true
		}
		return "", false
	}
	if staleThreshold > 0 && a.idleFor() > staleThreshold {
		return "stalled", true
	}
	return "", false
}

// beginSubagent marks that a subagent record went active (task_started). While
// the count is >0 the idle/no-progress guards are paused and the subagent-wait
// ceiling clock is armed.

func (a *agentAIActivity) beginSubagent() {
	if a == nil {
		return
	}
	if a.activeSubagents.Add(1) == 1 {
		a.subagentWaitStartedAt.Store(time.Now().UnixNano())
	}
	a.bump()
}

// endSubagent marks a subagent record terminal. The wait clock clears when the
// last active record closes.

func (a *agentAIActivity) endSubagent() {
	if a == nil {
		return
	}
	for {
		current := a.activeSubagents.Load()
		if current <= 0 {
			break
		}
		if a.activeSubagents.CompareAndSwap(current, current-1) {
			if current-1 == 0 {
				a.subagentWaitStartedAt.Store(0)
			}
			break
		}
	}
	a.bump()
}

// subagentWaitFor reports how long >=1 subagent has been continuously active.

func (a *agentAIActivity) subagentWaitFor() time.Duration {
	if a == nil {
		return 0
	}
	ts := a.subagentWaitStartedAt.Load()
	if ts == 0 {
		return 0
	}
	return time.Since(time.Unix(0, ts))
}

// subagentProgress feeds every watchdog clock from a task_progress frame: raw
// activity (idle guard), meaningful progress (no-progress guard), and — the
// mis-kill fix — a RESET of the contiguous tool-wait clock while a tool wait is
// pending. On the sync phone path the parent Agent tool_result only arrives at
// completion, so pendingToolUses stays armed for the whole subagent run and
// without this reset a >1h subagent dies as "suspected hung tool" despite
// emitting regular progress.

func (a *agentAIActivity) subagentProgress() {
	if a == nil {
		return
	}
	now := time.Now().UnixNano()
	a.lastActivityAt.Store(now)
	a.lastProgressAt.Store(now)
	if a.pendingToolUses.Load() > 0 {
		a.toolWaitStartedAt.Store(now)
	}
}

func (a *agentAIActivity) idleFor() time.Duration {
	if a == nil {
		return 0
	}
	last := a.lastActivityAt.Load()
	if last == 0 {
		return 0
	}
	return time.Since(time.Unix(0, last))
}

// setKillReason 采用先因优先（first-write-wins）：判死原因只在为空时落账。
//
// 为什么：判死入口有多条互相竞速的路径——watchdog 五条天花板、用户主动停止
// （stop→guard.kill）、内核停滞回调（coreSteer.onStalled 的 no_guard 分支）——
// 先触发 cancel 的那条才是真实死因。旧的「后来者覆盖」语义会让收尾窗内的
// user_stop 把 watchdog 已落账的 idle_timeout 洗成 user_stop（或反向），终态
// 归因与 [ai.run.kill] 日志随之失真。需要覆盖权的唯一调用方是 forceFinalize
// （判死兜底诊断），它显式走 forceSetKillReason。
func (a *agentAIActivity) setKillReason(reason string) {
	if a == nil {
		return
	}
	a.killReasonMu.Lock()
	if a.killReason == "" { // 先因优先：多路判死竞速，首个死因留账
		a.killReason = reason
	}
	a.killReasonMu.Unlock()
}

// forceSetKillReason 仅 forceFinalize（判死兜底诊断）使用：宽限期满仍未收敛
// 时，兜底判定原因覆盖先因——此刻唯一可信的结论是「按 reason 判死且 run 没
// 能在宽限内自然返回」，终态归因必须跟着它走。
func (a *agentAIActivity) forceSetKillReason(reason string) {
	if a == nil {
		return
	}
	a.killReasonMu.Lock()
	a.killReason = reason
	a.killReasonMu.Unlock()
}

func (a *agentAIActivity) killReasonOr(fallback string) string {
	if a == nil {
		return fallback
	}
	a.killReasonMu.RLock()
	reason := a.killReason
	a.killReasonMu.RUnlock()
	if reason != "" {
		return reason
	}
	return fallback
}

// startAIWatchdog cancels ctx when the run goes idle (no output for
// agentAIIdleWindow and not awaiting approval/tool results), produces no
// meaningful progress for agentAINoProgressWindow, a contiguous tool/subagent
// wait outlives agentAIToolWaitCeiling, or its engine-active time exceeds
// agentAIHardCeiling. It exits as soon as ctx is done, so it never outlives
// the run.

func (m *agentAIManager) startAIWatchdog(ctx context.Context, activity *agentAIActivity, cancel context.CancelFunc) {
	if activity == nil || cancel == nil {
		return
	}
	go agentAIWatchdogLoop(ctx, activity, m.wrapKillCancel(activity, cancel), agentAIIdleWindow, agentAIHardCeiling, agentAINoProgressWindow, agentAIToolWaitCeiling, agentAISubagentWaitCeiling, agentAIIdleCheckInterval)
}

// agentAIWatchdogLoop is the parameterized watchdog body, split out so tests can
// drive it with tiny windows instead of the multi-minute production defaults.

func agentAIWatchdogLoop(ctx context.Context, activity *agentAIActivity, cancel context.CancelFunc, idleWindow, hardCeiling, noProgressWindow, toolWaitCeiling, subagentCeiling, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// 引擎活跃时钟：逐拍累加，但处于任一合法等待态（审批/工具/子代理）的拍
	// 不计入。口径见 spec §3.2 第 4 条——等待态各自有独立天花板，硬顶只管真实
	// 干活时长，否则 3h 的子代理批处理会被 2h 硬顶误杀。按 ticker 粒度采样，
	// 误差以一拍为界（生产 agentAIIdleCheckInterval 为 30s）。
	lastTick := time.Now()
	var engineActive time.Duration
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			delta := now.Sub(lastTick)
			lastTick = now
			if !activity.idlePaused() {
				engineActive += delta
			}
			if !activity.idlePaused() && activity.idleFor() > idleWindow {
				activity.setKillReason("idle_timeout")
				cancel()
				return
			}
			// No-progress kill: the run keeps emitting output (so the idle
			// watchdog above never fires) but has produced no assistant text,
			// no file changes, and no completed work item for the whole window
			// — the observed degenerate-loop failure mode. Skipped while a
			// tool/approval wait is pending: a single long-lived Task/subagent
			// or human decision is work in flight, not looping.
			if noProgressWindow > 0 && !activity.idlePaused() && activity.progressIdleFor() > noProgressWindow {
				activity.setKillReason("no_progress")
				cancel()
				return
			}
			// Tool-wait ceiling: a contiguous tool/subagent wait (approval
			// time excluded — the clock is cleared while a human decision is
			// pending) that outlives the ceiling means the tool or subagent
			// is hung. Every other guard is paused during such a wait by
			// design, and the ai.run.progress heartbeat keeps feeding the
			// server-side stale-run sweeper, so without this bound a hung
			// subagent pins the run indefinitely while the phone
			// shows "running". The awaiting() re-check hardens against a
			// theoretical stale-armed clock (a scheduler preemption inside
			// beginToolUseWait's Add→Store window landing exactly as an
			// approval hook fires): approval waits are bounded separately by
			// agentAIApprovalTimeout, so skipping the kill while a human
			// decision is pending is always safe.
			if toolWaitCeiling > 0 && !activity.awaiting() && activity.toolWaitFor() > toolWaitCeiling {
				activity.setKillReason("tool_wait_timeout")
				cancel()
				return
			}
			// Subagent-wait ceiling: a contiguous stretch with >=1 active
			// subagent that outlives this bound means runaway subagent work.
			// task_progress resets the tool-wait clock above (progress = not
			// hung) but never this one, so an endlessly-progressing subagent is
			// still bounded. Old CC (no task_* frames) leaves the count at zero
			// and this guard stays inert.
			if subagentCeiling > 0 && activity.subagentWaitFor() > subagentCeiling {
				activity.setKillReason("subagent_wait_timeout")
				cancel()
				return
			}
			if hardCeiling > 0 && engineActive > hardCeiling {
				activity.setKillReason("hard_ceiling")
				cancel()
				return
			}
		}
	}
}

// agentAIRunStoppedStatus derives the terminal status/error for a run whose ctx
// was cancelled, from the watchdog kill reason (falling back to the output
// limiter or a plain stop).

func agentAIRunStoppedStatus(activity *agentAIActivity, limiter *agentAIOutputLimiter) (status string, errMsg string) {
	switch activity.killReasonOr("") {
	case "idle_timeout":
		return "idle_timeout", fmt.Sprintf("AI run went idle (no output for %s) and was stopped", agentAIIdleWindow)
	case "no_progress":
		// Same server-mapped status as idle_timeout (→ timed_out / error) so
		// the backend treats the run as terminally dead; only the message
		// differs, naming the real cause.
		return "idle_timeout", fmt.Sprintf("AI run produced no meaningful progress (no text or file changes) for %s and was stopped", agentAINoProgressWindow)
	case "tool_wait_timeout":
		return "timeout", fmt.Sprintf("AI run tool/subagent wait exceeded %s without a tool result (suspected hung tool) and was stopped", agentAIToolWaitCeiling)
	case "subagent_wait_timeout":
		return "timeout", fmt.Sprintf("AI run subagent work exceeded %s without completing (suspected runaway subagent) and was stopped", agentAISubagentWaitCeiling)
	case "hard_ceiling":
		return "hard_timeout", fmt.Sprintf("AI run exceeded the maximum engine-active runtime %s (approval, tool and subagent waits are excluded)", agentAIHardCeiling)
	}
	if limiter != nil && limiter.Exceeded() {
		return "output_limited", fmt.Sprintf("AI output exceeded rate limit (%d bytes per %s) or lifetime cap (%d bytes)", agentAIOutputRateBytes, agentAIOutputRateWindow, agentAIOutputCapBytes)
	}
	return "stopped", ""
}

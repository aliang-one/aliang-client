package services

import (
	"context"
	"fmt"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
)

// agentAIKillGrace 是 cancel 之后等待 run 自然退出的宽限期。超过它仍未返回，
// guardian 强制收尾（发终态 + 会话复位），僵尸后续帧由 emitter 终态闸丢弃。
//
//	ALIANG_AI_KILL_GRACE  (time.ParseDuration). Default 30s.
var agentAIKillGrace = resolveEnvDuration("ALIANG_AI_KILL_GRACE", 30*time.Second)

// agentAIRunGuard 是一个运行中回合的收尾守卫。它把「kill → 宽限 → 强拆」
// 串成一条带观测的链路，并给 stale 扫描器提供同一入口。
//
// 终态唯一性不在这里保证：emit 是 run 的 agentAIRunEmitter.emit，自带终态锁存。
type agentAIRunGuard struct {
	m         *agentAIManager
	sessionID string
	runSeq    int
	runID     string
	messageID string
	activity  *agentAIActivity
	cancel    context.CancelFunc
	// emit 是该 run 的 emitter.emit：自带终态锁存，保证任何路径恰好一个终态。
	emit agentTerminalWriter
	done chan struct{}

	doneOnce     sync.Once
	killOnce     sync.Once
	finalizeOnce sync.Once
}

func newAgentAIRunGuard(m *agentAIManager, run agentAIRun, emit agentTerminalWriter) *agentAIRunGuard {
	return &agentAIRunGuard{
		m:         m,
		sessionID: run.sessionID,
		runSeq:    run.runSeq,
		runID:     run.runID,
		messageID: run.messageID,
		activity:  run.activity,
		cancel:    run.cancel,
		emit:      emit,
		done:      make(chan struct{}),
	}
}

// attachRunGuard 把 guard 挂到会话上（runSeq 校验后才挂，防止窜号）。
//
// 不匹配（会话已不在 / 本回合已被新回合顶替）时返回 nil 并留痕：调用方必须
// 能区分「守卫已装」与「守卫没装上」。静默返回一个未挂载的空壳，会让该回合
// 的看门狗永远走 legacy 分支且事后无从排查。
func (m *agentAIManager) attachRunGuard(run agentAIRun, emit agentTerminalWriter) *agentAIRunGuard {
	if m == nil || run.activity == nil || run.cancel == nil {
		return nil
	}
	guard := newAgentAIRunGuard(m, run, emit)
	m.mu.Lock()
	session := m.sessions[run.sessionID]
	if session == nil || session.runSeq != run.runSeq {
		actual := -1
		if session != nil {
			actual = session.runSeq
		}
		m.mu.Unlock()
		logger.Warn(fmt.Sprintf(
			"[ai.run.guard] attach_skipped session=%s runSeq=%d actualRunSeq=%d",
			run.sessionID, run.runSeq, actual,
		))
		return nil
	}
	session.guard = guard
	m.mu.Unlock()
	return guard
}

func (m *agentAIManager) runGuardForActivity(activity *agentAIActivity) *agentAIRunGuard {
	if m == nil || activity == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, session := range m.sessions {
		if session.activity == activity && session.guard != nil {
			return session.guard
		}
	}
	return nil
}

// markRunGuardDone 标记该回合的 goroutine 已返回。强拆宽限据此判断要不要动手。
func (m *agentAIManager) markRunGuardDone(sessionID string, runSeq int) {
	if m == nil {
		return
	}
	m.mu.Lock()
	session := m.sessions[sessionID]
	var guard *agentAIRunGuard
	if session != nil && session.runSeq == runSeq {
		guard = session.guard
	}
	m.mu.Unlock()
	if guard != nil {
		guard.markDone()
	}
}

func (g *agentAIRunGuard) markDone() {
	if g == nil {
		return
	}
	g.doneOnce.Do(func() { close(g.done) })
}

// kill 是唯一的判死入口：记录原因 → 打日志 → cancel → 起强拆宽限。
// 幂等（看门狗与 stale 扫描器可能同时命中同一个 run）。
func (g *agentAIRunGuard) kill(reason string) {
	if g == nil {
		return
	}
	g.killOnce.Do(func() {
		g.activity.setKillReason(reason)
		logger.Warn(fmt.Sprintf(
			"[ai.run.kill] reason=%s session=%s runSeq=%d run=%s runFor=%s idleFor=%s progressIdleFor=%s awaiting=%t pendingToolUses=%d activeSubagents=%d grace=%s",
			reason,
			g.sessionID,
			g.runSeq,
			g.runID,
			time.Since(g.activity.runStart).Truncate(time.Second),
			g.activity.idleFor().Truncate(time.Second),
			g.activity.progressIdleFor().Truncate(time.Second),
			g.activity.awaiting(),
			g.activity.pendingToolUses.Load(),
			g.activity.activeSubagents.Load(),
			agentAIKillGrace,
		))
		g.cancel()
		g.armForceFinalize(reason)
	})
}

func (g *agentAIRunGuard) armForceFinalize(reason string) {
	if agentAIKillGrace <= 0 {
		return
	}
	time.AfterFunc(agentAIKillGrace, func() {
		select {
		case <-g.done:
			return // 正常收敛，无需强拆
		default:
		}
		g.forceFinalize(reason)
	})
}

// forceFinalize 在宽限期内 run 仍未返回时强制收尾：发恰好一个终态错误帧，
// 并把会话复位到 idle（cancel/steer/approval 全部清掉）。
//
// emit 放在 goroutine 里：僵尸可能正卡在 emitter 互斥锁或底层写侧，强拆本身
// 绝不能被它拖住——会话复位才是「手机不再显示运行中」的关键。
//
// 已知且接受的竞态：clearRunning 发出的 idle 状态帧与这里 turn_stalled 错误帧
// 都过同一个 emitter 终态闸，谁先谁得。「恰好一个终态」的不变量不变，但若状态
// 帧先赢，客户端只会看到回到 idle、看不到错误文案——判死原因仍可从
// [ai.run.force_finalize] 日志与 killReason 追溯。要消掉这个不确定性，得让
// clearRunning 接受「本次复位的原因」并合并进同一帧，属于后续增强。
//
// 另一条残余风险：若僵尸正卡在 emitter 写侧（e.write 持锁阻塞），这条终态
// 根本发不出去——本地的会话复位仍然完成（手机本地回到 idle），但服务端侧
// 只能靠 stale 扫描器收敛，且会留下 terminal_not_delivered 日志。
func (g *agentAIRunGuard) forceFinalize(reason string) {
	if g == nil {
		return
	}
	g.finalizeOnce.Do(func() {
		g.activity.setKillReason(reason)
		logger.Warn(fmt.Sprintf(
			"[ai.run.force_finalize] session=%s runSeq=%d run=%s reason=%s runFor=%s idleFor=%s",
			g.sessionID, g.runSeq, g.runID, reason,
			time.Since(g.activity.runStart).Truncate(time.Second),
			g.activity.idleFor().Truncate(time.Second),
		))
		payload := agentAIErrorPayload(g.sessionID, g.messageID,
			fmt.Errorf("回合已停止响应（%s），已判死，请重试", reason))
		payload["error_code"] = "turn_stalled"
		payload["status"] = "stalled"
		go func() {
			if err := g.emit(payload); err != nil {
				// 强拆终态送不出去（僵尸一般正卡在 emitter 写侧）时，手机收不到
				// 终态、服务端只能等 stale 扫描器兜底。错误绝不能吞——否则事后
				// 只能靠「本该出现的日志没出现」逆推。
				logger.Warn(fmt.Sprintf(
					"[ai.run.force_finalize] terminal_not_delivered session=%s runSeq=%d run=%s err=%v",
					g.sessionID, g.runSeq, g.runID, err,
				))
			}
		}()
		// 先打标记再复位：复位对外意味着「手机已回到 idle」，此后僵尸的任何
		// 迟返回都必须被非帧副作用守卫挡住（Task 8）。
		g.m.markRunFinalized(g.sessionID, g.runSeq)
		g.m.clearRunning(g.sessionID, g.runSeq, g.emit)
	})
}

// wrapKillCancel 把看门狗的裸 cancel 换成带观测与强拆升级的版本。
//
// 刻意保持 agentAIWatchdogLoop 的签名不变（9 处测试直接调它），改在这里注入。
// 查不到本回合的 guard 时退化为「只打日志 + cancel」（见下方 no_guard 分支）。
func (m *agentAIManager) wrapKillCancel(activity *agentAIActivity, cancel context.CancelFunc) context.CancelFunc {
	return func() {
		if activity == nil {
			cancel()
			return
		}
		reason := activity.killReasonOr("unknown")
		if guard := m.runGuardForActivity(activity); guard != nil {
			guard.kill(reason)
			return
		}
		// 走到这里只有两种可能：本回合的 guard 未挂载（并发起跑的 TOCTOU，
		// 见 attachRunGuard 的 attach_skipped 日志），或已被新回合顶替。
		// TUI 回合不会到这里——它在 startAIWatchdog 之前就 cancel 并 return 了。
		logger.Warn(fmt.Sprintf(
			"[ai.run.kill] reason=%s no_guard=1 runFor=%s idleFor=%s progressIdleFor=%s awaiting=%t pendingToolUses=%d activeSubagents=%d",
			reason,
			time.Since(activity.runStart).Truncate(time.Second),
			activity.idleFor().Truncate(time.Second),
			activity.progressIdleFor().Truncate(time.Second),
			activity.awaiting(),
			activity.pendingToolUses.Load(),
			activity.activeSubagents.Load(),
		))
		cancel()
	}
}

// agentAIRunSideEffectsAllowed 是僵尸非帧副作用的 runSeq 守卫。
//
// 判据是**强拆时打的显式标记** finalizedRunSeq，不是 session.cancel == nil：
// 单测/TUI 路径注册的会话本来就没有 cancel，若拿它当判据，会把合法回合的
// history/journal/resume 写一起挡掉（既有 6 个 agentcore 用例直接变红）。
// 未注册会话（单测/TUI 路径）返回 true，保持既有行为。
func (m *agentAIManager) agentAIRunSideEffectsAllowed(sessionID string, runSeq int) bool {
	if m == nil {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[sessionID]
	if session == nil {
		return true
	}
	if session.runSeq != runSeq {
		return false
	}
	return session.finalizedRunSeq != runSeq
}

// markRunFinalized 登记「本 runSeq 已被强拆收尾」，供非帧副作用守卫判定。
//
// 不随 clearRunning 清零：标记按 runSeq 记账，Retry 起新回合（runSeq 推进）
// 后旧标记自动不再命中，无需在起跑路径手工复位。
//
// 唯一例外是 /clear：它把 runSeq 归零，下一回合可能复用同一个 runSeq，此时
// 旧标记会误命中、把合法回合的 history/journal/resume 写一起丢掉。所以
// /clear 分支必须同时把 finalizedRunSeq 清零（见 Step 8）。
func (m *agentAIManager) markRunFinalized(sessionID string, runSeq int) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if session := m.sessions[sessionID]; session != nil && session.runSeq == runSeq {
		session.finalizedRunSeq = runSeq
	}
}

package services

import (
	"context"
	"fmt"
	"strings"
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

	// 起跑快照：判死强拆时用来判定「恢复点是不是本回合自己钉进来的」。
	// resumeAtStart 为空 ⟹ 现在任何非空的 resumeSessionID 必由本回合中途写入；
	// bindingAtStart 是起跑时已确认的绑定（可能是上一轮留下的合法值）。
	resumeAtStart  string
	bindingAtStart string

	doneOnce     sync.Once
	killOnce     sync.Once
	finalizeOnce sync.Once
}

func newAgentAIRunGuard(m *agentAIManager, run agentAIRun, emit agentTerminalWriter) *agentAIRunGuard {
	return &agentAIRunGuard{
		m:             m,
		sessionID:     run.sessionID,
		runSeq:        run.runSeq,
		runID:         run.runID,
		messageID:     run.messageID,
		activity:      run.activity,
		cancel:        run.cancel,
		emit:          emit,
		done:          make(chan struct{}),
		resumeAtStart: strings.TrimSpace(run.resumeSessionID),
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
	// 进程级 stale 扫描器与单回合成败无关：无论本次是否挂载成功（attach 竞态
	// 跳过也一样），都要保证 manager 上有且仅有一条扫描 goroutine 在跑。
	// 注意 TUI 回合在 agent_ai.go 的 TUI 分支提前返回、不挂 guard，因此不在
	// L3 扫描覆盖范围内。
	defer m.startStaleRunScanner()
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
	// 起跑快照必须与挂载在同一临界区：起跑前已确认的绑定是上一轮的合法值，
	// 判死撤销不能把它当成「本回合钉入」误删。
	guard.bindingAtStart = strings.TrimSpace(m.bindings[run.sessionID].NativeSessionID)
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

// markRunGuardDone 标记「本回合已正常返回」，让宽限计时器不再强拆。
//
// expectGuard 与 clearRunning 同一口径：/clear 后 runSeq 会被复用，只比 runSeq
// 会让僵尸的 defer 关掉新回合的 done 通道——新回合的宽限计时器随之失效，真
// 僵尸再没人强拆。传了 guard 就必须是会话当前挂着的那一个。
func (m *agentAIManager) markRunGuardDone(sessionID string, runSeq int, expectGuard ...*agentAIRunGuard) {
	if m == nil {
		return
	}
	m.mu.Lock()
	session := m.sessions[sessionID]
	var guard *agentAIRunGuard
	if session != nil && session.runSeq == runSeq {
		if len(expectGuard) == 0 || expectGuard[0] == nil || session.guard == expectGuard[0] {
			guard = session.guard
		}
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
		// 身份校验与复位在同一个临界区内完成（finalizeStalledGuard）：/clear
		// 复用 runSeq 后，迟到的强拆绝不能拆掉新的合法回合。
		if code, ok := g.m.finalizeStalledGuard(g, g.emit); !ok {
			detail := "该回合已不是会话当前回合"
			switch code {
			case "session_missing":
				detail = "会话已不存在"
			case "guard_replaced":
				detail = "guard 已被新回合顶替"
			case "run_seq_moved":
				detail = "runSeq 已推进"
			}
			logger.Warn(fmt.Sprintf(
				"[ai.run.force_finalize] guard_not_current session=%s runSeq=%d run=%s reason=%s code=%s（%s，跳过复位）",
				g.sessionID, g.runSeq, g.runID, reason, code, detail,
			))
		}
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

// agentAIRunSideEffectsAllowed 是僵尸非帧副作用的回合归属守卫。
//
// 判据是**强拆时打的显式标记** finalizedRunSeq，不是 session.cancel == nil：
// 单测/TUI 路径注册的会话本来就没有 cancel，若拿它当判据，会把合法回合的
// history/journal/resume 写一起挡掉（既有 6 个 agentcore 用例直接变红）。
// 未注册会话（单测/TUI 路径）返回 true，保持既有行为。
//
// expectGuard 是可选的回合身份：runSeq 不是跨 /clear 的稳定身份（/clear 把它
// 归零后会被新回合复用），只比 runSeq 时旧回合的僵尸迟返回可能撞上新回合的
// runSeq 并通过守卫。调用方传 run.guard 后，guard 指针不同即判为非本回合。
// 不传或传 nil 时退化为纯 runSeq 判定（单测/TUI 路径）。
func (m *agentAIManager) agentAIRunSideEffectsAllowed(sessionID string, runSeq int, expectGuard ...*agentAIRunGuard) bool {
	if m == nil {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[sessionID]
	if session == nil {
		return true
	}
	if len(expectGuard) > 0 && expectGuard[0] != nil && session.guard != expectGuard[0] {
		return false
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

// finalizeStalledGuard 校验 guard 仍是该会话当前回合后，打终结标记并复位运行态。
// /clear 会把 runSeq 归零后被新回合复用，只比 runSeq 会把迟到的强拆打到新回合上，
// 所以这里以 guard 身份为判据，并合取 runSeq：/clear 归零 runSeq 但不摘 guard，
// 若将来放开「运行中 /clear」，只比 guard 指针会把 finalizedRunSeq 写成旧 runSeq，
// 从而挡住复用该 runSeq 的新回合的 history/journal/resume 写入；一个合取条件即可闭合。
//
// 返回原因码（"" 表示成功复位），供调用方如实记日志。
func (m *agentAIManager) finalizeStalledGuard(g *agentAIRunGuard, writeJSON agentTerminalWriter) (string, bool) {
	if m == nil || g == nil {
		return "session_missing", false
	}
	m.mu.Lock()
	session := m.sessions[g.sessionID]
	if session == nil {
		m.mu.Unlock()
		return "session_missing", false
	}
	if session.guard != g {
		m.mu.Unlock()
		return "guard_replaced", false
	}
	if session.runSeq != g.runSeq {
		m.mu.Unlock()
		return "run_seq_moved", false
	}
	session.finalizedRunSeq = g.runSeq
	revokedResume, droppedBinding := m.revokeRunResumePointLocked(g)
	m.mu.Unlock()
	if droppedBinding != "" {
		// 绑定是持久化状态：不落盘的话，进程重启后 loadIdentityState 会把僵尸
		// 绑定读回来，下一回合照样从它取 resumeID。
		if err := m.persistIdentityState(); err != nil {
			logger.Warn(fmt.Sprintf(
				"[ai.run.zombie_revoke] persist_failed session=%s runSeq=%d err=%v",
				g.sessionID, g.runSeq, err,
			))
		}
	}
	if revokedResume != "" || droppedBinding != "" {
		logger.Warn(fmt.Sprintf(
			"[ai.run.zombie_revoke] session=%s runSeq=%d run=%s revoked_resume=%q dropped_binding=%q reason=stalled",
			g.sessionID, g.runSeq, g.runID, revokedResume, droppedBinding,
		))
	}
	m.clearRunning(g.sessionID, g.runSeq, writeJSON, g)
	return "", true
}

// revokeRunResumePointLocked 撤销本回合在流式途中钉下的恢复点与绑定。
//
// 为什么在这里而不是 run 返回之后：判死路径必先 kill() → cancel()，僵尸迟返回
// 会先在 ctx.Err() != nil 的 stopped 早退处 return，永远走不到成功收尾段；而
// confirmBinding 在流式途中（早于判死）就已把内核新 id 钉进
// session.resumeSessionID 与 m.bindings。撤销因此只能在强拆链路里做。
//
// 判据只用起跑快照，不做时间猜测：
//   - resumeAtStart 非空 ⟹ 本回合起跑时已有已确认的恢复点，现在的值可能仍是
//     上一轮的合法值，一律不撤；
//   - resumeAtStart 为空 ⟹ 现在任何非空的 resumeSessionID 都必然是 confirmBinding
//     在本回合中途写进来的，撤掉；
//   - 绑定只有与起跑快照不同（即本回合新钉的那个）才删。
//
// 调用方必须持有 m.mu，且已在同一临界区内通过 guard 身份校验——此时会话仍被
// 本回合占着（busy 闸挡着新回合），不存在「撤掉的是新回合恢复点」的归属问题。
func (m *agentAIManager) revokeRunResumePointLocked(g *agentAIRunGuard) (revokedResume, droppedBinding string) {
	if g == nil || g.resumeAtStart != "" {
		return "", ""
	}
	session := m.sessions[g.sessionID]
	if session == nil {
		return "", ""
	}
	if session.resumeSessionID != "" {
		revokedResume = session.resumeSessionID
		session.resumeSessionID = ""
	}
	if binding, ok := m.bindings[g.sessionID]; ok && binding.NativeSessionID != "" && binding.NativeSessionID != g.bindingAtStart {
		droppedBinding = binding.NativeSessionID
		delete(m.bindings, g.sessionID)
	}
	return revokedResume, droppedBinding
}

// agentAIStaleRunThreshold 是「非等待态下多久没有任何活性事件就判死」。
//
//	ALIANG_AI_STALE_RUN  (time.ParseDuration). Default 5min.
//
// 注：ALIANG_AI_STALE_RUN=0/负值会被 resolveEnvDuration 回落默认值，即 stale
// 检测不可关闭（刻意 fail-safe）。
var agentAIStaleRunThreshold = resolveEnvDuration("ALIANG_AI_STALE_RUN", 5*time.Minute)

// agentAIStaleScanInterval 是 stale running 自检的扫描周期。
var agentAIStaleScanInterval = time.Minute

// agentAIStaleScannerEnabled 是常驻扫描的总开关。测试在 TestMain 里关掉它：
// 扫描逻辑由用例直接调 scanStaleRuns 覆盖，常驻 ticker 只会泄漏 goroutine、
// 并让 1min 一次的读与用例对包级 var 的写构成 -race 竞争。
var agentAIStaleScannerEnabled = true

// startStaleRunScanner 为这个 manager 启动唯一一条扫描 goroutine。
//
// 只在第一次注册 guard 时启动（sync.Once），扫到进程退出为止：进程级兜底，
// 不绑定任何单回合的 ctx。
//
// 生命周期取舍：goroutine 不随 manager 回收（生产只有一个全局 manager，空转
// 开销可忽略）。测试里由 agentAIStaleScannerEnabled 在 TestMain 统一关闭，
// 不要再指望调大 agentAIStaleScanInterval——对已创建的 ticker 无效。
func (m *agentAIManager) startStaleRunScanner() {
	if m == nil {
		return
	}
	if !agentAIStaleScannerEnabled {
		return
	}
	m.scanOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(agentAIStaleScanInterval)
			defer ticker.Stop()
			for range ticker.C {
				m.scanStaleRuns()
			}
		}()
	})
}

// scanStaleRuns 扫描所有运行中会话，把已判死的交给同一个 guardian kill 通道。
//
// 不自己发终态：终态唯一性由 emitter 终态闸保证，扫描器只是「发现者」。
func (m *agentAIManager) scanStaleRuns() {
	if m == nil {
		return
	}
	type victim struct {
		guard  *agentAIRunGuard
		reason string
	}
	var victims []victim
	m.mu.Lock()
	for _, session := range m.sessions {
		guard := session.guard
		// guard != nil 才是「这是个在跑的真回合」的真正判据（Task 4 起所有
		// provider 回合都会挂 guard）。session.cancel == nil 是历史遗留的
		// 廉价前置条件，只为跳过从未起跑的会话，不要把它当成僵尸判据去「优化」。
		if guard == nil || session.cancel == nil || guard.activity == nil {
			continue
		}
		reason, dead := guard.activity.staleDeathReason(
			agentAIStaleRunThreshold,
			agentAIApprovalTimeout,
			agentAIToolWaitCeiling,
			agentAISubagentWaitCeiling,
		)
		if dead {
			victims = append(victims, victim{guard: guard, reason: reason})
		}
	}
	m.mu.Unlock()
	for _, v := range victims {
		v.guard.kill(v.reason)
	}
}

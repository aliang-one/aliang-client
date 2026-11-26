package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
)

func (m *agentAIManager) clearRunning(sessionID string, runSeq int, writeJSON agentTerminalWriter, expectGuard ...*agentAIRunGuard) {
	m.mu.Lock()
	session := m.sessions[sessionID]
	// 身份校验必须与复位在同一个临界区：独立的事前检查会留下「检查通过 →
	// 新回合挂上 guard → 复位」的窗口，正是要消灭的竞态。
	guardOwns := len(expectGuard) == 0 || expectGuard[0] == nil ||
		(session != nil && session.guard == expectGuard[0])
	var steerControl *agentAICodexSteerControl
	var claudeSteerControl *agentAIClaudeSteerControl
	var coreSteerControl *agentAICoreSteerControl
	var subagentControl *agentCoreSubagentControl
	if guardOwns && session != nil && session.runSeq == runSeq {
		steerControl = session.codexSteer
		claudeSteerControl = session.claudeSteer
		coreSteerControl = session.coreSteer
		session.cancel = nil
		session.activeWriter = nil
		session.approvalToken = ""
		session.activity = nil
		if session.guard != nil {
			// 正常收尾也必须关闭 done。guard 挂在所有 provider 的回合上，而
			// markRunGuardDone 只有 agentcore 路径调用；缺了这一步，codex/claude
			// 回合被合法 kill 后 30s 还会再跑一次多余的强拆（终态被 emitter 闸
			// 丢弃，但会多一条 force_finalize 日志 + 一个多余 goroutine）。
			session.guard.markDone()
			session.guard = nil
		}
		session.codexSteer = nil
		session.claudeSteer = nil
		session.coreSteer = nil
		subagentControl = session.subagentControl
		session.subagentControl = nil
		session.activeRunID = ""
		m.finishRemoteUnsubscribeLocked(sessionID)
		session.goalIdentity = nil
		session.history = trimAgentAIHistoryForSession(session, session.history)
	}
	var cancelled []string
	if guardOwns {
		cancelled = m.clearPendingApprovalsLocked(sessionID, runSeq, models.AgentAIApprovalDecisionCancel)
	}
	m.mu.Unlock()
	if steerControl != nil {
		steerControl.close("not_running", "codex turn ended before steer was applied", "turn_ended")
	}
	if claudeSteerControl != nil {
		claudeSteerControl.close("not_running", "claude turn ended before steer was applied", "turn_ended")
	}
	if coreSteerControl != nil {
		coreSteerControl.close("not_running", "agentcore turn ended before steer was applied", "turn_ended")
	}
	// 子 agent 控制表随回合退场（L3）：先停全部在跑子 agent——回合已收尾，
	// 这里还挂着说明是异常路径（正常路径下桥的 defer 各自 unregister 干净），
	// 留着只会让 goroutine 无主游荡到父 ctx 超时才死。
	if subagentControl != nil {
		if n := subagentControl.stop(""); n > 0 {
			logger.Warn(fmt.Sprintf("agentcore-subagent: turn ended with %d live subagent(s); cancelled session=%s runSeq=%d", n, sessionID, runSeq))
		}
	}
	m.emitApprovalCancelled(writeJSON, sessionID, cancelled, "run_ended")
}

// attachClaudeSteer 把 Claude steer 通道挂到会话上，供 ai.steer 路由查找。
// 只在 runSeq 仍匹配时挂载，避免迟到/串号的 run 覆盖当前通道。

func (m *agentAIManager) attachClaudeSteer(sessionID string, runSeq int, control *agentAIClaudeSteerControl) {
	if control == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if session := m.sessions[sessionID]; session != nil && session.runSeq == runSeq {
		session.claudeSteer = control
	}
}

func (m *agentAIManager) appendAssistantHistory(sessionID string, runSeq int, messageID string, output string) {
	output = strings.TrimSpace(output)
	if output == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[sessionID]
	if session == nil || session.runSeq != runSeq {
		return
	}
	session.history = append(session.history, agentAIMessage{
		Role:      "assistant",
		MessageID: agentAssistantMessageID(messageID),
		Content:   output,
		CreatedAt: time.Now().UTC(),
	})
	session.history = trimAgentAIHistoryForSession(session, session.history)
}

// lastUserHistoryMessage 返回会话 history 里最后一条 user 消息（m.mu 内读取）。
// journal 用它拿「触发本轮的真实用户 content」——不取 run.prompt（拼接产物）。

func (m *agentAIManager) lastUserHistoryMessage(sessionID string) (agentAIMessage, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[sessionID]
	if session == nil {
		return agentAIMessage{}, false
	}
	for i := len(session.history) - 1; i >= 0; i-- {
		if strings.EqualFold(session.history[i].Role, "user") {
			return session.history[i], true
		}
	}
	return agentAIMessage{}, false
}

// journalSteerUserMessage 把一条运行中插话落成 journal 的 user 行。
// content 必须是用户原话：steer.Content 在投递前会被拼上附件后缀与手机契约
// 包装（见 ai.steer 投递分支），journal 按 spec §5 取真实用户 content，与
// session.history 的 user 行同口径。
// steer 可能先于 run 的任何写入到达（目录/文件都还没有）——appendLineLocked
// 内部有目录懒创建，这里只管追加。steer 先建的文件无头行（ProjectPath/Model
// 暂空，reader ID 兜底取文件名），随后 run 入口的 ensureSessionHeader 会整体
// 重写补头并保留已落内容（agent_ai_agentcore_journal.go prependHeaderLocked）。

func (m *agentAIManager) journalSteerUserMessage(sessionID string, msg agentAISteerMessage, content string) {
	if m == nil || m.coreJournal == nil {
		return
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	if !msg.CreatedAt.IsZero() {
		ts = msg.CreatedAt.UTC().Format(time.RFC3339)
	}
	m.coreJournal.appendUser(sessionID, msg.MessageID, content, ts, msg.JournalAttachments)
}

// setAgentAIResumeSessionIDIfEmpty pins a CLI-assigned session id so the NEXT
// turn resumes it instead of starting a fresh process-local conversation. No-op
// when no id was captured, the session is gone, the run is stale, or the session
// already has a resume id (imported sessions carry one, or it was captured on an
// earlier turn). Codex app-server threads are persisted by Codex and use this too,
// even though Aliang starts a fresh app-server transport process for each turn.

func (m *agentAIManager) setAgentAIResumeSessionIDIfEmpty(sessionID string, runSeq int, resumeSessionID string) {
	resumeSessionID = strings.TrimSpace(resumeSessionID)
	if resumeSessionID == "" {
		logger.Info(fmt.Sprintf("resume-set: skip, no captured id (session %s runSeq %d)", sessionID, runSeq))
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[sessionID]
	if session == nil || session.runSeq != runSeq {
		logger.Info(fmt.Sprintf("resume-set: skip, session gone/stale (session %s runSeq %d)", sessionID, runSeq))
		return
	}
	if session.resumeSessionID == "" {
		session.resumeSessionID = resumeSessionID
		logger.Info(fmt.Sprintf("resume-set: PINNED CLI resume id %s on session %s", resumeSessionID, sessionID))
	} else {
		logger.Info(fmt.Sprintf("resume-set: already set %s (session %s)", session.resumeSessionID, sessionID))
	}
}

func (m *agentAIManager) clearAgentAIResumeSessionID(sessionID string, runSeq int, expected string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[sessionID]
	if session == nil || session.runSeq != runSeq {
		return
	}
	// expected 为空是「按值撤销」的退化形态：无条件清空会把确认期钉下的恢复点
	// 一并抹掉（2026-10-29 already-in-use 事故类：空值覆盖已钉恢复点）。空锚即
	// 不撤，撤销必须指名道姓。
	if expected == "" {
		return
	}
	if session.resumeSessionID == expected {
		session.resumeSessionID = ""
	}
}

// agentAIRunOutcome reports how a single CLI pass finished.

const (
	agentAIRunDone agentAIRunOutcome = iota
	// agentAIRunResumeMissing means the CLI could not find the requested
	// --resume session locally (it is absent, or filed under a different
	// project path than the run's cwd). The pass emitted no assistant output;
	// the caller should retry the run fresh, without --resume.
	agentAIRunResumeMissing
	// agentAIRunEffortRejected means the CLI hard-rejected the requested
	// --effort value at argument validation (older claude builds exit 1 and
	// print the accepted levels). The pass emitted no assistant output; the
	// caller should clamp the effort to a listed level and retry once.
	agentAIRunEffortRejected
	// agentAIRunSessionIDInUse means the CLI refused to START a new session
	// with the caller-provided --session-id because a transcript with that id
	// already exists on disk (the identity was consumed by an earlier run).
	// The pass emitted no assistant output; the caller should release the
	// reserved binding, drop --session-id, and retry fresh once.
	agentAIRunSessionIDInUse
	// agentAIRunThreadBusy means codex rejected thread/resume|fork because the
	// native thread is currently owned by another live codex client (single
	// writer: 用户自己的 codex-tui/IDE 还开着)。The pass emitted no assistant
	// output and NO terminal frame; the caller asks the user whether to fork
	// the conversation (default: no fork → codex_thread_busy terminal error).
	agentAIRunThreadBusy
)

type agentAIRunOutcome int

func (m *agentAIManager) runCLI(ctx context.Context, run agentAIRun, writeJSON agentTerminalWriter) {
	// 必须带 guard：内核路径是在同一 goroutine 内联调用 runAgentCore 的，僵尸
	// 迟返回时 agentcore 内部两个带 guard 的 defer 会正确跳过，控制权却回到这里。
	// 若只比 runSeq，/clear 归零后新回合复用同一个值，就会把新回合的
	// cancel/activity/guard/steer 与待审批一并拆掉。正常路径 session.guard
	// 就是 run.guard，行为不变。
	defer m.clearRunning(run.sessionID, run.runSeq, writeJSON, run.guard)
	// 开跑先给云端把订阅订上（2026-11-12 视频会话事故）：本函数是 message()/
	// optionResponse() 全 provider（claude/codex/agentcore）的统一 run 入口，
	// 桌面-origin 的回合在 App interest 过期后云端已不在订阅名单——不订回来，
	// ai.run.started 根本到不了云端，服务端自证认领（PhoneServer 8d8f790）
	// 拿不到触发帧，云端 run_state 永卡终态。同 goroutine 内先订阅后写帧，
	// 扇出首帧必然看见订阅。离线时 no-op，重连由活跃 run 补订阅兜底。
	m.ensureRemoteAISessionSubscribed(run.sessionID)
	if run.cancel != nil {
		defer run.cancel()
	}

	// 附件下载放在 run 的最外层：一次 run 内可能重试多个 pass（resume-missing
	// / effort-rejected），下载一次、整轮复用，退出时统一删临时目录。失败按
	// 硬错误处理——宁可不跑，也不让模型拿到一个「有附件清单但没有文件」的
	// prompt（那会变成一轮看似正常、实则瞎猜的回复）。
	attachments, cleanupAttachments, err := downloadAgentAIAttachments(ctx, run.attachments, run.attachmentAllowedHosts)
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, err))
		return
	}
	defer cleanupAttachments()
	run.attachments = attachments

	// 持久附件缓存（尽力而为）：副本落 <home>/aliang-attachments/<session>/，
	// journal 与网页显示读它；失败仅 Warn 降级（无网页显示≠内核失图）。
	persistBase := strings.TrimSpace(run.messageID)
	if persistBase == "" {
		persistBase = fmt.Sprintf("run-%d", run.runSeq)
	}
	persistAgentAIAttachmentFiles(run.sessionID, persistBase, run.attachments)

	if agentAIUseCodexAppServer(run.provider) {
		outcome := m.runCodexAppServer(ctx, run, writeJSON)
		if outcome == agentAIRunResumeMissing && ctx.Err() == nil {
			emitAgentAIResumeMissingNotice(writeJSON, run, run.resumeSessionID)
			m.clearAgentAIResumeSessionID(run.sessionID, run.runSeq, run.resumeSessionID)
			run.resumeSessionID = ""
			run.prompt = firstNonEmpty(run.freshPrompt, run.prompt)
			_ = m.runCodexAppServer(ctx, run, writeJSON)
		}
		if outcome == agentAIRunThreadBusy && ctx.Err() == nil {
			m.handleAgentAICodexThreadBusy(ctx, &run, writeJSON)
		}
		return
	}

	// 内置内核：进程内跑 agent loop，不 spawn 任何外部 CLI，因此没有
	// resume-missing / effort-rejected 这两类自愈重试——内核自己就是权威，
	// 会话 id 由我们自己发。
	//
	// fallback 命中时把 run.provider 改写为 "agentcore"：auto 只是「由你决定」
	// 的解析指令，不是真实 provider。CLI 路径也是这么做的——confirmBinding 落
	// 的是 tool.id（解析结果）而不是 "auto"，否则绑定记录的 provider 会和实际
	// 跑的 provider 对不上，providersEquivalent 一比对就分叉。
	if agentAIUseAgentCoreForRun(run) {
		run.provider = "agentcore"
		m.runAgentCore(ctx, run, writeJSON)
		return
	}

	// Try to resume the prior CLI session when one is referenced. If that
	// session does not exist locally (an imported/foreign session, or one filed
	// under a different project path than this run's cwd), retry fresh so the
	// conversation still streams instead of surfacing a hard error.
	if strings.TrimSpace(run.resumeSessionID) != "" {
		outcome, rej := m.runCLIPass(ctx, run, writeJSON, true)
		if outcome != agentAIRunResumeMissing {
			m.retryAfterEffortRejected(ctx, run, writeJSON, outcome, rej, true)
			return
		}
		// resume id 本机缺失（导入会话/异项目路径）→ 降级 fresh。显式提示一次：
		// 此前这一降级完全静默，用户只看到「上下文断了」却说不出为什么。
		emitAgentAIResumeMissingNotice(writeJSON, run, run.resumeSessionID)
	}
	outcome, rej := m.runCLIPass(ctx, run, writeJSON, false)
	if outcome == agentAIRunSessionIDInUse && ctx.Err() == nil {
		// 自愈：reserved 身份已被消费（jsonl 在盘），释放绑定并去掉
		// --session-id，让 CLI 自铸新 id 重跑一次（与 resume-missing 的清+重试
		// 同构）。单次封顶：重跑时 newSessionID 为空，分类器不可能再命中。
		m.releaseReservedBinding(run.sessionID, run.reservedNativeSessionID)
		run.reservedNativeSessionID = ""
		outcome, rej = m.runCLIPass(ctx, run, writeJSON, false)
	}
	m.retryAfterEffortRejected(ctx, run, writeJSON, outcome, rej, false)
}

// emitAgentAIResumeMissingNotice 发 resume_missing 的 ai.run.notice（信息性帧，
// 回合照常继续）。三处自愈降级（runCLI 主路径 / retryAfterEffortRejected /
// codex 路径）共用；每条路径每回合至多降级一次，fresh 重跑不带 resume 不会再
// 触发，故天然只发一次。

func emitAgentAIResumeMissingNotice(writeJSON agentTerminalWriter, run agentAIRun, resumeSessionID string) {
	if writeJSON == nil {
		return
	}
	_ = writeJSON(map[string]interface{}{
		"type":       models.AgentEventAIRunNotice,
		"session_id": run.sessionID,
		"message_id": agentAssistantMessageID(run.messageID),
		"code":       "resume_missing",
		"detail":     fmt.Sprintf("native session %s not found; continuing as a fresh conversation", strings.TrimSpace(resumeSessionID)),
	})
}

// retryAfterEffortRejected 在 CLI 拒绝 effort(flag 本身不存在,或硬拒值)时
// 自愈重试一次:flag 级拒绝直接去掉 --effort,值级拒绝按声明的合法清单钳制
// (与 agentAIRunResumeMissing 的自愈重试同构)。无法钳制、或重试仍被拒时补发
// ai.error,避免回合静默无响应。

func (m *agentAIManager) retryAfterEffortRejected(ctx context.Context, run agentAIRun, writeJSON agentTerminalWriter, outcome agentAIRunOutcome, rej agentAIEffortRejection, allowResume bool) {
	if outcome != agentAIRunEffortRejected || ctx.Err() != nil {
		return
	}
	requested := strings.TrimSpace(run.effort)
	plan := planAgentAIEffortRetry(requested, rej)
	if !plan.retry {
		detail := "no compatible effort level"
		if rej.flagUnsupported {
			detail = "CLI does not support --effort"
		} else if len(rej.levels) > 0 {
			detail = "supported: " + strings.Join(rej.levels, ", ")
		}
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID,
			fmt.Errorf("claude CLI rejected effort %q (%s)", requested, detail)))
		return
	}
	run.effort = plan.retryEffort
	second, _ := m.runCLIPass(ctx, run, writeJSON, allowResume)
	if second == agentAIRunEffortRejected {
		_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID,
			fmt.Errorf("claude CLI rejected effort %q even after clamping to %q", requested, plan.retryEffort)))
		return
	}
	// 去档/钳制重试撞上 resume 缺失(与 runCLI 的自愈同构):allowResume 路径
	// 的第二次 pass 可能因陈旧 resume id 返回 ResumeMissing,直接返回会让回合
	// 既无输出也无错误(2026-09-29 评审确认的静默回归)。清掉 resume 再补一发
	// fresh pass;此时 effort 已处理完毕,fresh pass 不再带被拒的档。
	if second == agentAIRunResumeMissing && allowResume && ctx.Err() == nil {
		emitAgentAIResumeMissingNotice(writeJSON, run, run.resumeSessionID)
		m.clearAgentAIResumeSessionID(run.sessionID, run.runSeq, run.resumeSessionID)
		run.resumeSessionID = ""
		run.prompt = firstNonEmpty(run.freshPrompt, run.prompt)
		_, _ = m.runCLIPass(ctx, run, writeJSON, false)
	}
}

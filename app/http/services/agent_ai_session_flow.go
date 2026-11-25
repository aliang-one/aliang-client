package services

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/internal/tccguard"
)

func (m *agentAIManager) create(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	if writeJSON == nil {
		return
	}
	sessionID := remoteString(msg, "session_id")
	if sessionID == "" {
		_ = writeJSON(agentAIErrorPayload("", "", errors.New("ai.session.create missing session_id")))
		return
	}
	projectPath, err := resolveAgentAICWD(remoteString(msg, "project_path"))
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(sessionID, "", err))
		return
	}
	mode := remoteString(msg, "mode")
	if mode == "" {
		mode = "vibe"
	}
	provider := strings.TrimSpace(remoteString(msg, "provider"))
	if provider == "" {
		provider = strings.TrimSpace(remoteString(msg, "tool"))
	}
	provider, err = normalizeAgentAIProvider(provider)
	if err != nil {
		_ = writeJSON(agentAIErrorPayload(sessionID, "", err))
		return
	}
	model := strings.TrimSpace(remoteString(msg, "model"))
	effort := strings.TrimSpace(remoteString(msg, "effort"))
	// approval_mode 带键标记(2026-10-31 F2 同款纪律):老客户端不带键时
	// 绝不把持久化的模式冲成默认。
	approvalMode, approvalModeSet := parseAgentAIApprovalMode(msg)
	contextWindow := clampAgentAIContextWindow(remoteInt(msg, "context_window", 0))
	maxTokens := clampAgentAIMaxTokens(remoteInt(msg, "max_tokens", 0))
	// 会话级 MCP 开关：键存在即整体替换（含空数组=全部启用）；键缺失=不动。
	disabledMCPServers, disabledMCPServersSet := parseAgentAIDisabledMCPServers(msg)
	disabledSkills, disabledSkillsSet := parseAgentAIDisabledSkills(msg)
	resumeSessionID := firstNonEmpty(remoteString(msg, "resume_session_id"), remoteString(msg, "source_session_id"))
	reservedNativeSessionID := strings.TrimSpace(remoteString(msg, "reserved_native_session_id"))
	bindingVersion := remoteInt(msg, "binding_version", 0)
	initialContext := strings.TrimSpace(remoteString(msg, "initial_context"))
	isGoalFork, _ := msg["is_goal_fork"].(bool) // #5: server flags fork sessions
	surface := normalizeAgentAISurface(remoteString(msg, "surface"))
	claudePolicy := parseAgentAIClaudeRemotePolicy(msg)
	history := remoteAgentAIHistory(msg)
	// 载荷 transcript 判定必须在 initial_context 拼接之前——initial_context 是
	// 会话级上下文不是对话历史，journal 回装的「载荷没带货载历史」判定以这里
	// 为准（与 message() 懒建分支的 len(lazyHistory)==0 判点同位）。
	payloadHasTranscript := len(history) > 0
	if initialContext != "" {
		history = append([]agentAIMessage{{
			Role:      "system",
			MessageID: "initial_context",
			Content:   initialContext,
			CreatedAt: time.Now().UTC(),
		}}, history...)
	}

	// 撤回重发（spec §A.2）：截断 journal + history 全文重建（帧不带 transcript）。
	// 时序：①活跃拒绝先于任何 journal 写；②不在内存 ⇒ 无活跃 run，前置检查
	// 覆盖 create() 双分支；③读写失败绝不静默空 history，幂等重试即收敛。
	// 仅 agentcore 生效——claude/codex 有各自的原生转录，journal 无锚点。
	//
	// TOCTOU 窗口声明（时序铁律不变量的一部分，重排者必读）：下方 peek 解锁
	// （m.mu.Unlock）与 truncateBefore 的 rename 之间存在理论竞态窗——窗口内
	// 并发起跑的 run 对本路径不可见，其 end 行可能落进刚截断的 journal。接受
	// 依据＝前端「stop → 等终态 → create」的串行契约（撤回流程不并发起跑），
	// 加上并发截断的收敛性（truncateBefore 按位置幂等，重试即收敛）；不加锁
	// 贯穿 journal 重写是因为锁内 rename 会把 peek→truncate 的临界区拉长到
	// 覆盖磁盘 IO，与「活跃拒绝先于任何 journal 写」的次序保证互相卡死。
	truncateBeforeID := strings.TrimSpace(remoteString(msg, "truncate_before_message_id"))
	truncateActive := truncateBeforeID != "" && provider == "agentcore"
	truncateApplied := false
	if truncateActive {
		m.mu.Lock()
		existingPeek := m.sessions[sessionID]
		busy := existingPeek != nil && existingPeek.cancel != nil
		m.mu.Unlock()
		if busy {
			// cancel==nil ⇒ runAgentCore defer LIFO 已落 end 行（清位锚，勿重排）。
			_ = writeJSON(agentAIErrorPayload(sessionID, "", &agentAITruncateError{code: truncateErrCodeRunActive, msg: "ai.session.create truncate rejected: run active"}))
			return
		}
		if m.coreJournal == nil {
			_ = writeJSON(agentAIErrorPayload(sessionID, "", &agentAITruncateError{code: truncateErrCodeJournalUnavailable, msg: "ai.session.create truncate unavailable: journal disabled"}))
			return
		}
		applied, err := m.coreJournal.truncateBefore(sessionID, truncateBeforeID)
		if err != nil {
			_ = writeJSON(agentAIErrorPayload(sessionID, "", &agentAITruncateError{code: truncateErrCodeFailed, msg: "ai.session.create truncate failed", err: err}))
			return
		}
		truncateApplied = applied
		hist, _, rerr := readAgentCoreJournalHistoryFrom(m.coreJournal.dir, sessionID)
		if rerr != nil {
			// 截断成功后文件必在；ErrNotExist 只会出现在「从未落过 journal」的
			// no-op 截断上——同样拒绝，绝不静默回退空 history。
			_ = writeJSON(agentAIErrorPayload(sessionID, "", &agentAITruncateError{code: truncateErrCodeRebuildFailed, msg: "ai.session.create rebuild failed", err: rerr}))
			return
		}
		history = hist // 空 journal=空 history:允许空覆盖（撤回到空）
	}

	m.mu.Lock()
	if existing := m.sessions[sessionID]; existing != nil {
		existing.mode = mode
		existing.projectPath = projectPath
		existing.provider = provider
		// model/effort 只在「本次下发非空」时才覆盖既有会话。聊天页每条消息前
		// 都会重发一次 create，而续聊设备会话时前端可能拿不到原值（清单未回填
		// 时为 undefined）→ 若无条件赋值，空串会把服务端记住的模型抹掉，紧接着
		// 内核因「未指定模型」拒绝开跑（agent_ai_agentcore.go 无兜底链）。空值
		// 一律视为「沿用既有设置」，显式清空由 UI 侧禁止。
		if model != "" {
			existing.model = model
		}
		if effort != "" {
			existing.effort = effort
		}
		if contextWindow > 0 {
			existing.contextWindow = contextWindow
		}
		if maxTokens > 0 {
			existing.maxTokens = maxTokens
		}
		if disabledMCPServersSet {
			existing.disabledMCPServers = disabledMCPServers
		}
		if disabledSkillsSet {
			existing.disabledSkills = disabledSkills
		}
		// approval_mode 换挡落内存(2026-11-09 修复):此前解析值在此分支整体
		// 丢弃,run.approvalMode 从 session 拷贝恒空串,换挡对执行层零影响,
		// ack 回显陈旧值把前端 chip 弹回「默认推荐」。带键才写(preserve 同纪律)。
		if approvalModeSet {
			existing.approvalMode = approvalMode
		}
		// 显式改键立即同步 journal 头行——这是持久化的唯一写点（2026-10-31
		// 评审修正：不做「每 run 进门同步」，那会把未恢复/未水合的内存态当
		// 权威、把头行里持久化的名单删掉）。未带键的一侧先从当前头行水合内存
		// 再交 preserve 语义同步（评审 F2/F3：单键写入绝不许拿未水合的兄弟
		// 内存值覆盖兄弟持久值）。
		if (disabledMCPServersSet || disabledSkillsSet) && m.coreJournal != nil {
			if !disabledMCPServersSet || !disabledSkillsSet {
				if head, herr := readAgentCoreJournalHeaderFrom(m.coreJournal.dir, sessionID); herr == nil {
					if !disabledMCPServersSet {
						existing.disabledMCPServers = head.disabledMCPServers
					}
					if !disabledSkillsSet {
						existing.disabledSkills = head.disabledSkills
					}
				}
			}
			m.coreJournal.syncSessionSwitches(sessionID, existing.disabledMCPServers, disabledMCPServersSet, existing.disabledSkills, disabledSkillsSet)
		}
		// approval_mode 换挡独立同步 journal(2026-11-09 修复):sync 自带 set
		// 标记(缺键=preserve),此前被裹进上方 MCP/技能键门禁——纯换挡连
		// 头行持久化都不落。
		if approvalModeSet && m.coreJournal != nil {
			m.coreJournal.syncSessionApprovalMode(sessionID, approvalMode, approvalModeSet)
		}
		// 身份三字段空值不覆写已钉值(与 mergeRunStartIdentityLocked 同纪律):
		// 聊天页每条消息前都重发一次 create 且不带 resume/reserved/binding_version
		// 键——无条件覆写会把 confirmBinding 上一轮钉下的原生会话 id 抹空,下一
		// 回合内核拿到空 SessionID 必然重铸(loop.go 空则 newSessionID),
		// confirmBinding 期望旧 id≠实际新 id 拒整回合,会话从第二回合起轮轮必败
		// (2026-11-02 新装机 agentcore 实锤)。非空新值(服务端权威改写)照常
		// 覆盖;撤回 truncate 的 create 刻意不带 resume,同样受益于保留已钉身份。
		if resumeSessionID != "" {
			existing.resumeSessionID = resumeSessionID
		}
		if reservedNativeSessionID != "" {
			existing.reservedNativeSessionID = reservedNativeSessionID
		}
		if bindingVersion != 0 {
			existing.bindingVersion = bindingVersion
		}
		m.seedConfirmedBindingResumeLocked(existing)
		existing.initialContext = initialContext
		existing.isGoalFork = isGoalFork
		if surface != "" {
			existing.surface = surface
		}
		if claudePolicy.enabled {
			existing.claudePolicy = cloneAgentAIClaudeRemotePolicy(claudePolicy)
			existing.claudeCaps = agentAIClaudeCapabilities{}
		}
		if truncateActive {
			// 撤回路径:允许空覆盖（重建结果哪怕是空也必须生效）。
			existing.history = trimAgentAIHistoryForSession(existing, history)
		} else if len(history) > 0 {
			existing.history = trimAgentAIHistoryForSession(existing, history)
		}
		if v := strings.TrimSpace(remoteString(msg, agentAIOriginSocketField)); v != "" {
			existing.originSocket = v
		}
		m.mu.Unlock()
		payload := agentAISessionCreatedPayload(existing)
		if truncateBeforeID != "" {
			payload["truncate_applied"] = truncateApplied
		}
		_ = writeJSON(payload)
		return
	}
	// NOTE: no count cap here. The real turn runs on ai.message, whose lazy-create
	// path (see message()) registers the session unconditionally — so a cap only
	// in create() was a dead check that emitted a misleading "limit reached"
	// error while the conversation still ran. Bound via eviction, not a gate.
	// 2026-10-31 评审修正：新建内存会话时，载荷没带键的那一侧从 journal 头行
	// 兜底恢复——仪表盘设备会话壳经 deviceSessionToConversation 重建后不带这
	// 两个键（服务端也不认识它们），若按 nil 注册，重启后对同一会话的 create
	// 会把持久化的开关「视为未设置」。逐键兜底（评审 F3：单键 create 的兄弟
	// 侧也要兜底）；载荷显式带键时以载荷为权威。
	if provider == "agentcore" && m.coreJournal != nil &&
		(!disabledMCPServersSet || !disabledSkillsSet ||
			(!truncateActive && !payloadHasTranscript)) {
		hist, head, herr := readAgentCoreJournalHistoryFrom(m.coreJournal.dir, sessionID)
		if herr == nil {
			if !disabledMCPServersSet {
				disabledMCPServers = head.disabledMCPServers
			}
			if !disabledSkillsSet {
				disabledSkills = head.disabledSkills
			}
			// 重启失忆修复（2026-11-05）：create 重建内存会话时回装 journal 历史。
			// 读侧与 message() 懒建分支共用 readAgentCoreJournalHistoryFrom
			// （msg/turn 行进历史、tool/approval/end 行跳过、文件顺序即历史顺序，
			// 完全一致）；装回后下方的 limits 截断照常执行（同懒建「装回后裁剪」
			// 口径）。载荷带 transcript 时以载荷为准（既有语义）；撤回 truncate
			// 路径的 history 已按截断后的 journal 重建，不二次回装。
			if !truncateActive && !payloadHasTranscript && len(hist) > 0 {
				// create 的 initial_context 拼接点在解析期（早于懒建的拼接点），
				// 回装不能把它冲掉——按解析期的同款构造还原到首部。
				if initialContext != "" {
					hist = append([]agentAIMessage{{
						Role:      "system",
						MessageID: "initial_context",
						Content:   initialContext,
						CreatedAt: time.Now().UTC(),
					}}, hist...)
				}
				history = hist
			}
		} else if !truncateActive && !payloadHasTranscript && !errors.Is(herr, os.ErrNotExist) {
			// 只在历史确实指望 journal 时才告警：载荷自带 transcript 的会话历史
			// 不缺，journal 读失败只影响开关兜底（镜像懒建分支的告警门）。
			logger.Warn(fmt.Sprintf("ai.session.create: journal history restore failed session=%s err=%v", sessionID, herr))
		}
	}
	// approval_mode 重启兜底(2026-11-09 修复):载荷没带键时从 journal 头行
	// 回填,与上方 MCP/技能开关同纪律(message() 懒建分支一直有此回填,create
	// 侧此前缺失→重启后首个 create 把持久化的挡位静默冲回默认)。独立条件——
	// 上方块的门禁含 MCP/技能/transcript 项,全带键时整块跳过,不能复用。
	if provider == "agentcore" && m.coreJournal != nil && !approvalModeSet {
		if _, head, herr := readAgentCoreJournalHistoryFrom(m.coreJournal.dir, sessionID); herr == nil {
			approvalMode = head.approvalMode
		}
	}
	newHistoryMessages, newHistoryChars := agentAIHistoryLimits(provider, contextWindow)
	session := &agentAISession{
		id:                      sessionID,
		mode:                    mode,
		projectPath:             projectPath,
		provider:                provider,
		model:                   model,
		effort:                  effort,
		contextWindow:           contextWindow,
		maxTokens:               maxTokens,
		disabledMCPServers:      disabledMCPServers,
		disabledSkills:          disabledSkills,
		approvalMode:            approvalMode,
		resumeSessionID:         resumeSessionID,
		reservedNativeSessionID: reservedNativeSessionID,
		bindingVersion:          bindingVersion,
		initialContext:          initialContext,
		isGoalFork:              isGoalFork,
		surface:                 surface,
		originSocket:            strings.TrimSpace(remoteString(msg, agentAIOriginSocketField)),
		claudePolicy:            cloneAgentAIClaudeRemotePolicy(claudePolicy),
		history:                 trimAgentAIHistoryWithLimits(history, newHistoryMessages, newHistoryChars),
		lastActiveAt:            time.Now().UTC(),
	}
	// 新建内存会话同样从 confirmed 绑定供种(重启后首回合 create 不带 resume
	// 键的场景);细节见 seedConfirmedBindingResumeLocked 注释。
	m.seedConfirmedBindingResumeLocked(session)
	m.registerAISessionLocked(session)
	m.mu.Unlock()

	payload := agentAISessionCreatedPayload(session)
	if truncateBeforeID != "" {
		payload["truncate_applied"] = truncateApplied
	}
	_ = writeJSON(payload)
}

func (m *agentAIManager) message(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	if writeJSON == nil {
		return
	}
	sessionID := remoteString(msg, "session_id")
	messageID := remoteString(msg, "message_id")
	content := strings.TrimSpace(remoteString(msg, "content"))
	// CRITICAL: inject structured goal_context into the prompt so the AI knows
	// the task details, required checks, file requirements, etc. Without this,
	// Claude only sees the task title in `content` and has no idea what files
	// to create or what verification will check — causing repeated failures.
	if gc := serializeGoalContext(msg["goal_context"]); gc != "" {
		content = gc + "\n\n" + content
	}
	if sessionID == "" {
		_ = writeJSON(agentAIErrorPayload("", messageID, errors.New("ai.message missing session_id")))
		return
	}
	// message_id 缺省（2026-11-10 聊天记录身份化）：旧实现回落到 sessionID。
	// 那是**同一个会话内所有缺 id 的消息共用一个键**——journal 去重键是
	// sessionID+role+messageID，于是同会话第二条及以后的缺 id 消息全部命中
	// 去重被静默丢弃（用户侧表现：手机上提问网页看不见）。现在改为就地铸造
	// 一个唯一 id，并在下面的 ai.message.received 里回传，客户端可据以对账。
	// 取舍见 resolveAgentAIUserMessageID 注释。
	if mintedID, minted, err := resolveAgentAIUserMessageID(messageID); err != nil {
		_ = writeJSON(agentAIErrorPayload(sessionID, "", errors.New("ai.message missing message_id")))
		return
	} else if minted {
		messageID = mintedID
		logger.Warn(fmt.Sprintf("ai.message: minted message_id session=%s id=%s (client sent none)", sessionID, messageID))
	}
	// 绑定族守卫（镜像 runStart 的先例，见 runStart 内同款注释；Task G3
	// 2026-11-05）：绑定已 confirmed 时，原生会话 id 归 persisted.Provider 所有。
	// v2 面（裸 ai.message，不经 runStart）此前没有这道守卫——服务端设备推断
	// 翻转 / 客户端误带 / 老服务端带来的 provider 会把 agentcore 会话改派给
	// claude CLI 路径（内核原生 id 被喂给 --resume，confirmBinding 必然
	// mismatch，会话从这一回合起永久报错），反方向同理。语义与 runStart 完全
	// 对齐：只改写 provider、不拒绝消息；reserved 绑定不触发（fresh 铸造/预留
	// 是它的正当路径）；claude/claudecode 经 providersEquivalent 视为同族；
	// provider 缺省也强制（缺省但 tool 是外来值时同样会走错 CLI）。
	// 位置在会话解析（含懒建）之前——v3 路径 runStart 也是在把 msg 交给
	// message() 之前改写，懒建分支因此同样受保护；新会话（无绑定）零影响。
	m.mu.Lock()
	if persisted := m.bindings[sessionID]; persisted.NativeSessionID != "" && persisted.State == "confirmed" {
		if requested := firstNonEmpty(remoteString(msg, "provider"), remoteString(msg, "tool")); !providersEquivalent(requested, persisted.Provider) {
			msg["provider"] = persisted.Provider
			msg["tool"] = persisted.Provider
		}
	}
	m.mu.Unlock()
	// #5: a fork (re-planning exploration) session runs READ-ONLY so the AI can't
	// mutate the workspace during exploration. Read the flag from the payload
	// here (the session object is resolved only later); the atomic run.start path
	// carries is_goal_fork, and lazy-create persists it for subsequent turns.
	messageIsGoalFork, _ := msg["is_goal_fork"].(bool)
	messageRun := agentAIRun{
		sessionID: sessionID,
		runID:     remoteString(msg, "run_id"),
		messageID: messageID,
		// origin 必须在本条消息自带标记上取：emitter 在 session 解析（含
		// lazy-create）之前就要建好，读 session.originSocket 只能拿到上一回合
		// 的旧值——换端发起的回合会被误判成旧端 origin，去重失效变成双投。
		originSocketID:         strings.TrimSpace(remoteString(msg, agentAIOriginSocketField)),
		goalIdentity:           goalRunIdentityFromMessage(msg),
		goalRequiredCheckCount: goalRequiredCheckCountFromContext(msg["goal_context"]),
		goalAllowedRoots:       goalAllowedRootsFromContext(msg["goal_context"]),
		goalAllowedCommands:    goalAllowedCommandsFromContext(msg["goal_context"]),
		nativeGoal:             cloneAgentAIMap(mapIf(msg["native_goal"])),
		readOnly:               messageIsGoalFork,
	}
	emitter := m.runEmitter(messageRun, writeJSON)
	runWrite := agentTerminalWriter(emitter.emit)
	// 纯图消息放行（2026-11-20 审计⑦）：content 空+附件在场是合法形态，与
	// journal 写侧（2026-11-05 已改）「text 空且附件空才拒」同口径。旧守卫
	// 在解析 attachments 之前开火，纯图消息被英文 ai.error 拒死。此处只看
	// 原始载荷在场；实质解析/校验仍归 resolveAgentAIAttachments（其错误走
	// 专门的 ai.error 分支）。
	if content == "" && !agentAIMessageHasAttachments(msg["attachments"]) {
		_ = runWrite(agentAIErrorPayload(sessionID, messageID, errors.New("ai.message content is empty")))
		return
	}
	if len(content) > agentAIMessageLimitBytes {
		_ = runWrite(agentAIErrorPayload(sessionID, messageID, fmt.Errorf("ai.message exceeds %d bytes", agentAIMessageLimitBytes)))
		return
	}

	// Acknowledge receipt immediately, before any processing, so the cloud
	// admin's per-turn pipeline can mark "Agent 已收到" (confirmed) distinctly
	// from the server's own "送达 Agent" (dispatched). Mirrors
	// scripts/local-agent.ts. Optional for older agents — the server falls back
	// to "已转发·待确认" when this is absent.
	_ = runWrite(map[string]interface{}{
		"type":        models.AgentEventAIMessageReceived,
		"session_id":  sessionID,
		"message_id":  messageID,
		"received_at": time.Now().UTC().Format(time.RFC3339Nano),
	})

	// 撤回重发 v3 回执（2026-11-14）：runStart 截断成功时向订阅面回执一帧，
	// 手机/网页可据此提示「已撤回锚点后的消息」。ai.run.notice 是已登记的
	// 非致命提示帧（AgentEventAIRunNotice 契约行），老客户端忽略无害。
	if _, applied := msg["truncate_applied"].(bool); applied {
		_ = runWrite(map[string]interface{}{
			"type":       models.AgentEventAIRunNotice,
			"session_id": sessionID,
			"message_id": messageID,
			"code":       "truncate_applied",
		})
	}

	if err := m.preemptForGoalRun(
		sessionID,
		messageRun.runID,
		messageRun.goalIdentity,
		10*time.Second,
	); err != nil {
		_ = runWrite(agentAIErrorPayload(sessionID, messageID, err))
		return
	}

	m.mu.Lock()
	session := m.sessions[sessionID]
	m.mu.Unlock()
	if session == nil {
		// Lazy creation: a session is born on its first ai.message. The prior
		// ai.session.create may have errored (the server validates project_path
		// is AUTHORIZED but not that it EXISTS on disk — a path that's
		// authorized-but-missing fails the agent's create), been dropped /
		// reordered across a flaky link, or never sent. The old behavior — a
		// generic "ai session not found" — ended the turn immediately AND masked
		// the real cause. Register on demand from the message's own fields; if a
		// field is invalid, surface THAT clear error instead. Validation runs
		// outside the manager lock (filesystem ops).
		projectPath, cwdErr := resolveAgentAICWD(remoteString(msg, "project_path"))
		if cwdErr != nil {
			_ = runWrite(agentAIErrorPayload(sessionID, messageID, cwdErr))
			return
		}
		lazyProvider, providerErr := normalizeAgentAIProvider(firstNonEmpty(
			strings.TrimSpace(remoteString(msg, "provider")),
			strings.TrimSpace(remoteString(msg, "tool")),
		))
		if providerErr != nil {
			_ = runWrite(agentAIErrorPayload(sessionID, messageID, providerErr))
			return
		}
		lazyMode := remoteString(msg, "mode")
		if lazyMode == "" {
			lazyMode = "vibe"
		}
		// P0-1 root cause: mirror create()'s initial_context handling. ai.run.start
		// (atomic v3) carries initial_context (the fork snapshot / goal context),
		// but the v1 lazy-create dropped it → a fork session born via run.start
		// never saw the goal snapshot / ALIANG_GOAL_REPLAN_DELTA contract. The
		// server's proactive ai.session.create only covers the online happy path;
		// persisting it here is the robust fix (offline / reconnect / lost create
		// all still converge here on first run.start → message lazy-create).
		lazyInitialContext := strings.TrimSpace(remoteString(msg, "initial_context"))
		lazyIsGoalFork, _ := msg["is_goal_fork"].(bool) // #5: ai.run.start lazy-create
		lazyHistory := remoteAgentAIHistory(msg)
		// 会话级 MCP/技能开关（2026-10-31 持久化）：懒建路径同样吃这两个键——
		// 缺键=不动（此处无既有设置），键存在才落。载荷没带时兜底读 journal
		// 头行（syncSessionSwitches 落的持久化值）：重启后 run.start 直达懒建、
		// 不带 create 的开关键，必须恢复「上次显式设置」而不是静默回滚成全启用。
		lazyDisabledMCP, lazyDisabledMCPSet := parseAgentAIDisabledMCPServers(msg)
		lazyDisabledSkills, lazyDisabledSkillsSet := parseAgentAIDisabledSkills(msg)
		// 跨端/重启续聊（spec §4.3）：agentcore 会话不在内存时，优先从 journal
		// 读回历史装回内存再跑。历史仍只在载荷没带时采纳——载荷那份更新；
		// claude/codex 的上下文在 CLI 自己的会话里，宿主历史只做展示拼 prompt，
		// 不回灌，故不读。读回失败（非 ErrNotExist 的 IO 错）发 ai.run.notice
		// 提示后照常空起——历史损坏不能顶死整轮 run。
		// 2026-10-31 评审修正：头行读取**不能**被「载荷没带历史」门住——手机
		// v3 的 ai.run.start 恒带 transcript（服务端不认识 disabled_mcp_servers/
		// disabled_skills，永远不会补发），若只在无载荷历史时读 journal，重启
		// 后的会话级开关恢复在手机主路径上永远不发生。头行 model/开关兜底照旧
		// 「载荷权威」：载荷显式带的值永远优先。
		// 装回后下方的 limits 截断照常执行（超长历史按会话窗口裁剪，是有意的）。
		var headModel string
		var headSwitches agentCoreJournalHead
		var restoreFailed error
		if lazyProvider == "agentcore" && m.coreJournal != nil {
			if hist, head, herr := readAgentCoreJournalHistoryFrom(m.coreJournal.dir, sessionID); herr == nil {
				if len(lazyHistory) == 0 && len(hist) > 0 {
					lazyHistory = hist
				}
				headModel = head.model
				headSwitches = head
			} else if herr != nil && !errors.Is(herr, os.ErrNotExist) && len(lazyHistory) == 0 {
				// 只在历史确实指望 journal 时才告警：载荷自带 transcript 的会话
				// 历史不缺，journal 读失败只影响开关兜底，不该发「历史没接上」。
				logger.Warn(fmt.Sprintf("ai.message: journal history restore failed session=%s err=%v", sessionID, herr))
				restoreFailed = herr
			}
		}
		// 载荷没带开关键时用头行兜底（与 headModel 的「载荷权威」纪律同款）。
		if !lazyDisabledMCPSet {
			lazyDisabledMCP = headSwitches.disabledMCPServers
		}
		if !lazyDisabledSkillsSet {
			lazyDisabledSkills = headSwitches.disabledSkills
		}
		lazyApprovalMode, lazyApprovalModeSet := parseAgentAIApprovalMode(msg)
		if !lazyApprovalModeSet {
			lazyApprovalMode = headSwitches.approvalMode
		}
		if lazyInitialContext != "" {
			lazyHistory = append([]agentAIMessage{{
				Role:      "system",
				MessageID: "initial_context",
				Content:   lazyInitialContext,
				CreatedAt: time.Now().UTC(),
			}}, lazyHistory...)
		}
		lazyContextWindow := clampAgentAIContextWindow(remoteInt(msg, "context_window", 0))
		lazyHistoryMessages, lazyHistoryChars := agentAIHistoryLimits(lazyProvider, lazyContextWindow)
		session = &agentAISession{
			id:                      sessionID,
			mode:                    lazyMode,
			projectPath:             projectPath,
			provider:                lazyProvider,
			model:                   firstNonEmpty(strings.TrimSpace(remoteString(msg, "model")), headModel),
			effort:                  strings.TrimSpace(remoteString(msg, "effort")),
			approvalMode:            lazyApprovalMode,
			contextWindow:           lazyContextWindow,
			resumeSessionID:         firstNonEmpty(remoteString(msg, "resume_session_id"), remoteString(msg, "source_session_id")),
			reservedNativeSessionID: strings.TrimSpace(remoteString(msg, "reserved_native_session_id")),
			bindingVersion:          remoteInt(msg, "binding_version", 0),
			claudePolicy:            cloneAgentAIClaudeRemotePolicy(parseAgentAIClaudeRemotePolicy(msg)),
			disabledMCPServers:      lazyDisabledMCP,
			disabledSkills:          lazyDisabledSkills,
			initialContext:          lazyInitialContext,
			isGoalFork:              lazyIsGoalFork,
			surface:                 normalizeAgentAISurface(remoteString(msg, "surface")),
			history:                 trimAgentAIHistoryWithLimits(lazyHistory, lazyHistoryMessages, lazyHistoryChars),
			lastActiveAt:            time.Now().UTC(),
		}
		m.mu.Lock()
		if existing := m.sessions[sessionID]; existing != nil {
			session = existing // a concurrent create/message registered it first
		} else {
			// 懒建同样从 confirmed 绑定供种:手机/仪表盘的裸 ai.message 在
			// agent 重启后直达懒建,不带 resume 键(细节见 helper 注释)。
			m.seedConfirmedBindingResumeLocked(session)
			m.registerAISessionLocked(session)
		}
		m.mu.Unlock()
		// 装回失败提示（在会话落地之后发）：经 runWrite=emitter→订阅扇出，
		// 手机/跨端订阅者都能看到「历史没接上、本轮空跑」这一事实。
		if restoreFailed != nil {
			_ = runWrite(map[string]interface{}{
				"type":       models.AgentEventAIRunNotice,
				"session_id": sessionID,
				"message_id": messageID,
				"code":       "history_restore_failed",
				"detail":     restoreFailed.Error(),
			})
		}
	}
	m.mu.Lock()
	if policy := parseAgentAIClaudeRemotePolicy(msg); policy.enabled {
		session.claudePolicy = cloneAgentAIClaudeRemotePolicy(policy)
		session.claudeCaps = agentAIClaudeCapabilities{}
	}
	// 会话级开关的「键存在才整体替换」合并（与 create 同语义，2026-10-31）：
	// ai.message / ai.run.start 载荷带了 disabled_mcp_servers/disabled_skills
	// 就整体替换——普通回合不带键，自然零影响。缺这半边时，懒建之外的存量
	// 会话对 run.start 携带的开关键视而不见（验收 D1 的另一半）。显式改键时
	// 同步 journal 头行（与 create 同为持久化写点）；未带键的一侧先从头行
	// 水合内存再交 preserve 语义同步（评审 F2/F3，同 create 处注释）。
	mcpSet := false
	skillsSet := false
	if lists, set := parseAgentAIDisabledMCPServers(msg); set {
		session.disabledMCPServers = lists
		mcpSet = true
	}
	if lists, set := parseAgentAIDisabledSkills(msg); set {
		session.disabledSkills = lists
		skillsSet = true
	}
	if (mcpSet || skillsSet) && m.coreJournal != nil {
		if !mcpSet || !skillsSet {
			if head, herr := readAgentCoreJournalHeaderFrom(m.coreJournal.dir, sessionID); herr == nil {
				if !mcpSet {
					session.disabledMCPServers = head.disabledMCPServers
				}
				if !skillsSet {
					session.disabledSkills = head.disabledSkills
				}
			}
		}
		m.coreJournal.syncSessionSwitches(sessionID, session.disabledMCPServers, mcpSet, session.disabledSkills, skillsSet)
	}
	if approvalMode, ok := parseAgentAIApprovalMode(msg); ok {
		// 内存写入(2026-11-09 修复):此前只同步 journal 不落内存,
		// run.approvalMode 从 session 拷到的是陈旧值,ai.message 换挡对执行
		// 层零影响。journal 同步保留 nil 守卫(syncSessionApprovalMode 首行
		// 自判 j==nil,双保险仅为显式可读)。
		session.approvalMode = approvalMode
		if m.coreJournal != nil {
			m.coreJournal.syncSessionApprovalMode(sessionID, approvalMode, ok)
		}
	}
	// 撤回重发 v3：runStart 截断后注入的截断 history（键存在即消费，值类型
	// 断言失败视为未带键）。空切片合法（撤回到空）——必须允许空覆盖，否则
	// 截断后的会话内存里仍留着旧行，模型从本回合起看见已撤回的上下文。
	// 懒建分支不经此处：journal 已截断，装回的天然就是截断后历史。
	if historyFrame, hasFrame := msg["history"].([]agentAIMessage); hasFrame {
		session.history = trimAgentAIHistoryForSession(session, historyFrame)
	}
	if surface := normalizeAgentAISurface(remoteString(msg, "surface")); surface != "" {
		session.surface = surface
	}
	if cw := clampAgentAIContextWindow(remoteInt(msg, "context_window", 0)); cw > 0 {
		session.contextWindow = cw
	}
	if v := strings.TrimSpace(remoteString(msg, agentAIOriginSocketField)); v != "" {
		session.originSocket = v
	}
	session.lastActiveAt = time.Now().UTC() // LRU recency: this conversation is active
	if session.cancel != nil {
		m.mu.Unlock()
		_ = runWrite(agentAIErrorPayload(sessionID, messageID, fmt.Errorf("ai session is already running: %s", sessionID)))
		return
	}
	m.mu.Unlock()

	// Phone-driven runs can arrive while the Mac is unattended. The project may
	// live under a macOS privacy-protected folder (Desktop/Documents/Downloads/
	// iCloud/external volume): touching it would raise a "Files & Folders" (TCC)
	// prompt nobody is there to answer. tccguard refuses with an actionable
	// consent error before the first disk access (goal capture, attachments and
	// provider spawn all touch the project), and probes the category once when a
	// human IS at the Mac, remembering the grant.
	if guardErr := tccguard.GuardPath(session.projectPath); guardErr != nil {
		_ = runWrite(agentAIErrorPayload(sessionID, messageID, guardErr))
		return
	}

	provider, err := normalizeAgentAIProvider(firstNonEmpty(strings.TrimSpace(remoteString(msg, "provider")), strings.TrimSpace(remoteString(msg, "tool")), session.provider))
	if err != nil {
		_ = runWrite(agentAIErrorPayload(sessionID, messageID, err))
		return
	}
	attachments, err := resolveAgentAIAttachments(msg["attachments"], session.projectPath)
	if err != nil {
		_ = runWrite(agentAIErrorPayload(sessionID, messageID, err))
		return
	}
	if len(messageRun.goalIdentity) > 0 {
		if err := emitter.captureGoalWorkspace(session.projectPath); err != nil {
			_ = runWrite(agentAIErrorPayload(sessionID, messageID, fmt.Errorf("goal workspace fingerprint failed: %w", err)))
			return
		}
	}

	// Slash-command dispatcher: agent-local builtins (/clear /model /help /cost
	// /compact) run WITHOUT a model turn. The Go agent drives the CLI headlessly
	// (claude --print --resume / codex app-server --stdio), so there is no live
	// REPL to receive interactive slash commands — these are direct session
	// operations (/clear) or honest status replies (/model /help /cost /compact).
	// Prompt-style commands (/review, custom .claude/commands) and unknown /xxx
	// fall through to the normal model turn below.
	if m.handleLocalSlashCommand(session, messageID, content, provider, runWrite) {
		return
	}

	if err := m.runUserMessage(session, remoteString(msg, "run_id"), messageID, content, provider, attachments, remoteStringSlice(msg, "attachment_allowed_hosts"), emitter, messageRun.nativeGoal); err != nil {
		_ = runWrite(agentAIErrorPayload(sessionID, messageID, err))
	}
}

// runStart is the protocol-v3 atomic create+message command. The server may
// redeliver it from its durable outbox; replayProcessedRun guarantees the same
// run never launches a second provider process.
// mergeRunStartIdentityLocked 把 ai.run.start 携带的身份字段并进会话。空值不
// 覆盖已钉值（与 attachLiveSession 的字段纪律同款）：server 重投/重连重放的
// payload 常不带 resume/reserved 字段，空串覆盖会把上一轮 confirmBinding 钉下
// 的恢复点抹掉，下一回合退化 fresh（2026-10-29 already-in-use 事故第 3 层）。
// 非空新值（含绑定 confirmed 的权威改写）正常覆盖。调用方必须持有 m.mu。

func mergeRunStartIdentityLocked(session *agentAISession, resumeID, reservedID string) {
	if resumeID != "" {
		session.resumeSessionID = resumeID
	}
	if reservedID != "" {
		session.reservedNativeSessionID = reservedID
	}
}

// seedConfirmedBindingResumeLocked 在会话侧 resume 恢复点为空时,从 confirmed
// 绑定供种(镜像 runStart 的绑定反推分支)。v2 面(create/message 裸走)在
// agent 重启丢内存后同样要拿回绑定钉下的原生 id,否则内核重新铸造 →
// confirmBinding mismatch。reserved 绑定不供种(fresh 铸造/预留是它的正当
// 路径);provider 不同族不供种——跨端污染警报(confirmBinding mismatch)是
// 2026-10-27/10-29 两轮事故建起来的线,不许在这里被悄悄拆掉。
// 调用方必须持有 m.mu。

func (m *agentAIManager) seedConfirmedBindingResumeLocked(session *agentAISession) {
	if session.resumeSessionID != "" {
		return
	}
	b, ok := m.bindings[session.id]
	if !ok || b.State != "confirmed" || b.NativeSessionID == "" {
		return
	}
	if !providersEquivalent(session.provider, b.Provider) {
		return
	}
	session.resumeSessionID = b.NativeSessionID
}

// runStartProjectPath 是 ai.run.start 收 project_path 的唯一入口:空值沿用
// 既有;解析(resolveAgentAICWD,含 ~ 展开)通过才覆盖;解析失败保留既有值
// 并留痕。返回 (收下的值, 是否发生覆盖)。
//
// 2026-11-25 线上事故:这里曾把载荷字符串裸赋值进 session(与 create() 的
// resolveAgentAICWD 校验纪律不一致),手机端目录解析不出时兜底发出的字面
// "~" 直达执行层,整局会话一碰 shell 就死在 chdir,直到触发防重试循环
// 保险丝。脏 cwd 不允许打断续聊——解析失败一律保留既有值。
func runStartProjectPath(raw, existing, sessionID string) (string, bool) {
	candidate := strings.TrimSpace(raw)
	if candidate == "" {
		return existing, false
	}
	resolved, err := resolveAgentAICWD(candidate)
	if err != nil {
		logger.Warn(fmt.Sprintf("ai.run.start project_path rejected, keep existing session=%s raw=%q: %v", sessionID, candidate, err))
		return existing, false
	}
	if resolved == existing {
		return existing, false
	}
	return resolved, true
}

func (m *agentAIManager) runStart(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	runID := strings.TrimSpace(remoteString(msg, "run_id"))
	sessionID := strings.TrimSpace(remoteString(msg, "session_id"))
	messageID := strings.TrimSpace(remoteString(msg, "message_id"))
	if runID == "" || sessionID == "" || messageID == "" {
		_ = writeJSON(agentAIErrorPayload(sessionID, messageID, errors.New("ai.run.start missing session_id, run_id, or message_id")))
		return
	}
	if m.replayProcessedRun(runID, writeJSON) {
		return
	}
	claimed, claimErr := m.claimProcessedRun(
		runID,
		sessionID,
		messageID,
		goalRunIdentityFromMessage(msg),
	)
	if claimErr != nil {
		// Do not launch without a durable claim and do not emit a terminal run
		// event: the server outbox must remain pending and retry after storage
		// recovers.
		logger.Warn(fmt.Sprintf("ai.run.start durable claim failed run=%s: %v", runID, claimErr))
		return
	}
	if !claimed {
		m.replayProcessedRun(runID, writeJSON)
		return
	}

	reservedID := strings.TrimSpace(remoteString(msg, "reserved_native_session_id"))
	resumeID := firstNonEmpty(
		remoteString(msg, "resume_session_id"),
		remoteString(msg, "source_session_id"),
		remoteString(msg, "native_session_id"),
	)
	provider := firstNonEmpty(remoteString(msg, "provider"), remoteString(msg, "tool"), "auto")
	bindingVersion := remoteInt(msg, "binding_version", 1)
	if err := m.reserveBinding(sessionID, provider, reservedID, bindingVersion); err != nil {
		// The run was already durably claimed. Emit through the normal v2 terminal
		// barrier so the cloud can settle it and a redelivery replays this exact
		// terminal instead of leaving a permanently "received" processed-run row.
		emitter := m.runEmitter(agentAIRun{
			runID: runID, sessionID: sessionID, messageID: messageID,
			// 错误回包也要带 origin：fan-out 去重靠它跳过发起通道，Task 7 的
			// 审批 ack 双写同样按 origin 寻址。
			originSocketID: strings.TrimSpace(remoteString(msg, agentAIOriginSocketField)),
			goalIdentity:   goalRunIdentityFromMessage(msg),
		}, writeJSON)
		_ = emitter.emit(agentAIErrorPayload(sessionID, messageID, err))
		return
	}
	m.mu.Lock()
	if persisted := m.bindings[sessionID]; persisted.NativeSessionID != "" {
		if persisted.State == "confirmed" {
			resumeID = persisted.NativeSessionID
			reservedID = ""
			// 绑定守卫（2026-10-27 native session id mismatch 事故根治的 agent
			// 端半边）：绑定已 confirmed 时，resume id 归 persisted.Provider 所有。
			// 消息里带来的 provider（服务端设备推断翻转 / 客户端误带 / 老服务端）
			// 若与绑定不同族，以绑定为权威强制改写 msg——否则 CLI 拿内核的原生 id
			// 去 --resume，confirmBinding 必然 mismatch，会话从这一回合起永久报错。
			// 缺省也强制：provider 缺省但 tool 是外来值时同样会走错 CLI。
			// claude/claudecode 经 providersEquivalent 视为同族，不触发强制。
			if requested := firstNonEmpty(remoteString(msg, "provider"), remoteString(msg, "tool")); !providersEquivalent(requested, persisted.Provider) {
				msg["provider"] = persisted.Provider
				msg["tool"] = persisted.Provider
			}
		} else {
			if reservedID == "" {
				reservedID = persisted.NativeSessionID
			}
			// reserved 自愈（2026-10-29 already-in-use 事故的入口保险）：server
			// 判死吞掉终态 confirm 后，reserved 身份对应的 jsonl 可能已由上一轮
			// 真实落盘。此时仍按 fresh --session-id 启动必撞 "Session ID already
			// in use" exit 1，且绑定恒停 reserved、每回合重试必死。探到落盘事实
			// 即把本回合升级为 --resume，恢复点交由本轮 confirmBinding 正常收敛。
			// 仅 claude 族有 jsonl 概念；codex 无从探测，由 codex 侧 mismatch
			// 自愈兜底。
			if persisted.State == "reserved" && resumeID == "" && reservedID != "" &&
				providersEquivalent(persisted.Provider, "claudecode") {
				if jsonl := ccPeerSessionJSONL(externalTUIHome(), reservedID); jsonl != "" {
					logger.Info(fmt.Sprintf("ai.run.resume.revive: session=%s reserved id %q jsonl 已落盘(%s)，fresh 自动升级 --resume", sessionID, reservedID, jsonl))
					resumeID = reservedID
					reservedID = ""
				}
			}
		}
	}
	if session := m.sessions[sessionID]; session != nil {
		session.mode = firstNonEmpty(remoteString(msg, "mode"), session.mode, "vibe")
		// project_path 只收解析通过的值(2026-11-25 事故);空值沿用既有——
		// 与下方 model/effort 的「非空才覆盖」纪律同构。
		if resolved, ok := runStartProjectPath(remoteString(msg, "project_path"), session.projectPath, sessionID); ok {
			session.projectPath = resolved
		}
		session.provider = firstNonEmpty(remoteString(msg, "provider"), remoteString(msg, "tool"), session.provider)
		session.model = strings.TrimSpace(remoteString(msg, "model"))
		session.effort = strings.TrimSpace(remoteString(msg, "effort"))
		if approvalMode, ok := parseAgentAIApprovalMode(msg); ok {
			session.approvalMode = approvalMode
		}
		if cw := clampAgentAIContextWindow(remoteInt(msg, "context_window", 0)); cw > 0 {
			session.contextWindow = cw
		}
		mergeRunStartIdentityLocked(session, resumeID, reservedID)
		session.bindingVersion = remoteInt(msg, "binding_version", session.bindingVersion)
		if v := strings.TrimSpace(remoteString(msg, agentAIOriginSocketField)); v != "" {
			session.originSocket = v
		}
		if policy := parseAgentAIClaudeRemotePolicy(msg); policy.enabled {
			session.claudePolicy = cloneAgentAIClaudeRemotePolicy(policy)
			session.claudeCaps = agentAIClaudeCapabilities{}
		}
	}
	m.mu.Unlock()

	// 撤回重发 v3 通道（2026-11-14）：ai.run.start 携带 truncate_before_message_id
	// 时，在本回合开跑前截断 journal 到锚点行之前，并从截断后 journal 全文重建
	// history——与 create() 的截断纪律逐条对齐（spec §A.2 的 v3 面）：
	//   1. 活跃拒绝先于任何 journal 写（cancel != nil 即 runAgentCore defer LIFO
	//      已落 end 行的清位锚）——被拒时 journal 必须原封不动；
	//   2. 截断历史按「文件位置语义」幂等（outbox 重投收敛），截断成功后文件
	//      必在，ErrNotExist 一律拒绝绝不静默回退；
	//   3. 截断后的 history 注入 msg["history"]（message() 消费，见下），锚点
	//      后的 transcript 不会经由任何路径回灌。
	// 重建结果的内存落地在 message() 内完成：懒建分支装回截断后 journal（天然
	// 一致）；存量会话分支消费 msg["history"] 覆盖（historyFrame != nil 门）。
	// 与 create 相同，provider != agentcore 时静默忽略锚点（v2 语义）。
	truncateBeforeID := strings.TrimSpace(remoteString(msg, "truncate_before_message_id"))
	if truncateBeforeID != "" && provider == "agentcore" {
		m.mu.Lock()
		existingPeek := m.sessions[sessionID]
		busy := existingPeek != nil && existingPeek.cancel != nil
		m.mu.Unlock()
		if busy {
			emitter := m.runEmitter(agentAIRun{
				runID: runID, sessionID: sessionID, messageID: messageID,
				originSocketID: strings.TrimSpace(remoteString(msg, agentAIOriginSocketField)),
				goalIdentity:   goalRunIdentityFromMessage(msg),
			}, writeJSON)
			_ = emitter.emit(agentAIErrorPayload(sessionID, messageID, &agentAITruncateError{code: truncateErrCodeRunActive, msg: "ai.run.start truncate rejected: run active"}))
			return
		}
		if m.coreJournal == nil {
			_ = writeJSON(agentAIErrorPayload(sessionID, messageID, &agentAITruncateError{code: truncateErrCodeJournalUnavailable, msg: "ai.run.start truncate unavailable: journal disabled"}))
			return
		}
		if _, err := m.coreJournal.truncateBefore(sessionID, truncateBeforeID); err != nil {
			_ = writeJSON(agentAIErrorPayload(sessionID, messageID, &agentAITruncateError{code: truncateErrCodeFailed, msg: "ai.run.start truncate failed", err: err}))
			return
		}
		hist, _, rerr := readAgentCoreJournalHistoryFrom(m.coreJournal.dir, sessionID)
		if rerr != nil {
			// 截断成功后文件必在；ErrNotExist 只会出现在「从未落过 journal」
			// 的 no-op 截断上——同样拒绝，绝不静默回退空 history（镜像
			// create 同款注释）。
			_ = writeJSON(agentAIErrorPayload(sessionID, messageID, &agentAITruncateError{code: truncateErrCodeRebuildFailed, msg: "ai.run.start rebuild failed", err: rerr}))
			return
		}
		// 交给 message()：懒建分支直接装回；存量分支经 historyFrame 门覆盖。
		// 空 history 合法（撤回到空），保持键存在、值为空切片。
		msg["history"] = hist
		msg["truncate_applied"] = true
	}

	msg["resume_session_id"] = resumeID
	msg["reserved_native_session_id"] = reservedID
	m.message(msg, writeJSON)
}

// parseLocalSlashCommand recognizes a leading "/<name>" with optional args.
// Returns ok=false when content is not a slash command or the name is empty.
// Callers further filter to the agent-local builtin set; anything else falls
// through to a normal model turn.

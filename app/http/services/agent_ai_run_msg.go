package services

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/internal/tccguard"
)

func (m *agentAIManager) runUserMessage(session *agentAISession, runID, messageID, content, provider string, attachments []agentAIAttachment, attachmentAllowedHosts []string, emitter *agentAIRunEmitter, nativeGoal map[string]interface{}) error {
	// continued-in 重定向（2026-10-30 1dc69343 事故）：resume 目标被 /fork/
	// continuation 续走（尾部标记且标记后无对话活动）时改指续接会话——否则
	// TUI 注入会落进不消化的退役身份（消息入队永不 dequeue → 10 分钟零输出
	// idle_timeout），headless --resume 也挂在幽灵转录上。必须先于 TUI busy
	// 预检，让预检评估的是重定向后的真实目标。
	m.redirectAgentAIContinuedSession(session, provider)
	// Spawn precheck (imported Claude sessions): refuse to --resume a native
	// session whose interactive TUI is mid-turn — two writers on one native
	// conversation would interleave transcripts. Earliest abortable point: no
	// run state below has been mutated yet, so returning here surfaces as a
	// plain ai.error and the server can retry after the TUI turn finishes.
	if err := guardExternalTUIClaudeSpawn(externalTUIHome(), provider, session.resumeSessionID); err != nil {
		return err
	}
	writeJSON := agentTerminalWriter(emitter.emit)
	approvalToken, err := newAgentAIApprovalToken()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	activity := newAgentAIActivity()
	now := time.Now().UTC()

	m.mu.Lock()
	session.cancel = cancel
	session.activeWriter = writeJSON
	session.approvalToken = approvalToken
	session.activity = activity
	session.runSeq++
	session.activeRunID = runID
	session.goalIdentity = cloneGoalIdentity(emitter.goalIdentity)
	session.goalAllowedRoots = emitter.goalAllowedRoots
	session.goalAllowedCommands = emitter.goalAllowedCommands
	resumeSessionID := strings.TrimSpace(session.resumeSessionID)
	reservedNativeSessionID := strings.TrimSpace(session.reservedNativeSessionID)
	// A raw /compact for claude must reach the CLI unchanged: the local command
	// is the compaction trigger, and any agent-side wrapping turns it into an
	// ordinary user message (the CLI then answers text instead of compacting).
	nativeCompact := agentAINativeCompactTurn(content, provider, resumeSessionID)
	logger.Info(fmt.Sprintf("ai.run: session=%s runSeq=%d resumeSessionID=%q (empty=fresh claude run this turn; non-empty=--resume)", session.id, session.runSeq, resumeSessionID))
	if !nativeCompact {
		session.history = append(session.history, agentAIMessage{
			Role:      "user",
			MessageID: messageID,
			Content:   content,
			CreatedAt: now,
		})
	}
	freshPrompt := content
	if !nativeCompact {
		freshPrompt = buildAgentAIPrompt(session, content)
	}
	// headless 通道（claude -p / codex / agentcore）不带手机行为契约：那是
	// 宿主拉起的进程，约束走 settings/permissions 与 append-system-prompt，
	// 见 agent_ai_phone_contract.go 头注释。契约只在 TUI 注入路由里加。
	prompt := freshPrompt
	if resumeSessionID != "" {
		prompt = content
	}
	run := agentAIRun{
		sessionID:               session.id,
		runID:                   runID,
		messageID:               messageID,
		runSeq:                  session.runSeq,
		mode:                    session.mode,
		projectPath:             session.projectPath,
		provider:                provider,
		model:                   session.model,
		effort:                  session.effort,
		approvalMode:            session.approvalMode,
		contextWindow:           session.contextWindow,
		maxTokens:               session.maxTokens,
		disabledMCPServers:      session.disabledMCPServers,
		disabledSkills:          session.disabledSkills,
		resumeSessionID:         resumeSessionID,
		reservedNativeSessionID: reservedNativeSessionID,
		bindingVersion:          session.bindingVersion,
		prompt:                  prompt,
		freshPrompt:             freshPrompt,
		attachments:             append([]agentAIAttachment(nil), attachments...),
		attachmentAllowedHosts:  append([]string(nil), attachmentAllowedHosts...),
		cancel:                  cancel,
		approvalToken:           approvalToken,
		activity:                activity,
		subagents:               newAgentAISubagentRegistry(),
		claudePolicy:            cloneAgentAIClaudeRemotePolicy(session.claudePolicy),
		goalIdentity:            cloneGoalIdentity(emitter.goalIdentity),
		goalAllowedRoots:        emitter.goalAllowedRoots,
		goalAllowedCommands:     emitter.goalAllowedCommands,
		nativeGoal:              cloneAgentAIMap(nativeGoal),
		// #5 (v2): enforce read-only on the EXECUTION run for fork sessions.
		// v1 set this only on the temporary messageRun, which is discarded before
		// the real run is built here — so it never reached runCLIPass/codex.
		readOnly: session.isGoalFork,
		surface:  session.surface,
		// 执行 run 也带 origin（fan-out 去重走 emitter 侧的 messageRun 值，
		// 这份供审批 ack 双写等 run 级消费方用；临界区内读，与 session 快照同源）。
		originSocketID: session.originSocket,
	}
	run.onClaudeInit = func(commands []string, version string) {
		m.recordClaudeCapabilities(run.sessionID, run.projectPath, commands, version)
	}
	m.mu.Unlock()

	// TUI 执行路由（spec §4.1）：原生 TUI 开着且空闲时，把这一回合送进那个 TUI
	// 执行——TUI 与手机天然共享上下文、同看一次对话，TUI 自动更新。
	//
	// 三个不变量：
	//   ① 判定发生在任何 provider 进程/spawn 状态之前；
	//   ② 注入失败不回退 spawn（spec §4.5）——一次回合只走一条路；
	//   ③ 释放本函数前段为 spawn 准备的 ctx，避免悬挂 goroutine。
	//
	// 位置刻意选在 ensurePolicyBeforeRun / startAIWatchdog / runCLI 之前：前者是
	// spawn 路径的策略同步（注入回合用 TUI 自己的权限模式），中者的 watchdog 会因
	// TUI 回合没有子进程 stdout 而误判 idle 超时。
	if plan := tuiTurnDecide(tuiTurnInput{
		Home:            externalTUIHome(),
		Provider:        provider,
		NativeSessionID: run.resumeSessionID,
		ReadOnly:        run.readOnly,
		HasGoalIdentity: len(run.goalIdentity) > 0,
		JSONLPath:       ccPeerSessionJSONL(externalTUIHome(), run.resumeSessionID),
	}); plan != nil && !nativeCompact {
		// 释放 spawn 路径预建的 ctx（执行器自建 runCtx，不再用这个）。
		cancel()
		// 传原始 content 而非 run.prompt（后者被 buildAgentAIPrompt 包装过）；
		// 附件走 run.attachments，由执行器拼进注入文本。手机行为契约也在执行器
		// 里按 run.surface 追加到注入文本——history 与注入文本因此各归各位：
		// 历史只存原文，契约只存在于 TUI 收到的那一帧。
		go m.runTUITurn(context.Background(), session, run, content, plan, emitter)
		return nil
	}

	// Best-effort sync of the device approval policy before the run starts, so a
	// just-changed policy (or a fresh process with only the built-in default)
	// picks up the current rules. Never blocks beyond a short timeout or errors
	// the turn; failures keep the cached/built-in policy.
	if svc := m.approvalService(); svc != nil {
		svc.ensurePolicyBeforeRun(ctx, session.projectPath)
	}
	run.guard = m.attachRunGuard(run, emitter.emit)
	m.startAIWatchdog(ctx, activity, cancel)
	m.mu.Lock()
	if current := m.sessions[run.sessionID]; current != nil && current.runSeq == run.runSeq {
		// Approval HTTP hooks emit asynchronously through activeWriter. Point them
		// at the same per-run emitter so they share run_id/event_seq and cannot
		// escape the terminal barrier through the original socket writer.
		current.activeWriter = emitter.emit
	}
	m.mu.Unlock()
	go m.runCLI(ctx, run, emitter.emit)
	return nil
}

// optionResponse 处理用户对 ai.option.request 的选择：清除 pendingOption、
// 广播 resolved 同型帧（跨端翻卡），并按选择结果续接一轮新的 AI run。

func (m *agentAIManager) optionResponse(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	if writeJSON == nil {
		return
	}
	sessionID := remoteString(msg, "session_id")
	optionID := strings.TrimSpace(remoteString(msg, "option_id"))
	if sessionID == "" || optionID == "" {
		_ = writeJSON(agentAIErrorPayload(sessionID, remoteString(msg, "message_id"), errors.New("ai.option.response missing session_id or option_id")))
		return
	}
	selected := remoteStringSlice(msg, "selected")
	custom := strings.TrimSpace(remoteString(msg, "custom_text"))
	originSock := strings.TrimSpace(remoteString(msg, agentAIOriginSocketField))
	// 决策跨端同步（2026-11-08）：sync_only 表示「该决策的续跑已由发起端
	// （PhoneServer 手机决议 dispatchUserAiMessage）自行负责，agent 只需清卡 +
	// 广播翻卡帧」——再走续跑路径会把同一选择双重注入模型。不喂 codex waiter：
	// 活跑的 requestUserInput 等待态不被旁路注入答案。
	syncOnly := remoteBool(msg, "sync_only", false)

	m.mu.Lock()
	inputKey := agentAICodexInputMapKey(sessionID, optionID)
	if waiter := m.codexInputs[inputKey]; waiter != nil && waiter.sessionID == sessionID {
		if syncOnly {
			m.mu.Unlock()
			m.emitOptionResolved(sessionID, optionID, "codex", "", selected, custom, originSock, writeJSON)
			return
		}
		// 刻意不刷新 session.originSocket（Task 7 评审落档）：codex-input 答复
		// 续接的是正在运行的旧 run（经 waiter.respond 回传，不起新 run）——
		// 把 origin 迁到答复方会劫持 session.originSocket，把该 run 后续审批
		// ack 从发起通道路由走，复现 ack 双写要修的投递滞留。
		delete(m.codexInputs, inputKey)
		m.mu.Unlock()
		waiter.respond <- agentAICodexInputAnswer{selected: selected, custom: custom}
		// 跨端翻卡（2026-11-08）：此前此分支零帧提前 return，其他端选项卡永
		// pending——resolved 同型帧经 fanOut 广播（ai.option.request 在审批族
		// 白名单），dashboard aisock 与手机 WS 均可达。
		m.emitOptionResolved(sessionID, optionID, "codex", "", selected, custom, originSock, writeJSON)
		return
	}
	session := m.sessions[sessionID]
	// 跨端同态（Task 7 前置）：option 作答同样刷新 session.originSocket——
	// option 续接的执行 run 与其后续审批 ack 都要写回「作答这条 socket」，
	// 不刷新的话执行 run 还带着上一回合的旧 origin，ack 双写会写到旧通道。
	// 盖章只来自两条 WS 分发路径（远端/本机），未盖章（测试内部旧调用方）
	// 不动；未匹配/过期的 option 作答同样刷新 origin——与 message() 的
	// 「origin 跟随最近盖章入站」语义一致。
	if session != nil {
		if originSock != "" {
			session.originSocket = originSock
		}
	}
	if session == nil || session.cancel != nil || session.pendingOption == nil || session.pendingOption.ID != optionID {
		m.mu.Unlock()
		if syncOnly {
			// 迟到/重复 sync 幂等翻卡（2026-11-08）：卡已不在场（另一端已消费）
			// 时发起端的翻卡信号不能丢——发出口径与命中一致，前端按已作答键位
			// 天然幂等吸收。
			m.emitOptionResolved(sessionID, optionID, "", "", selected, custom, originSock, writeJSON)
		}
		return
	}
	pending := session.pendingOption
	session.pendingOption = nil
	provider := session.provider
	m.mu.Unlock()

	// 先广播翻卡帧再起续跑（2026-11-08）：其他端先看到「已选」，再看到续跑
	// 回合开跑；此时锁已释放，emit 不入锁（历史自锁死教训）。
	m.emitOptionResolved(sessionID, optionID, provider, pending.MessageID, selected, custom, originSock, writeJSON)
	if syncOnly {
		return
	}

	content := buildAgentAIOptionFollowup(pending, selected, custom)
	messageID := pending.MessageID + ".option"

	// option 续接回合同样盖 origin：不补的话，发起 socket 已注册时会出现
	// 「内联 + 扇出」双投（无去重扇出）。
	run := agentAIRun{
		sessionID:      sessionID,
		runID:          remoteString(msg, "run_id"),
		messageID:      messageID,
		originSocketID: originSock,
	}
	emitter := m.runEmitter(run, writeJSON)
	if err := m.runUserMessage(session, remoteString(msg, "run_id"), messageID, content, provider, nil, nil, emitter, nil); err != nil {
		_ = emitter.emit(agentAIErrorPayload(sessionID, messageID, err))
	}
}

// emitOptionResolved option 作答落地后的跨端翻卡帧（2026-11-08 决策跨端同步）：
// 镜像审批族 status:"resolved" 同型约定（requestApproval 的 <-respond 分支重发
// ai.approval.request）——ai.option.request 在审批族广播白名单里，经
// fanOutWrite 全端可达（dashboard aisock + 手机 WS），PhoneServer 依此 settle
// 派生审批、前端依此翻卡。originSockID 为空（测试/内部路径未盖章）退化为
// writeJSON 直发，与既有 emit* 函数口径一致。

func (m *agentAIManager) emitOptionResolved(sessionID, optionID, provider, messageID string, selected []string, customText, originSockID string, writeJSON agentTerminalWriter) {
	if writeJSON == nil || sessionID == "" || optionID == "" {
		return
	}
	payload := map[string]interface{}{
		"type":       models.AgentEventAIOptionRequest,
		"session_id": sessionID,
		"option_id":  optionID,
		"status":     "resolved",
		"selected":   selected,
	}
	if provider != "" {
		payload["provider"] = provider
	}
	if messageID != "" {
		payload["message_id"] = messageID
	}
	if customText != "" {
		payload["custom_text"] = customText
	}
	write := writeJSON
	if originSockID != "" {
		write = m.fanOutWrite(originSockID, sessionID, writeJSON)
	}
	_ = write(payload)
}

func agentAICodexInputMapKey(sessionID, optionID string) string {
	return sessionID + "\x00" + optionID
}

func agentAISessionCreatedPayload(session *agentAISession) map[string]interface{} {
	payload := map[string]interface{}{
		"type":          models.AgentEventAISessionCreated,
		"session_id":    session.id,
		"mode":          session.mode,
		"project_path":  session.projectPath,
		"provider":      session.provider,
		"model":         session.model,
		"effort":        session.effort,
		"approval_mode": session.approvalMode,
		"state":         "idle",
	}
	if session.resumeSessionID != "" {
		payload["resume_session_id"] = session.resumeSessionID
	}
	if session.contextWindow > 0 {
		payload["context_window"] = session.contextWindow
	}
	return payload
}

func remoteAgentAIHistory(msg map[string]interface{}) []agentAIMessage {
	raw, ok := msg["transcript"].([]interface{})
	if !ok || len(raw) == 0 {
		return nil
	}
	history := make([]agentAIMessage, 0, len(raw))
	for _, item := range raw {
		row, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		content := stripAgentAIPhoneContract(strings.TrimSpace(remoteString(row, "content")))
		if content == "" {
			continue
		}
		history = append(history, agentAIMessage{
			Role:      remoteString(row, "role"),
			MessageID: remoteString(row, "id"),
			Content:   content,
			CreatedAt: time.Now().UTC(),
		})
	}
	return history
}

func resolveAgentAIAttachments(raw interface{}, projectPath string) ([]agentAIAttachment, error) {
	items, _ := raw.([]interface{})
	if len(items) == 0 {
		return nil, nil
	}
	// Attachment validation is the first place a message touches the project
	// directory. Behind tccguard, an unattended Mac refuses protected folders
	// instead of leaving an unanswerable "Files & Folders" prompt on screen.
	if err := tccguard.GuardPath(projectPath); err != nil {
		return nil, err
	}
	projectRoot, err := cleanExistingAgentDirectory(projectPath)
	if err != nil {
		return nil, fmt.Errorf("resolve attachment project directory: %w", err)
	}
	if err := tccguard.GuardPath(projectRoot); err != nil {
		return nil, err
	}
	const maxAttachments = 8
	const maxAttachmentBytes = 20 * 1024 * 1024
	const maxTotalBytes = 50 * 1024 * 1024
	// 文本素材的上限按 rune 计（不是字节）：中文/emoji 的字节数是 rune 的数倍，
	// 按字节卡会把一幅正常大小的 ASCII 图误杀。服务端 schema 按字符卡同一上限，
	// 这里只做防线，放宽方向是安全的（不会拒掉服务端放行的载荷）。
	const maxAttachmentTextRunes = 32 * 1024
	if len(items) > maxAttachments {
		return nil, fmt.Errorf("ai.message attachments exceed %d files", maxAttachments)
	}
	attachments := make([]agentAIAttachment, 0, len(items))
	var totalBytes int64
	for _, rawItem := range items {
		item := mapIf(rawItem)
		if item == nil {
			return nil, errors.New("ai.message attachment must be an object")
		}
		attachment := agentAIAttachment{
			Type: strings.ToLower(strings.TrimSpace(remoteString(item, "type"))),
			Name: strings.TrimSpace(remoteString(item, "name")),
			Path: strings.TrimSpace(firstNonEmpty(remoteString(item, "path"), remoteString(item, "local_path"))),
			URL:  strings.TrimSpace(remoteString(item, "url")),
			// 正文不 TrimSpace：ASCII 图的缩进就是语义，首尾空格删掉图就错位了。
			Text: remoteString(item, "text"),
		}
		if attachment.Type == "" && attachment.Text != "" {
			attachment.Type = "text"
		}
		if attachment.Type == "text" && attachment.Text != "" {
			// 内联文本素材：既没有文件也没有 URL，不走下面任何下载/围栏分支。
			// 注意 Type=="text" 还有另一种历史含义——「一个文本文件」，那种
			// 带 Path 但没有 Text，继续走下面的文件校验。
			if strings.TrimSpace(attachment.Text) == "" {
				return nil, errors.New("text attachment requires non-empty text")
			}
			if utf8.RuneCountInString(attachment.Text) > maxAttachmentTextRunes {
				return nil, fmt.Errorf("text attachment exceeds %d characters", maxAttachmentTextRunes)
			}
			if attachment.Name == "" {
				attachment.Name = "attachment.txt"
			}
			attachments = append(attachments, attachment)
			continue
		}
		if attachment.Type == "text" && attachment.Path == "" && attachment.URL == "" {
			return nil, errors.New("text attachment requires non-empty text")
		}
		if attachment.Path != "" {
			path := attachment.Path
			if !filepath.IsAbs(path) {
				path = filepath.Join(projectPath, path)
			}
			absolute, err := filepath.Abs(path)
			if err != nil {
				return nil, err
			}
			// Absolute attachment paths may point at another account's home or a
			// different privacy category than the project; guard before resolving
			// symlinks (EvalSymlinks itself stats every component).
			if err := tccguard.GuardPath(absolute); err != nil {
				return nil, err
			}
			if resolved, resolveErr := filepath.EvalSymlinks(absolute); resolveErr == nil {
				absolute = resolved
				if err := tccguard.GuardPath(absolute); err != nil {
					return nil, err
				}
			}
			cacheHandled := false
			if cacheRoot, cacheErr := agentAttachmentCacheRoot(); cacheErr == nil {
				// macOS 的 /var→/private/var 类 symlink 会让 TempDir 形态的根与
				// EvalSymlinks 后的 absolute 前缀失配——根也走一次 symlink 解析。
				resolvedRoot := cacheRoot
				if resolved, err := filepath.EvalSymlinks(cacheRoot); err == nil {
					resolvedRoot = resolved
				}
				if agentPathInsideDirectory(absolute, resolvedRoot) {
					if rel, relErr := filepath.Rel(resolvedRoot, absolute); relErr == nil {
						attachment.PersistPath = filepath.ToSlash(rel)
						attachment.Origin = "web"
						cacheHandled = true
					}
				}
			}
			if !cacheHandled && !agentPathInsideDirectory(absolute, projectRoot) {
				return nil, fmt.Errorf("attachment path is outside the project directory: %s", absolute)
			}
			stat, err := os.Stat(absolute)
			if err != nil {
				return nil, err
			}
			if !stat.Mode().IsRegular() {
				return nil, fmt.Errorf("attachment is not a regular file: %s", absolute)
			}
			if stat.Size() > maxAttachmentBytes {
				return nil, fmt.Errorf("attachment exceeds %d MiB: %s", maxAttachmentBytes/(1024*1024), absolute)
			}
			totalBytes += stat.Size()
			if totalBytes > maxTotalBytes {
				return nil, fmt.Errorf("attachments exceed %d MiB in total", maxTotalBytes/(1024*1024))
			}
			attachment.Path = absolute
			if attachment.Name == "" {
				attachment.Name = filepath.Base(absolute)
			}
			if attachment.Type == "" && isImageAttachmentPath(absolute) {
				attachment.Type = "image"
			}
		}
		if attachment.URL != "" {
			parsed, err := url.Parse(attachment.URL)
			if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "data") {
				return nil, errors.New("attachment URL must use https or data scheme")
			}
			if parsed.Scheme == "data" && !strings.HasPrefix(strings.ToLower(attachment.URL), "data:image/") {
				return nil, errors.New("only image data URLs are supported")
			}
			if attachment.Type == "" {
				attachment.Type = "image"
			}
		}
		if attachment.Path == "" && attachment.URL == "" {
			return nil, errors.New("attachment requires path or url")
		}
		attachments = append(attachments, attachment)
	}
	return attachments, nil
}

func isImageAttachmentPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	default:
		return false
	}
}

func (m *agentAIManager) stop(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	if writeJSON == nil {
		return
	}
	sessionID := remoteString(msg, "session_id")
	if sessionID == "" {
		_ = writeJSON(agentAIErrorPayload("", "", errors.New("ai.stop missing session_id")))
		return
	}
	sourceSessionID := strings.TrimSpace(remoteString(msg, "source_session_id"))

	m.mu.Lock()
	session := m.sessions[sessionID]
	if session == nil {
		m.mu.Unlock()
		// Imported sessions are conversations Claude Code spawned outside this
		// agent (nothing in m.sessions, no cancel func). The turn may be
		// running in a live TUI process on this machine: try the external
		// interrupt (SIGINT = one Esc press) when the stop carries the native
		// session id, and fall back to the legacy "stopped" reply otherwise.
		interrupted := false
		if sourceSessionID != "" {
			_, interrupted = interruptExternalClaudeTurn(externalTUIHome(), sourceSessionID)
		}
		_ = writeJSON(externalStopReply(sessionID, interrupted))
		return
	}
	requestedRunID := strings.TrimSpace(remoteString(msg, "run_id"))
	if requestedRunID != "" && session.activeRunID != "" && requestedRunID != session.activeRunID {
		activeRunID := session.activeRunID
		m.mu.Unlock()
		logger.Info(fmt.Sprintf("ai.stop: ignored stale run session=%s requested=%s active=%s", sessionID, requestedRunID, activeRunID))
		return
	}
	cancel := session.cancel
	runWrite := session.activeWriter
	cancelledApprovals := m.clearPendingApprovalsLocked(
		sessionID,
		session.runSeq,
		models.AgentAIApprovalDecisionCancel,
	)
	m.mu.Unlock()
	if runWrite == nil {
		runWrite = writeJSON
	}
	// TODO(ai-run-resilience): 此处的 cancel 不经过 agentAIRunGuard——卡死回合
	// 上用户主动停止时没有 [ai.run.kill] 轨迹、也没有 30s 强拆兜底。见
	// docs/superpowers/plans/2026-10-27-ai-run-resilience.md（计划未覆盖）。
	if cancel != nil {
		cancel()
	} else if sourceSessionID != "" {
		// Registered but idle: the previous agent-spawned turn already
		// finished, so the turn being stopped may belong to the external TUI.
		// Best-effort interrupt; the reply below is unchanged either way.
		interruptExternalClaudeTurn(externalTUIHome(), sourceSessionID)
	}
	m.emitApprovalCancelled(runWrite, sessionID, cancelledApprovals, "run_cancelled")
	_ = runWrite(map[string]interface{}{
		"type":       models.AgentEventAIStatus,
		"session_id": sessionID,
		"status":     "stopping",
	})
}

// preemptForGoalRun makes a newer server-authoritative Goal run replace a
// locally-stuck run in the same conversation. Ordinary chat messages retain the
// existing single-run guard. The run_id fence prevents a redelivered copy of the
// same Goal run from cancelling itself.

func (m *agentAIManager) preemptForGoalRun(
	sessionID string,
	incomingRunID string,
	goalIdentity map[string]interface{},
	timeout time.Duration,
) error {
	if len(goalIdentity) == 0 || strings.TrimSpace(incomingRunID) == "" {
		return nil
	}

	m.mu.Lock()
	session := m.sessions[sessionID]
	if session == nil || session.cancel == nil {
		m.mu.Unlock()
		return nil
	}
	if session.activeRunID == incomingRunID {
		m.mu.Unlock()
		return fmt.Errorf("ai session is already running: %s", sessionID)
	}
	cancel := session.cancel
	runWrite := session.activeWriter
	cancelledApprovals := m.clearPendingApprovalsLocked(
		sessionID,
		session.runSeq,
		models.AgentAIApprovalDecisionCancel,
	)
	m.mu.Unlock()

	// TODO(ai-run-resilience): 此处的 cancel 不经过 agentAIRunGuard——卡死回合
	// 上用户主动停止时没有 [ai.run.kill] 轨迹、也没有 30s 强拆兜底。见
	// docs/superpowers/plans/2026-10-27-ai-run-resilience.md（计划未覆盖）。
	cancel()
	m.emitApprovalCancelled(runWrite, sessionID, cancelledApprovals, "goal_run_preempted")

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		m.mu.Lock()
		current := m.sessions[sessionID]
		idle := current == nil || current.cancel == nil
		m.mu.Unlock()
		if idle {
			return nil
		}
		select {
		case <-deadline.C:
			return fmt.Errorf("goal run preemption timed out: %s", sessionID)
		case <-ticker.C:
		}
	}
}

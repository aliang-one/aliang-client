package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
)

func (m *agentAIManager) approval(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	sessionID := remoteString(msg, "session_id")
	approvalID := remoteString(msg, "approval_id")
	decision := normalizeAgentAIApprovalDecision(remoteString(msg, "decision"))
	deliveryID := strings.TrimSpace(remoteString(msg, "delivery_id"))
	if sessionID == "" || approvalID == "" {
		if writeJSON != nil {
			_ = writeJSON(agentAIErrorPayload(sessionID, remoteString(msg, "message_id"), errors.New("ai.approval.response missing session_id or approval_id")))
		}
		return
	}
	// Always ACK a delivery (even on duplicate / no_match / bad decision) so the
	// server stops retrying it. result tells the server whether the decision
	// actually took effect on a live waiter.
	//
	// 跨端同态（spec §4.2.2）：ack 双写——决策到达的 socket（现有语义，重投
	// 引擎的对端确认）+ run 发起的 socket（修「仪表盘抢答手机回合时 PhoneServer
	// 收不到 ack、已生效决策被重投至 10 次」）。runSocket 在闭包外一次取好
	// （session.originSocket，Task 5 落）；同 socket 只写一次；run socket 已
	// 换道/已死/未注册时尽力而为跳过（服务端宽限+离线撤单兜底）。duplicate /
	// no_match / 坏决策各 return 路径都经这一个闭包，全覆盖。
	originSocket := strings.TrimSpace(remoteString(msg, agentAIOriginSocketField))
	runSocket := ""
	m.mu.Lock()
	if s := m.sessions[sessionID]; s != nil {
		runSocket = s.originSocket
	}
	m.mu.Unlock()
	ack := func(result string) {
		payload := map[string]interface{}{
			"type":        models.AgentEventAIApprovalAck,
			"session_id":  sessionID,
			"approval_id": approvalID,
			"result":      result,
		}
		if deliveryID != "" {
			payload["delivery_id"] = deliveryID
		}
		if writeJSON != nil {
			_ = writeJSON(payload)
		}
		if runSocket != "" && runSocket != originSocket {
			m.mu.Lock()
			sock := m.aiSockets[runSocket]
			m.mu.Unlock()
			if sock != nil && sock.alive() {
				sock.enqueue(payload)
			}
		}
	}
	if decision == "" {
		ack("no_match")
		if writeJSON != nil {
			_ = writeJSON(agentAIErrorPayload(sessionID, remoteString(msg, "message_id"), fmt.Errorf("unsupported approval decision: %s", remoteString(msg, "decision"))))
		}
		return
	}

	raw := marshalAgentAIRaw(msg["raw"])
	response := agentAIApprovalResponse{
		Decision: decision,
		Scope:    normalizeAgentAIApprovalScope(remoteString(msg, "scope")),
		Raw:      raw,
	}

	approvalKey := agentAIApprovalMapKey(sessionID, approvalID)
	m.mu.Lock()
	if completed := m.completedApprovals[approvalKey]; completed != nil && completed.sessionID == sessionID {
		m.mu.Unlock()
		ack("duplicate")
		return
	}
	waiter := m.approvals[approvalKey]
	if waiter != nil && waiter.sessionID == sessionID {
		delete(m.approvals, approvalKey)
		m.rememberCompletedApprovalLocked(approvalID, sessionID, waiter.runSeq, response)
	}
	m.mu.Unlock()
	if waiter == nil || waiter.sessionID != sessionID {
		ack("no_match")
		if writeJSON != nil {
			_ = writeJSON(map[string]interface{}{
				"type":       models.AgentEventAIStatus,
				"session_id": sessionID,
				"status":     "approval_not_found",
			})
		}
		return
	}

	// 会话级批准记忆（spec §4.2.4）：accept_for_session 落一条 (sessionID,
	// provider, kind, 匹配键)，本会话内同键后续请求由三个检查点短路、不再
	// 云端往返。普通 accept 不记录。必须在 m.mu.Unlock() 之后调用
	// （rememberSessionApproval 自身取 m.mu，临界区内调用=死锁）。
	if decision == models.AgentAIApprovalDecisionAcceptForSession {
		m.rememberSessionApproval(sessionID, waiter.request.Provider, waiter.request.Kind, waiter.request.ToolName, waiter.request.Command)
	}

	waiter.request.respond <- response
	ack("applied")
}

// pendingApprovalsSnapshot returns the approvals this client is still waiting
// on, for ai.approval.sync (reconnect reconcile + liveness heartbeat).

func (m *agentAIManager) pendingApprovalsSnapshot() []map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]map[string]interface{}, 0, len(m.approvals))
	for _, waiter := range m.approvals {
		if waiter == nil {
			continue
		}
		entry := map[string]interface{}{
			"session_id":  waiter.sessionID,
			"approval_id": waiter.request.ID,
			"kind":        waiter.request.Kind,
			"provider":    waiter.request.Provider,
		}
		if waiter.request.Command != "" {
			entry["command"] = waiter.request.Command
		}
		if waiter.request.ToolName != "" {
			entry["tool_name"] = waiter.request.ToolName
		}
		if !waiter.expiresAt.IsZero() {
			entry["expires_at"] = waiter.expiresAt.Format(time.RFC3339)
		}
		out = append(out, entry)
	}
	return out
}

// emitApprovalSync sends ai.approval.sync for all still-pending approvals.
// Used after reconnect and on a periodic ticker. 空集也发(2026-11-01 审计):
// 显式表达「本机无 pending」——此前 no-op 使协议无法传达撤销,server 侧残留
// 卡只能靠 24h/deadline 收敛;服务端对空数组 no-op,安全。

func (m *agentAIManager) emitApprovalSync(writeJSON agentTerminalWriter) {
	if writeJSON == nil {
		return
	}
	pending := m.pendingApprovalsSnapshot()
	_ = writeJSON(map[string]interface{}{
		"type":    models.AgentEventAIApprovalSync,
		"pending": pending,
	})
}

// emitApprovalCancelled notifies the server that a dialogue went inactive and
// its pending approvals were dropped, so the server stops retrying them.
// Best-effort: callers without a writer (closeAll on shutdown) rely on the
// server's offline-grace cancellation instead.

func (m *agentAIManager) emitApprovalCancelled(writeJSON agentTerminalWriter, sessionID string, approvalIDs []string, reason string) {
	if writeJSON == nil || sessionID == "" || len(approvalIDs) == 0 {
		return
	}
	payload := map[string]interface{}{
		"type":         models.AgentEventAIApprovalCancelled,
		"session_id":   sessionID,
		"approval_ids": approvalIDs,
	}
	if reason != "" {
		payload["reason"] = reason
	}
	_ = writeJSON(payload)
}

// emitOptionCancelled 在 session 失活（close 等）时，best-effort 通知服务端作废待选。
// 与 emitApprovalCancelled 同理：closeAll 等无 writer 的场景依赖服务端 offline-grace。

func (m *agentAIManager) emitOptionCancelled(writeJSON agentTerminalWriter, sessionID string, optionIDs []string, reason string) {
	if writeJSON == nil || sessionID == "" || len(optionIDs) == 0 {
		return
	}
	payload := map[string]interface{}{
		"type":       models.AgentEventAIOptionCancelled,
		"session_id": sessionID,
		"option_ids": optionIDs,
	}
	if reason != "" {
		payload["reason"] = reason
	}
	_ = writeJSON(payload)
}

// cancelOptions 处理 server→agent 的 ai.option.cancelled：清匹配的 pendingOption，
// 避免后续把已作废的选项误续接成新 run。

func (m *agentAIManager) cancelOptions(msg map[string]interface{}) {
	sessionID := remoteString(msg, "session_id")
	if sessionID == "" {
		return
	}
	m.mu.Lock()
	if session := m.sessions[sessionID]; session != nil {
		session.pendingOption = nil
	}
	m.mu.Unlock()
}

func (m *agentAIManager) close(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	if writeJSON == nil {
		return
	}
	sessionID := remoteString(msg, "session_id")
	if sessionID == "" {
		_ = writeJSON(agentAIErrorPayload("", "", errors.New("ai.session.close missing session_id")))
		return
	}

	m.mu.Lock()
	session := m.sessions[sessionID]
	var cancelled []string
	var cancelledOptions []string
	if session != nil {
		// Collect pendingOption id BEFORE deleting the session so the server can
		// be told to drop it. Mirrors the approval-cancelled cleanup.
		if session.pendingOption != nil {
			cancelledOptions = []string{session.pendingOption.ID}
			session.pendingOption = nil
		}
		delete(m.sessions, sessionID)
		cancelled = m.clearPendingApprovalsLocked(sessionID, session.runSeq, models.AgentAIApprovalDecisionCancel)
	}
	var cancel context.CancelFunc
	var runWrite agentTerminalWriter
	if session != nil {
		cancel = session.cancel
		runWrite = session.activeWriter
	}
	m.mu.Unlock()
	m.emitApprovalCancelled(writeJSON, sessionID, cancelled, "session_closed")
	m.emitOptionCancelled(writeJSON, sessionID, cancelledOptions, "session_closed")

	if runWrite == nil {
		runWrite = writeJSON
	}
	if err := runWrite(map[string]interface{}{
		"type":       models.AgentEventAISessionClosed,
		"session_id": sessionID,
	}); err != nil {
		logger.Warn(fmt.Sprintf("ai.session.close: terminal delivery deferred session=%s error=%v", sessionID, err))
	}
	if cancel != nil {
		cancel()
	}
}

// agentAITeardownStatus 是「run 的所有者被拆掉」时发布的终态。服务端把
// stopped|cancelled|interrupted 映射成 run_state=cancelled（生命周期收敛），
// 这才是「回合被打断」的诚实结论。什么都不发则会留下一个 live run，被服务端
// 的 stale sweeper 打翻成 timed_out —— 手机上一张红色失败卡片（2026-10-21 事故）。

const agentAITeardownStatus = "cancelled"

func agentAITeardownMessage(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "agent_shutdown"
	}
	return fmt.Sprintf("AI run cancelled by the agent (%s); the local process may keep running, but its result is no longer reported", reason)
}

// closeAll 拆掉本机所有常驻 AI 会话（进程内调用点没有具体原因时的默认入口）。

func (m *agentAIManager) closeAll() {
	m.closeAllWithReason("agent_shutdown")
}

// closeAllWithReason 拆掉本机所有常驻 AI 会话。在途 run **先收敛后取消**：通过该
// run 自己的 emitter 发布终态 ai.status —— emitter 会先把终态写进持久 journal
// (rememberPendingTerminal) 并关闭 terminal 屏障，之后才 cancel ctx。
//
// 顺序在这里有两重意义：
//  1. journal 必须在 run 身份还在手上时落盘，进程随后崩掉也还能在下次
//     agent.registered 时由 replayPendingTerminals 重放；
//  2. terminal 屏障让 run goroutine 的 ctx.Err() 分支无法再塞一个自相矛盾的终态。
//
// 旧实现先把 m.sessions 整个换掉再 cancel，run goroutine 醒来时既找不到 session
// 也没有终态可发，服务端就一直挂着一个 live run，直到 sweeper 把它画成
// timed_out/失败（2026-10-21 事故：回合其实已经跑完，卡片却是红的）。

func (m *agentAIManager) closeAllWithReason(reason string) {
	type inflightRun struct {
		sessionID string
		runID     string
		write     agentTerminalWriter
	}

	m.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(m.sessions))
	runs := make([]inflightRun, 0, len(m.sessions))
	for _, session := range m.sessions {
		if session.cancel != nil {
			cancels = append(cancels, session.cancel)
			if session.activeWriter != nil {
				runs = append(runs, inflightRun{
					sessionID: session.id,
					runID:     session.activeRunID,
					write:     session.activeWriter,
				})
			}
		}
		// Drop pendingOption on shutdown (no writer -> rely on server offline-grace).
		session.pendingOption = nil
		m.clearPendingApprovalsLocked(session.id, session.runSeq, models.AgentAIApprovalDecisionCancel)
	}
	m.sessions = make(map[string]*agentAISession)
	m.mu.Unlock()

	message := agentAITeardownMessage(reason)
	for _, run := range runs {
		err := run.write(map[string]interface{}{
			"type":       models.AgentEventAIStatus,
			"session_id": run.sessionID,
			"status":     agentAITeardownStatus,
			"error":      message,
		})
		// emitter 先 journal 后写线，所以这里报错只代表「还没上线路」，终态本身
		// 已经持久化，重连后会被 replayPendingTerminals 补发。
		logger.Info(fmt.Sprintf("[ai.run.terminal] status=%s session=%s run=%s reason=%s delivered=%t",
			agentAITeardownStatus, run.sessionID, run.runID, reason, err == nil))
		if err != nil {
			logger.Warn(fmt.Sprintf("[ai.run.terminal] teardown delivery deferred session=%s run=%s error=%v", run.sessionID, run.runID, err))
		}
	}
	for _, cancel := range cancels {
		cancel()
	}
}

// emitOptionRequest 把 run 结束时提取到的方案块，发 ai.option.request 并在 session 上记 pending。
// MVP：一次 run 只处理第一个有效块。runSeq 不匹配（已被覆盖）时静默跳过，防止串台。

func (m *agentAIManager) emitOptionRequest(run agentAIRun, writeJSON agentTerminalWriter, blocks []agentAIOptionRequest) {
	if writeJSON == nil || len(blocks) == 0 {
		return
	}
	req := blocks[0]
	if strings.TrimSpace(req.ID) == "" {
		req.ID = newAgentAIApprovalID(run.sessionID, run.runSeq)
	}
	req.SessionID = run.sessionID
	req.MessageID = agentAssistantMessageID(run.messageID)
	req.Provider = firstNonEmpty(req.Provider, run.provider)

	m.mu.Lock()
	session := m.sessions[run.sessionID]
	if session == nil || session.runSeq != run.runSeq {
		m.mu.Unlock()
		return
	}
	session.pendingOption = &req
	m.mu.Unlock()

	_ = writeJSON(map[string]interface{}{
		"type":         models.AgentEventAIOptionRequest,
		"session_id":   run.sessionID,
		"message_id":   req.MessageID,
		"option_id":    req.ID,
		"title":        req.Title,
		"header":       req.Header,
		"summary":      req.Summary,
		"v":            req.Version,
		"options":      req.Options,
		"allow_custom": req.AllowCustom,
		"multi":        req.Multi,
		"provider":     req.Provider,
	})
}

func (m *agentAIManager) requestApproval(ctx context.Context, run agentAIRun, writeJSON agentTerminalWriter, req agentAIApprovalRequest) (agentAIApprovalResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if writeJSON == nil {
		return agentAIApprovalResponse{}, errors.New("approval writer is unavailable")
	}
	req.SessionID = run.sessionID
	req.MessageID = agentAssistantMessageID(run.messageID)
	req.Provider = firstNonEmpty(req.Provider, run.provider)
	if req.ID == "" {
		req.ID = newAgentAIApprovalID(run.sessionID, run.runSeq)
	}
	if req.Kind == "" {
		req.Kind = models.AgentAIApprovalKindTool
	}
	if len(req.AvailableDecisions) == 0 {
		req.AvailableDecisions = []string{
			models.AgentAIApprovalDecisionAccept,
			models.AgentAIApprovalDecisionDecline,
			models.AgentAIApprovalDecisionCancel,
		}
	}
	req.respond = make(chan agentAIApprovalResponse, 1)

	// deadline 与下方等待用的 agentAIApprovalTimeout 同源：expires_at 是给
	// 两端 UI 显示剩余时间的绝对截止（RFC3339，spec §4.2.1）。
	deadline := time.Now().UTC().Add(agentAIApprovalTimeout)
	m.mu.Lock()
	if m.approvals == nil {
		m.approvals = make(map[string]*agentAIApprovalWaiter)
	}
	approvalKey := agentAIApprovalMapKey(run.sessionID, req.ID)
	m.approvals[approvalKey] = &agentAIApprovalWaiter{
		sessionID: run.sessionID,
		runSeq:    run.runSeq,
		request:   req,
		expiresAt: deadline,
	}
	m.mu.Unlock()

	run.activity.setAwaitingApproval(true)
	defer run.activity.setAwaitingApproval(false)
	logger.Info(fmt.Sprintf("approval-hook: requestApproval SENDING ai.approval.request to cloud session=%s approval_id=%s kind=%s — awaiting user decision (≤%s)", run.sessionID, req.ID, req.Kind, agentAIApprovalTimeout))
	payload := agentAIApprovalRequestPayload(req)
	payload["expires_at"] = deadline.Format(time.RFC3339)
	// L1 可靠投递(2026-11-01 一致性审计根因 1):发送前 write-through 记入
	// 投递 journal,ack/waiter 终结时清除;半开 socket 静默丢帧后由重连重放
	// 补达(服务端 intake 幂等)。持久化失败只降级为现状,不阻塞审批。
	m.rememberPendingApprovalRequest(payload)
	if err := writeAgentAIApprovalRequest(ctx, writeJSON, payload, agentAIApprovalSendAttempts, agentAIApprovalSendRetryDelay); err != nil {
		// 决策丢弃竞态兜底（2026-11-09，11-04 生产实证）：发送失败≠审批无主——
		// 重试窗口内决策可能已先到（approval() 删 waiter、落 completedApprovals、
		// 投 respond）。删 waiter 与读 completedApprovals 必须同一临界区：与
		// approval() 的「删+落账」临界区原子互斥后，任何已生效决策必在此读出
		// （分两次取锁会在两锁间重开竞态窗口）。有账即决策已生效，喂回内核 +
		// 同型 resolved 帧照发（对端翻卡），不得返回 error 让内核按超时拒掉
		// 用户已批准的操作；无账维持既有 error 语义（waiter 已删，迟到的决策
		// 走 approval() no_match，两端一致）。
		m.mu.Lock()
		if waiter := m.approvals[approvalKey]; waiter != nil && waiter.sessionID == run.sessionID && waiter.runSeq == run.runSeq {
			delete(m.approvals, approvalKey)
		}
		completed := m.completedApprovals[approvalKey]
		m.mu.Unlock()
		if completed != nil && completed.sessionID == run.sessionID && completed.runSeq == run.runSeq {
			m.acknowledgePendingApprovalRequest(req.ID)
			_ = writeJSON(map[string]interface{}{
				"type":        models.AgentEventAIApprovalRequest,
				"session_id":  run.sessionID,
				"message_id":  agentAssistantMessageID(run.messageID),
				"approval_id": req.ID,
				"provider":    req.Provider,
				"kind":        req.Kind,
				"status":      "resolved",
				"decision":    completed.response.Decision,
			})
			return completed.response, nil
		}
		m.acknowledgePendingApprovalRequest(req.ID)
		return agentAIApprovalResponse{}, fmt.Errorf("send approval request: %w", err)
	}

	select {
	case response := <-req.respond:
		m.acknowledgePendingApprovalRequest(req.ID)
		_ = writeJSON(map[string]interface{}{
			"type":        models.AgentEventAIApprovalRequest,
			"session_id":  run.sessionID,
			"message_id":  agentAssistantMessageID(run.messageID),
			"approval_id": req.ID,
			"provider":    req.Provider,
			"kind":        req.Kind,
			"status":      "resolved",
			"decision":    response.Decision,
		})
		return response, nil
	case <-ctx.Done():
		cancelled := false
		m.mu.Lock()
		if waiter := m.approvals[approvalKey]; waiter != nil && waiter.sessionID == run.sessionID && waiter.runSeq == run.runSeq {
			delete(m.approvals, approvalKey)
			m.rememberCompletedApprovalLocked(req.ID, run.sessionID, run.runSeq, agentAIApprovalResponse{Decision: models.AgentAIApprovalDecisionCancel})
			cancelled = true
		}
		m.mu.Unlock()
		if cancelled {
			reason := "run_cancelled"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				reason = "approval_timeout"
			}
			m.emitApprovalCancelled(writeJSON, run.sessionID, []string{req.ID}, reason)
		}
		// waiter 已终结,journal 条目随之清除:后续重放只会得到服务端
		// already_resolved/duplicate 告知,主动清干净。
		m.acknowledgePendingApprovalRequest(req.ID)
		return agentAIApprovalResponse{}, ctx.Err()
	}
}

func writeAgentAIApprovalRequest(
	ctx context.Context,
	writeJSON agentTerminalWriter,
	payload map[string]interface{},
	maxAttempts int,
	retryDelay time.Duration,
) error {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := writeJSON(payload); err == nil {
			return nil
		} else {
			lastErr = err
			logger.Warn(fmt.Sprintf("approval-hook: request send failed attempt=%d/%d error=%v", attempt, maxAttempts, err))
		}
		if attempt == maxAttempts {
			break
		}
		timer := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

func (m *agentAIManager) handleClaudeApprovalHook(ctx context.Context, sessionID string, messageID string, token string, raw map[string]interface{}) (map[string]interface{}, error) {
	sessionID = strings.TrimSpace(sessionID)
	messageID = strings.TrimSpace(messageID)
	token = strings.TrimSpace(token)
	hookEventName := claudeApprovalHookEventName(raw)
	logger.Info(fmt.Sprintf("approval-hook: INVOKED by claude session=%s messageID=%s tokenLen=%d tool_name=%s", sessionID, messageID, len(token), firstNonEmpty(remoteString(raw, "tool_name"), remoteString(raw, "toolName"))))
	if sessionID == "" || token == "" {
		logger.Warn(fmt.Sprintf("approval-hook: deny (missing session or token) session=%q tokenLen=%d", sessionID, len(token)))
		return claudeApprovalHookDecision(hookEventName, false, "Aliang approval hook is missing session or token."), errors.New("approval hook missing session or token")
	}

	m.mu.Lock()
	session := m.sessions[sessionID]
	// Diagnose which guard fails (each maps to a distinct fix).
	switch {
	case session == nil:
		logger.Warn(fmt.Sprintf("approval-hook: deny (no live session in map) session=%s — run already ended or never started", sessionID))
	case session.approvalToken == "":
		logger.Warn(fmt.Sprintf("approval-hook: deny (session.approvalToken empty) session=%s — run ended, token cleared", sessionID))
	case session.approvalToken != token:
		logger.Warn(fmt.Sprintf("approval-hook: deny (token mismatch) session=%s", sessionID))
	case session.activeWriter == nil:
		logger.Warn(fmt.Sprintf("approval-hook: deny (activeWriter nil) session=%s", sessionID))
	case session.cancel == nil:
		logger.Warn(fmt.Sprintf("approval-hook: deny (cancel nil) session=%s", sessionID))
	}
	if session == nil || session.approvalToken == "" || session.approvalToken != token || session.activeWriter == nil || session.cancel == nil {
		m.mu.Unlock()
		return claudeApprovalHookDecision(hookEventName, false, "Aliang could not match this permission request to a running AI session."), fmt.Errorf("approval hook session mismatch: %s", sessionID)
	}
	run := agentAIRun{
		sessionID:           session.id,
		messageID:           firstNonEmpty(messageID, session.id),
		runSeq:              session.runSeq,
		mode:                session.mode,
		projectPath:         session.projectPath,
		provider:            session.provider,
		model:               session.model,
		approvalMode:        session.approvalMode,
		cancel:              session.cancel,
		approvalToken:       session.approvalToken,
		activity:            session.activity,
		goalIdentity:        cloneGoalIdentity(session.goalIdentity),
		goalAllowedRoots:    session.goalAllowedRoots,
		goalAllowedCommands: session.goalAllowedCommands,
	}
	writeJSON := session.activeWriter
	m.mu.Unlock()

	// 自有交互工具短路:ask_user 等白名单工具就是"审批"本身,再弹审批卡等于
	// 双重交互(非 full 档注入的 permissions.ask 含 mcp__* 会把它压进 ask 路径)。
	// 白名单是精确名集合——未知 mcp__aliang__*(项目 .mcp.json 同名 server 注入)
	// 落回原路径。
	if toolName := firstNonEmpty(remoteString(raw, "tool_name"), remoteString(raw, "toolName")); models.IsAgentToolAskTool(toolName) {
		logger.Info(fmt.Sprintf("approval-hook: AUTO-APPROVE (aliang interaction tool) tool=%s session=%s", toolName, sessionID))
		return claudeApprovalHookDecision(hookEventName, true, "auto-approved: aliang interaction tool"), nil
	}

	// Policy short-circuit: evaluate the device approval policy locally before
	// hitting the cloud. Auto-approved/denied tools resolve here with no
	// round-trip; only require_approval falls through to requestApproval.
	toolName := firstNonEmpty(remoteString(raw, "tool_name"), remoteString(raw, "toolName"))
	toolInput := marshalAgentAIRaw(raw["tool_input"])
	if len(toolInput) == 0 {
		toolInput = marshalAgentAIRaw(raw["toolInput"])
	}
	// Goal execution override: the plan was pre-approved by the user (with
	// allowed_roots + allowed_commands). Auto-approve every non-dangerous tool
	// without a cloud round-trip — goal runs must NOT stall on manual approval
	// requests that the phone may not surface in time. Only block commands the
	// policy explicitly flags as dangerous (decisionAutoDeny).
	if len(run.goalIdentity) > 0 {
		// Gap B: allowed_scope 执行边界。越界（路径/命令）直接 deny，claude 收到自纠。
		// agent 级强制，非 OS 级——见 docs/superpowers/specs/2026-08-08-goal-allowed-scope-enforcement-design.md §6。
		// 注意：m.mu.Unlock() 已在此前执行，此处只能读局部 run（run 在锁内从 session 拷贝）。
		if ok, reason := goalScopeAllowsToolCall(toolName, raw, run.projectPath, run.goalAllowedRoots, run.goalAllowedCommands); !ok {
			logger.Info(fmt.Sprintf("approval-hook: SCOPE-DENY (goal run) tool=%s reason=%s session=%s", toolName, reason, sessionID))
			return claudeApprovalHookDecision(hookEventName, false, "blocked: "+reason), nil
		}
		if svc := m.approvalService(); svc != nil {
			if decision, _ := svc.evaluateApprovalDecision(toolName, toolInput, run.projectPath); decision == decisionAutoDeny {
				logger.Info(fmt.Sprintf("approval-hook: AUTO-DENY (dangerous, goal run) tool=%s session=%s", toolName, sessionID))
				return claudeApprovalHookDecision(hookEventName, false, "blocked: dangerous command in goal execution"), nil
			}
		}
		logger.Info(fmt.Sprintf("approval-hook: AUTO-APPROVE (goal run, pre-approved plan) tool=%s session=%s", toolName, sessionID))
		return claudeApprovalHookDecision(hookEventName, true, "auto-approved: goal execution within pre-approved plan"), nil
	}
	// bypass_all 快路径(仅策略服务缺失,同 agentcore):服务在场时由块内
	// override 处理,deny 规则照拒。
	hookSvc := m.approvalService()
	if run.approvalMode == agentAIApprovalModeBypassAll && hookSvc == nil {
		logger.Info(fmt.Sprintf("approval-hook: BYPASS-ALL fast path (policy service unavailable) tool=%s session=%s (hook)", toolName, sessionID))
		return claudeApprovalHookDecision(hookEventName, true, "bypassed by session approval mode"), nil
	}
	if hookSvc != nil {
		// raw 传入边界判定:Claude auto-memory carve-out 需要 transcript_path
		// 在 slug 编码漂移时精确锁定本会话的 memory 目录。
		rawDecision, rawMatchedID := hookSvc.evaluateApprovalDecisionForHook(toolName, toolInput, run.projectPath, raw)
		overridden := applyApprovalModeOverride(run.approvalMode, rawDecision, toolName)
		if overridden != rawDecision {
			logger.Info(fmt.Sprintf("approval-hook: MODE-OVERRIDE mode=%s %s→%s tool=%s session=%s (hook)", run.approvalMode, rawDecision, overridden, toolName, sessionID))
		}
		switch decision, matchedID := overridden, rawMatchedID; decision {
		case decisionAutoApprove:
			logger.Info(fmt.Sprintf("approval-hook: AUTO-APPROVE by policy rule=%s tool=%s session=%s (no cloud round-trip)", matchedID, toolName, sessionID))
			return claudeApprovalHookDecision(hookEventName, true, "auto-approved by policy: "+matchedID), nil
		case decisionAutoDeny:
			logger.Info(fmt.Sprintf("approval-hook: AUTO-DENY by policy rule=%s tool=%s session=%s", matchedID, toolName, sessionID))
			return claudeApprovalHookDecision(hookEventName, false, "denied by policy: "+matchedID), nil
		case decisionRequireApproval:
			// Attach policy context so the approver sees why it escalated.
			run.matchedRuleID = matchedID
			run.policyVersion = hookSvc.effectiveApprovalPolicyForPath(run.projectPath).Version
		}
	}

	// 会话级批准记忆检查点（policy 之后、云端往返之前）：本会话用户对同键
	// （command 逐字 / tool 按名，见 agent_ai_session_approvals.go）选过
	// accept_for_session 的直接放行，不再问手机。provider 取请求对象自身，
	// 与 approval() 记录点存的 waiter.request.Provider 对齐。
	// double build intentional: hit path stays branch-free
	// ask_all/bypass_all 下跳过会话批准记忆(用户显式要求每次问/反正全放)。
	// read_only 只对写/执行族跳过——记忆免问不能复活被压制的写权限。
	if approvalModeSkipsSessionMemoryForTool(run.approvalMode, toolName) {
		logger.Info(fmt.Sprintf("approval-hook: SESSION-MEMORY-SKIPPED mode=%s tool=%s session=%s (hook)", run.approvalMode, toolName, sessionID))
	} else if preReq := buildClaudeApprovalRequest(run, raw); m.sessionApprovalAllows(sessionID, preReq.Provider, preReq.Kind, preReq.ToolName, preReq.Command) {
		logger.Info(fmt.Sprintf("approval-hook: SESSION-APPROVED (memory) tool=%s kind=%s session=%s", preReq.ToolName, preReq.Kind, sessionID))
		m.journalSessionApprovalHit(sessionID, run.runSeq, preReq.Kind, preReq.ToolName, preReq.Command)
		return claudeApprovalHookDecision(hookEventName, true, "approved for this session"), nil
	}

	req := buildClaudeApprovalRequest(run, raw)
	approvalCtx, cancel := context.WithTimeout(ctx, agentAIApprovalTimeout)
	defer cancel()
	response, err := m.requestApproval(approvalCtx, run, writeJSON, req)
	if err != nil {
		return claudeApprovalHookDecision(hookEventName, false, "Aliang approval timed out or was cancelled."), err
	}
	switch response.Decision {
	case models.AgentAIApprovalDecisionAccept, models.AgentAIApprovalDecisionAcceptForSession:
		return claudeApprovalHookDecision(hookEventName, true, "Approved in Aliang."), nil
	default:
		return claudeApprovalHookDecision(hookEventName, false, "Denied in Aliang."), nil
	}
}

func buildClaudeApprovalRequest(run agentAIRun, raw map[string]interface{}) agentAIApprovalRequest {
	rawJSON := marshalAgentAIRaw(raw)
	toolName := firstNonEmpty(remoteString(raw, "tool_name"), remoteString(raw, "toolName"))
	toolInput := marshalAgentAIRaw(raw["tool_input"])
	if len(toolInput) == 0 {
		toolInput = marshalAgentAIRaw(raw["toolInput"])
	}
	command := claudeApprovalCommand(raw)
	kind := models.AgentAIApprovalKindTool
	if strings.EqualFold(toolName, "bash") || command != "" {
		kind = models.AgentAIApprovalKindCommand
	}
	reason := firstNonEmpty(
		boundaryApprovalReason(run.matchedRuleID),
		remoteString(raw, "permission_prompt"),
		remoteString(raw, "permissionPrompt"),
		remoteString(raw, "reason"),
	)
	title := "Approve Claude Code tool use"
	if kind == models.AgentAIApprovalKindCommand {
		title = "Approve Claude Code command"
	}
	return agentAIApprovalRequest{
		ID:        newAgentAIApprovalID(run.sessionID, run.runSeq),
		Provider:  run.provider,
		Kind:      kind,
		Title:     title,
		Reason:    reason,
		Command:   command,
		CWD:       firstNonEmpty(remoteString(raw, "cwd"), run.projectPath),
		ToolName:  toolName,
		ToolInput: toolInput,
		Raw:       rawJSON,
		AvailableDecisions: []string{
			models.AgentAIApprovalDecisionAccept,
			models.AgentAIApprovalDecisionDecline,
			models.AgentAIApprovalDecisionCancel,
		},
		MatchedRuleID: run.matchedRuleID,
		PolicyVersion: run.policyVersion,
	}
}

func claudeApprovalCommand(raw map[string]interface{}) string {
	for _, key := range []string{"command", "cmd"} {
		if value := strings.TrimSpace(remoteString(raw, key)); value != "" {
			return value
		}
	}
	for _, key := range []string{"tool_input", "toolInput", "input"} {
		row, ok := raw[key].(map[string]interface{})
		if !ok {
			continue
		}
		if value := firstNonEmpty(remoteString(row, "command"), remoteString(row, "cmd")); value != "" {
			return value
		}
	}
	return ""
}

func claudeApprovalHookEventName(raw map[string]interface{}) string {
	if raw == nil {
		return "PermissionRequest"
	}
	switch firstNonEmpty(
		remoteString(raw, "hook_event_name"),
		remoteString(raw, "hookEventName"),
		remoteString(raw, "hook_event"),
		remoteString(raw, "hookEvent"),
		remoteString(raw, "event"),
	) {
	case "PreToolUse":
		return "PreToolUse"
	case "PermissionRequest":
		return "PermissionRequest"
	default:
		return "PermissionRequest"
	}
}

func claudeApprovalHookDecision(hookEventName string, allow bool, reason string) map[string]interface{} {
	decision := "deny"
	if allow {
		decision = "allow"
	}
	if hookEventName == "PreToolUse" {
		return map[string]interface{}{
			"hookSpecificOutput": map[string]interface{}{
				"hookEventName":             "PreToolUse",
				"permissionDecision":        decision,
				"permissionDecisionReason":  reason,
				"suppressOutputForApprover": false,
			},
		}
	}
	return map[string]interface{}{
		"hookSpecificOutput": map[string]interface{}{
			"hookEventName": "PermissionRequest",
			"decision": map[string]interface{}{
				"behavior": decision,
				"message":  reason,
			},
		},
	}
}

func (m *agentAIManager) clearPendingApprovalsLocked(sessionID string, runSeq int, decision string) []string {
	var cancelled []string
	for approvalKey, waiter := range m.approvals {
		if waiter == nil || waiter.sessionID != sessionID || waiter.runSeq != runSeq {
			continue
		}
		delete(m.approvals, approvalKey)
		response := agentAIApprovalResponse{Decision: decision}
		m.rememberCompletedApprovalLocked(waiter.request.ID, sessionID, runSeq, response)
		waiter.request.respond <- response
		cancelled = append(cancelled, waiter.request.ID)
	}
	return cancelled
}

// cancelApprovals drops the waiters for the given approval ids (server told us
// they were cancelled, e.g. the device was offline past grace). Each cancelled
// waiter resolves with a "cancel" decision so the running CLI denies the tool.

func (m *agentAIManager) cancelApprovals(msg map[string]interface{}) {
	rawIDs, _ := msg["approval_ids"].([]interface{})
	if len(rawIDs) == 0 {
		return
	}
	want := make(map[string]bool, len(rawIDs))
	for _, raw := range rawIDs {
		if id, ok := raw.(string); ok && id != "" {
			want[id] = true
		}
	}
	if len(want) == 0 {
		return
	}
	m.mu.Lock()
	for approvalKey, waiter := range m.approvals {
		if waiter == nil || !want[waiter.request.ID] {
			continue
		}
		delete(m.approvals, approvalKey)
		response := agentAIApprovalResponse{Decision: models.AgentAIApprovalDecisionCancel}
		m.rememberCompletedApprovalLocked(waiter.request.ID, waiter.sessionID, waiter.runSeq, response)
		waiter.request.respond <- response
	}
	m.mu.Unlock()
}

func (m *agentAIManager) rememberCompletedApprovalLocked(approvalID string, sessionID string, runSeq int, response agentAIApprovalResponse) {
	if approvalID == "" || sessionID == "" {
		return
	}
	if m.completedApprovals == nil {
		m.completedApprovals = make(map[string]*agentAICompletedApproval)
	}
	if len(m.completedApprovals) > 1024 {
		m.completedApprovals = make(map[string]*agentAICompletedApproval)
	}
	m.completedApprovals[agentAIApprovalMapKey(sessionID, approvalID)] = &agentAICompletedApproval{
		sessionID: sessionID,
		runSeq:    runSeq,
		response:  response,
		createdAt: time.Now().UTC(),
	}
	// 审批审计行（spec §4.5）：决策/取消/超时全走本汇聚点，审计行自动覆盖
	// 全部终态，两端回放可见。尽力而为。j.mu 与本函数调用方持有的 m.mu 是
	// 不同的锁且无互调（journal 写侧从不取 m.mu），锁内调用安全。
	if m.coreJournal != nil {
		m.coreJournal.appendApprovalAudit(sessionID, approvalID, runSeq, response.Decision)
	}
}

func agentAIApprovalMapKey(sessionID string, approvalID string) string {
	return sessionID + "\x00" + approvalID
}

// clearRunning 复位会话运行态。expectGuard 是可选的 guard 身份校验：给了就
// 必须仍是该会话当前的 guard 才执行复位——/clear 会把 runSeq 归零后被新回合
// 复用，只比 runSeq 会让迟到的强拆拆掉新回合（见 finalizeStalledGuard）。
// 不传 expectGuard 的调用方行为与旧版逐字等价（guardOwns 恒真）。

package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
)

func (m *agentAIManager) steerReviveViaMessage(msg map[string]interface{}, sessionID, messageID, content string, writeJSON agentTerminalWriter) bool {
	if m.coreJournal == nil {
		return false
	}
	head, err := readAgentCoreJournalHeaderFrom(m.coreJournal.dir, sessionID)
	if err != nil {
		return false // journal 无记录 = 真未知会话，保持 not_running
	}
	if head.projectPath == "" {
		// 头行缺 project_path 的旧 journal 无法安全定位工作目录——与其拿
		// 当前帧的 project_path 猜（可能落在错误目录执行），不如明确报错。
		logger.Warn(fmt.Sprintf("ai.steer: revive skipped session=%s (journal head has no project_path)", sessionID))
		emitAgentAISteerAck(writeJSON, sessionID, messageID, "error", "该会话的磁盘记录缺少项目路径，无法自动恢复；请改为发送新消息", "revive_unsupported")
		return true
	}
	// 历史装回成功的可观测性：云端/手机能看到这条插话为何变成了普通回合。
	_ = writeJSON(map[string]interface{}{
		"type":       models.AgentEventAIRunNotice,
		"session_id": sessionID,
		"message_id": messageID,
		"code":       "steer_revived_as_message",
		"detail":     "会话不在内存中，已从磁盘恢复历史并将该插话作为新消息执行",
	})
	// steer ack 兼容（queued）：等 ack 确认送达的客户端不因转正而把插话标成
	// 失败——消息本身确实被接管了，只是以新回合的形态执行。先于 message()
	// 发出：转正后 writeJSON 的回程帧全部经 run emitter 扇出，ack 不再归
	// steer 通道管。
	emitAgentAISteerAck(writeJSON, sessionID, messageID, "queued", "", "")
	revived := cloneAgentAIMap(msg)
	revived["type"] = models.AgentEventAIMessage
	revived["session_id"] = sessionID
	revived["message_id"] = messageID
	revived["content"] = content
	revived["project_path"] = head.projectPath
	if head.model != "" {
		revived["model"] = head.model
	}
	// provider 从头行无法取得（journal 头行不记 provider），agentcore journal
	// 即 agentcore 会话——绑定的 confirmed 值是最权威的归属（同时满足 message
	// 入口的绑定族守卫：requested 与 persisted 同族时不改写）。
	if binding, ok := m.bindingForConversation(sessionID); ok && binding.Provider != "" {
		revived["provider"] = binding.Provider
		revived["tool"] = binding.Provider
		if binding.NativeSessionID != "" {
			revived["source_session_id"] = binding.NativeSessionID
		}
	} else {
		revived["provider"] = "agentcore"
		revived["tool"] = "agentcore"
	}
	delete(revived, "attachments")
	delete(revived, "goal_context") // steer 没有目标语义，防误触发 goal 契约
	// 与手机 ai.message 主路径同款：surface=phone 时不注入 TUI 契约
	//（headless 通道不注入，见 agent_ai_phone_contract.go 头注释）。
	// steer() 已在前面给 steerContent 包过一次，这里恢复原始 content。
	m.message(revived, writeJSON)
	return true
}

func (m *agentAIManager) steer(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	if writeJSON == nil {
		return
	}
	sessionID := remoteString(msg, "session_id")
	messageID := remoteString(msg, "message_id")
	content := strings.TrimSpace(remoteString(msg, "content"))
	if sessionID == "" {
		emitAgentAISteerAck(writeJSON, "", messageID, "error", "ai.steer missing session_id", "bad_request")
		return
	}
	// 与 ai.message 同款兜底（2026-11-10 聊天记录身份化）：缺 message_id 时
	// 回落 sessionID 会让同一会话内所有插话共用去重键，插话轮次在 journal 里
	// 互相覆盖/丢弃。改为现场铸造唯一 id，并在 ack 里回传，客户端可据以对账。
	if mintedID, minted, err := resolveAgentAIUserMessageID(messageID); err != nil {
		emitAgentAISteerAck(writeJSON, sessionID, "", "error", "ai.steer missing message_id", "bad_request")
		return
	} else if minted {
		messageID = mintedID
		logger.Warn(fmt.Sprintf("ai.steer: minted message_id session=%s id=%s (client sent none)", sessionID, messageID))
	}
	if content == "" {
		emitAgentAISteerAck(writeJSON, sessionID, messageID, "error", "ai.steer content is empty", "bad_request")
		return
	}
	if len(content) > agentAIMessageLimitBytes {
		emitAgentAISteerAck(writeJSON, sessionID, messageID, "error", fmt.Sprintf("ai.steer exceeds %d bytes", agentAIMessageLimitBytes), "bad_request")
		return
	}
	// 手机驱动插话的契约只在「注入正在运行的 TUI」时随文本走（下面两处外部
	// TUI 分支与 session.tuiTurn 分支）。headless 通道（claude stream-json /
	// codex / agentcore）不注入文本契约，见 agent_ai_phone_contract.go 头注释。
	steerContent := content
	if normalizeAgentAISurface(remoteString(msg, "surface")) == agentAISurfacePhone {
		steerContent = wrapAgentAIPhoneContract(content)
	}

	m.mu.Lock()
	session := m.sessions[sessionID]
	if session == nil {
		m.mu.Unlock()
		// 导入/外部 TUI 会话不在注册表(手机侧 tuiDrivenRunning)。若 server
		// 随 ai.steer 附了 source_session_id,且该 native 会话有 live 的 busy
		// pid 记录并带跨会话 messaging UDS socket(CC >= 2.1.224),插话经
		// CC 收件箱排队——与 stop 的外部 SIGINT 同一份注册表、与 TUI 注入
		// 回合(steerTUITurn)同语义。TUI 插话只有文本帧,附件先拒。
		if sourceSessionID := strings.TrimSpace(remoteString(msg, "source_session_id")); sourceSessionID != "" {
			if arr, ok := msg["attachments"].([]interface{}); ok && len(arr) > 0 {
				emitAgentAISteerAck(writeJSON, sessionID, messageID, "error", "外部 TUI 执行中的插话暂不支持附件，请等回合结束后再发", "unsupported_attachment")
				return
			}
			if result, errMsg := externalSteerAttempt(externalTUIHome(), sourceSessionID, steerContent); result != "not_running" {
				code := ""
				if result == "error" {
					code = "send_failed"
				}
				emitAgentAISteerAck(writeJSON, sessionID, messageID, result, errMsg, code)
				return
			}
		}
		// 跨端/重启续聊的 steer 侧（spec §4.3 对齐）：会话不在内存注册表≠死
		// 亡——磁盘 journal 里可能躺着完整历史。此前这里直接 ack
		// "ai session not found"（2026-11-13 手机实锤：agent 重装升级后内存
		// 注册表清空，用户给休眠会话插话直接报错），而 ai.message 同场景走
		// 懒建恢复。对齐语义：journal 有该会话的记录时，把这条插话**转正为
		// 新消息**委托 message()——它内部的懒建会从 journal 装回全部历史、
		// 从 confirmed 绑定供种原生会话 id 续跑，插话作为普通用户回合执行。
		// 委托帧不带 attachments： steer 的附件是 base64 已物化形态且部分
		// 校验已在上文发生，转正路径走 message 自己的附件物化管线语义不符。
		// journal 无记录/无 journal 时保持原 not_running 报错（真未知会话）。
		if m.steerReviveViaMessage(msg, sessionID, messageID, content, writeJSON) {
			return
		}
		emitAgentAISteerAck(writeJSON, sessionID, messageID, "not_running", fmt.Sprintf("ai session not found: %s", sessionID), "")
		return
	}
	// surface 先落到会话上：下面 TUI 分支按 session.surface 决定是否注入契约。
	if surface := normalizeAgentAISurface(remoteString(msg, "surface")); surface != "" {
		session.surface = surface
	}
	if session.cancel == nil {
		m.mu.Unlock()
		// 注册表里在、但当前没有活跃回合:该会话的回合可能正由外部 TUI 驱动
		// (与 stop 的 idle 分支对称的 best-effort)。尝试同一外部通道。
		if sourceSessionID := strings.TrimSpace(remoteString(msg, "source_session_id")); sourceSessionID != "" {
			if arr, ok := msg["attachments"].([]interface{}); ok && len(arr) > 0 {
				emitAgentAISteerAck(writeJSON, sessionID, messageID, "error", "外部 TUI 执行中的插话暂不支持附件，请等回合结束后再发", "unsupported_attachment")
				return
			}
			if result, errMsg := externalSteerAttempt(externalTUIHome(), sourceSessionID, steerContent); result != "not_running" {
				code := ""
				if result == "error" {
					code = "send_failed"
				}
				emitAgentAISteerAck(writeJSON, sessionID, messageID, result, errMsg, code)
				return
			}
		}
		emitAgentAISteerAck(writeJSON, sessionID, messageID, "not_running", fmt.Sprintf("ai session is not running: %s", sessionID), "")
		return
	}
	// TUI 注入回合：没有 stdin 可写，但 UDS 注入通道就是插话通道——CC 把回合中
	// 收到的注入按排队消息处理，当前回合结束后自动接续（与 codex enqueue 的
	// "queued" 语义一致）。分流给专用执行器，不走下面的 stdin 路径。
	if tuiPlan := session.tuiTurn; tuiPlan != nil {
		m.mu.Unlock()
		// 契约由 steerTUITurn 按 session.surface 追加到注入帧；history 存原文。
		m.steerTUITurn(session, *tuiPlan, msg, sessionID, messageID, content, writeJSON)
		return
	}
	control := session.codexSteer
	claudeControl := session.claudeSteer
	coreControl := session.coreSteer
	if control == nil && claudeControl == nil && coreControl == nil {
		m.mu.Unlock()
		emitAgentAISteerAck(writeJSON, sessionID, messageID, "unsupported", "ai.steer is only supported by active Codex app-server, Claude stream-json or Aliang Agent runs", "unsupported_provider")
		return
	}
	projectPath := session.projectPath
	m.mu.Unlock()

	// 附件在写 history 之前物化：校验失败不该留下一条「用户说过但没发出去」
	// 的历史（下一轮 prompt 会把它当上下文带上）。
	attachments, err := resolveAgentAIAttachments(msg["attachments"], projectPath)
	if err != nil {
		emitAgentAISteerAck(writeJSON, sessionID, messageID, "error", err.Error(), "bad_request")
		return
	}
	steer := agentAISteerMessage{
		MessageID: messageID,
		Content:   content,
		CreatedAt: time.Now().UTC(),
	}
	if len(attachments) > 0 {
		// 文本素材并入正文；图片编码成 block（base64 内联，不落临时文件，
		// 因此可以安全排队——见 agentAISteerMessage.ImageBlocks 注释）。
		if suffix := agentAIAttachmentPromptSuffix(textOnlyAttachments(attachments), false); suffix != "" {
			steer.Content += suffix
		}
		if hasImageAttachments(attachments) {
			blocks, journalRefs, buildErr := buildSteerImageBlocks(context.Background(), sessionID, messageID, attachments, remoteStringSlice(msg, "attachment_allowed_hosts"))
			if buildErr != nil {
				emitAgentAISteerAck(writeJSON, sessionID, messageID, "error", buildErr.Error(), "send_failed")
				return
			}
			steer.ImageBlocks = blocks
			steer.JournalAttachments = journalRefs
		}
	}

	m.mu.Lock()
	session.history = append(session.history, agentAIMessage{
		Role:      "user",
		MessageID: messageID,
		Content:   content,
		CreatedAt: steer.CreatedAt,
	})
	m.mu.Unlock()

	var result string
	switch {
	case control != nil:
		result, err = control.enqueue(steer)
	case claudeControl != nil:
		result, err = claudeControl.deliver(steer)
	default:
		result, err = coreControl.deliver(steer)
		if err == nil {
			// agentcore 专属：插话投递成功才留痕（deliver 失败时消息没进内核）。
			// 落 content（原文）而非 steer.Content——后者此时已拼上附件后缀，
			// journal 按 spec §5 只存真实用户 content。
			m.journalSteerUserMessage(sessionID, steer, content)
		}
	}
	if err != nil {
		code := "send_failed"
		if result == "not_running" {
			code = "turn_ended"
		}
		emitAgentAISteerAck(writeJSON, sessionID, messageID, result, err.Error(), code)
		return
	}
	emitAgentAISteerAck(writeJSON, sessionID, messageID, result, "", "")
}

func emitAgentAISteerAck(writeJSON agentTerminalWriter, sessionID, messageID, result, errMsg, code string) {
	if writeJSON == nil {
		return
	}
	payload := map[string]interface{}{
		"type":       models.AgentEventAISteerAck,
		"session_id": sessionID,
		"message_id": messageID,
		"result":     result,
		"acked_at":   time.Now().UTC().Format(time.RFC3339Nano),
	}
	if errMsg != "" {
		payload["error"] = errMsg
	}
	if code != "" {
		payload["code"] = code
	}
	_ = writeJSON(payload)
}

// steerTUITurn 把插话注入执行中的 TUI。TUI 回合没有 stdin（claudeSteer 恒
// nil），UDS 注入是唯一通道：CC 把回合中收到的注入按排队消息处理，当前回合
// 结束后自动接续执行——正是 steer 的语义（ccPeerInject 注释已实证 busy TUI
// 对注入排队）。插话回显由进行中的 tail 桥送回手机（transcriptUserEcho →
// ai.delta channel=user），不在这里重复推流。
//
// v1 只支持文本：注入帧是纯文本，附件要先落 TUI 侧本地缓存并让模型 Read，
// 排队场景下缓存生命周期复杂（回合可能随时结束），先明确拒绝而不是静默丢弃。

func (m *agentAIManager) steerTUITurn(session *agentAISession, plan tuiTurnPlan, msg map[string]interface{}, sessionID, messageID, content string, writeJSON agentTerminalWriter) {
	if arr, ok := msg["attachments"].([]interface{}); ok && len(arr) > 0 {
		emitAgentAISteerAck(writeJSON, sessionID, messageID, "error", "TUI 执行中的插话暂不支持附件，请等回合结束后再发", "unsupported_attachment")
		return
	}
	// held 观测以「当前 transcript 末尾」为起点：只盯注入之后新写的字节，
	// held 标记（若有）一定落在其中；从回合起点的旧偏移开始会把历史字节
	// 重复扫一遍。
	offset := int64(0)
	if info, err := os.Stat(plan.JSONLPath); err == nil {
		offset = info.Size()
	}
	// 手机行为契约只加在注入帧上；下面 history 仍落原文（契约不进上下文）。
	injectText := wrapAgentAIPhoneContractForSurface(session.surface, content)
	if err := ccPeerDialInject(plan.SocketPath, plan.PeerToken, injectText, plan.FromMode); err != nil {
		emitAgentAISteerAck(writeJSON, sessionID, messageID, "error", "无法把插话送进终端会话", "send_failed")
		return
	}
	if ccPeerWatchHeld(plan.JSONLPath, offset, ccPeerHeldWatchWindow) {
		emitAgentAISteerAck(writeJSON, sessionID, messageID, "error", "终端会话拒绝了这条插话，请在终端中处理", "held")
		return
	}
	m.mu.Lock()
	session.history = append(session.history, agentAIMessage{
		Role: "user", MessageID: messageID, Content: content, CreatedAt: time.Now().UTC(),
	})
	m.mu.Unlock()
	emitAgentAISteerAck(writeJSON, sessionID, messageID, "queued", "", "")
}

func (c *agentAICodexSteerControl) enqueue(msg agentAISteerMessage) (string, error) {
	if c == nil {
		return "", errors.New("codex steer control is unavailable")
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return "", errors.New("codex turn is no longer running")
	}
	if c.threadID == "" || c.turnID == "" {
		c.queue = append(c.queue, msg)
		c.mu.Unlock()
		return "queued", nil
	}
	id, payload := c.nextSteerPayloadLocked(msg)
	send := c.send
	c.mu.Unlock()
	if err := send(payload); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return "", err
	}
	return "queued", nil
}

func (c *agentAICodexSteerControl) markReady(threadID, turnID string) {
	if c == nil {
		return
	}
	type pendingSend struct {
		id      string
		payload map[string]interface{}
	}
	var sends []pendingSend
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	if strings.TrimSpace(threadID) != "" {
		c.threadID = strings.TrimSpace(threadID)
	}
	if strings.TrimSpace(turnID) != "" {
		c.turnID = strings.TrimSpace(turnID)
	}
	if c.threadID != "" && c.turnID != "" && len(c.queue) > 0 {
		queued := c.queue
		c.queue = nil
		sends = make([]pendingSend, 0, len(queued))
		for _, item := range queued {
			id, payload := c.nextSteerPayloadLocked(item)
			sends = append(sends, pendingSend{id: id, payload: payload})
		}
	}
	send := c.send
	c.mu.Unlock()

	for _, item := range sends {
		if err := send(item.payload); err != nil {
			c.completePending(item.id, "error", err.Error(), "send_failed")
		}
	}
}

func (c *agentAICodexSteerControl) nextSteerPayloadLocked(msg agentAISteerMessage) (string, map[string]interface{}) {
	c.nextID++
	id := "aliang_steer_" + strconv.FormatInt(c.nextID, 10)
	c.pending[id] = msg
	return id, map[string]interface{}{
		"method": "turn/steer",
		"id":     id,
		"params": map[string]interface{}{
			"threadId":            c.threadID,
			"expectedTurnId":      c.turnID,
			"clientUserMessageId": msg.MessageID,
			"input":               codexSteerInput(msg),
		},
	}
}

func (c *agentAICodexSteerControl) handleResponse(msg map[string]interface{}) bool {
	if c == nil {
		return false
	}
	id := fmt.Sprint(msg["id"])
	if strings.TrimSpace(id) == "" || id == "<nil>" {
		return false
	}
	c.mu.Lock()
	steer, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.mu.Unlock()
	if !ok {
		return false
	}
	if errObj, ok := msg["error"].(map[string]interface{}); ok {
		message := firstNonEmpty(remoteString(errObj, "message"), remoteString(errObj, "additionalDetails"), "codex turn/steer failed")
		code := remoteString(errObj, "code")
		if info, ok := errObj["codexErrorInfo"].(map[string]interface{}); ok {
			code = firstNonEmpty(remoteString(info, "code"), code)
		}
		emitAgentAISteerAck(c.write, c.sessionID, steer.MessageID, "error", message, code)
		return true
	}
	emitAgentAISteerAck(c.write, c.sessionID, steer.MessageID, "applied", "", "")
	return true
}

func (c *agentAICodexSteerControl) completePending(id, result, errMsg, code string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	steer, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.mu.Unlock()
	if ok {
		emitAgentAISteerAck(c.write, c.sessionID, steer.MessageID, result, errMsg, code)
	}
}

func (c *agentAICodexSteerControl) close(result, errMsg, code string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	items := make([]agentAISteerMessage, 0, len(c.queue)+len(c.pending))
	items = append(items, c.queue...)
	for _, item := range c.pending {
		items = append(items, item)
	}
	c.queue = nil
	c.pending = make(map[string]agentAISteerMessage)
	write := c.write
	c.mu.Unlock()

	for _, item := range items {
		emitAgentAISteerAck(write, c.sessionID, item.MessageID, result, errMsg, code)
	}
}

func (m *agentAIManager) setCodexSteerControl(sessionID string, runSeq int, control *agentAICodexSteerControl) {
	m.mu.Lock()
	if session := m.sessions[sessionID]; session != nil && session.runSeq == runSeq {
		session.codexSteer = control
	}
	m.mu.Unlock()
}

// runUserMessage 在 session 上派发一轮新的 AI run。message()（用户消息）与
// optionResponse()（用户方案选择续接）共用。调用者须已确认 session 存在且当前未在跑。

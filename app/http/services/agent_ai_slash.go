package services

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

func parseLocalSlashCommand(content string) (name, args string, ok bool) {
	if !strings.HasPrefix(content, "/") {
		return "", "", false
	}
	rest := content[1:]
	if rest == "" {
		return "", "", false
	}
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if c == ' ' || c == '\t' {
			return strings.ToLower(rest[:i]), strings.TrimSpace(rest[i:]), true
		}
	}
	return strings.ToLower(rest), "", true
}

// localSlashBuiltins is the set of agent-local slash commands handled without a
// model turn. Keep in sync with the catalogs' remote='local' entries
// (AliangPhoneServer server/src/agentCommands.ts, the phone's
// src/utils/agentCommands.ts, and scripts/local-agent.ts BUILTIN_COMMANDS).

var localSlashBuiltins = map[string]struct{}{
	"clear":   {},
	"model":   {},
	"help":    {},
	"cost":    {},
	"compact": {},
}

// handleLocalSlashCommand intercepts agent-local slash commands and runs them
// without spawning a model turn, replying with ai.run.started + ai.delta(status)
// + ai.done so the phone settles with a clear outcome. Returns true when handled
// (the caller then skips runUserMessage).

func (m *agentAIManager) handleLocalSlashCommand(session *agentAISession, messageID, content, provider string, writeJSON agentTerminalWriter) bool {
	name, args, ok := parseLocalSlashCommand(content)
	if !ok {
		return false
	}
	if _, isLocal := localSlashBuiltins[name]; !isLocal {
		return false // prompt-style (/review, custom .claude/commands) or unknown → normal turn
	}
	if name == "compact" {
		switch provider {
		case "codex":
			m.handleCodexCompactCommand(session, messageID, writeJSON)
			return true
		case "agentcore":
			m.handleAgentCoreCompactCommand(session, messageID, writeJSON)
			return true
		case "claude", "claudecode":
			m.mu.Lock()
			resumeID := strings.TrimSpace(session.resumeSessionID)
			m.mu.Unlock()
			if resumeID == "" {
				m.replyLocalSlashCommand(session, messageID, provider, writeJSON,
					"当前还没有可压缩的原生上下文。先对话几轮，再发送 /compact。")
				return true
			}
			// Claude Code 的 headless CLI 自己实现了 /compact（带 --resume 时
			// 压缩原生会话并接着用压缩后的上下文）。这里不能本地应答，必须让
			// 命令原样落成一个模型回合——runUserMessage 识别该形态后跳过手机
			// 契约包装与 TUI 路由，否则命令文本会变成普通用户消息。
			return false
		}
	}

	// 斜杠命令也是对话的一部分（spec §4.5）：agentcore 无原生转录，本地应答
	// 的斜杠命令不产生 run 回合，journal 是它唯一的历史留痕——必须先落头行与
	// 命令行。claude/codex 由各自原生转录承载（spec amendment），这里仅
	// provider=agentcore 落。origin 走统一推导（Task 10 helper，与 run 路径同
	// 源）。/compact 的 agentcore 分支在上面已提前 return，它的留账在
	// handleAgentCoreCompactCommand 内，不会走到这里（无重复落账）。
	if provider == "agentcore" {
		m.mu.Lock()
		projectPath, model, effort := session.projectPath, session.model, session.effort
		originSocket, surface := session.originSocket, session.surface
		m.mu.Unlock()
		now := time.Now().UTC().Format(time.RFC3339)
		m.coreJournal.ensureSessionHeader(session.id, projectPath, model, effort, now, agentCoreJournalOriginFor(originSocket, surface), session.disabledMCPServers, session.disabledSkills, session.approvalMode)
		m.coreJournal.appendUser(session.id, messageID, content, now)
	}

	assistantID := agentAssistantMessageID(messageID)
	_ = writeJSON(map[string]interface{}{
		"type":         models.AgentEventAIRunStarted,
		"session_id":   session.id,
		"message_id":   assistantID,
		"provider":     provider,
		"mode":         session.mode,
		"project_path": session.projectPath,
		"state":        "running",
	})

	line := m.applyLocalSlashCommand(session, name, args, provider, messageID, content)

	if provider == "agentcore" && line != "" {
		m.coreJournal.appendAssistant(session.id, assistantID, line, time.Now().UTC().Format(time.RFC3339))
	}

	_ = writeJSON(map[string]interface{}{
		"type":       models.AgentEventAIDelta,
		"session_id": session.id,
		"message_id": assistantID,
		"channel":    "stdout",
		"delta":      line + "\n",
	})
	_ = writeJSON(map[string]interface{}{
		"type":       models.AgentEventAIDone,
		"session_id": session.id,
		"message_id": assistantID,
	})
	return true
}

// replyLocalSlashCommand settles a slash command with a single local status
// line (run.started → ai.delta → ai.done) without spawning a model turn.

func (m *agentAIManager) replyLocalSlashCommand(session *agentAISession, messageID, provider string, writeJSON agentTerminalWriter, line string) {
	if writeJSON == nil {
		return
	}
	assistantID := agentAssistantMessageID(messageID)
	_ = writeJSON(map[string]interface{}{
		"type":         models.AgentEventAIRunStarted,
		"session_id":   session.id,
		"message_id":   assistantID,
		"provider":     provider,
		"mode":         session.mode,
		"project_path": session.projectPath,
		"state":        "running",
	})
	_ = writeJSON(map[string]interface{}{
		"type":       models.AgentEventAIDelta,
		"session_id": session.id,
		"message_id": assistantID,
		"channel":    "stdout",
		"delta":      line + "\n",
	})
	_ = writeJSON(map[string]interface{}{
		"type":       models.AgentEventAIDone,
		"session_id": session.id,
		"message_id": assistantID,
	})
}

// applyLocalSlashCommand performs the side effect (if any) for a local slash
// command and returns a single status line streamed back as the reply.
// messageID/content 是本条命令的用户行身份与原文：agentcore 侧 /clear 在
// reset 后要按原文重落命令行（见 clearAgentCoreSession），保真透传不重造。

func (m *agentAIManager) applyLocalSlashCommand(session *agentAISession, name, args, provider, messageID, content string) string {
	switch name {
	case "clear":
		// agentcore 无原生转录，journal 就是历史的唯一权威（run 装配从 journal
		// 读回）——只清内存历史不够，下一帧 create/message 的懒建分支会把磁盘
		// 上的历史原样装回，「清空」形同虚设。走专用清空：内存 + journal 一起清。
		if provider == "agentcore" {
			return m.clearAgentCoreSession(session, messageID, content)
		}
		// Detach from the current Claude session so the next turn starts FRESH
		// (no --resume). resumeSessionID may have been set at ai.session.create
		// or recaptured post-run (setAgentAIResumeSessionIDIfEmpty); clearing it
		// drops that binding. history is cleared too so the non-resume fallback
		// prompt (buildAgentAIPrompt) starts clean. The next turn spawns a fresh
		// claude run that captures a NEW session id, and continuity from there
		// is owned by Claude's own session manager (not agent-side history).
		m.mu.Lock()
		session.resumeSessionID = ""
		session.history = nil
		session.runSeq = 0
		session.finalizedRunSeq = 0
		m.mu.Unlock()
		return fmt.Sprintf("✓ /clear 已清空。下一轮将开启全新的 %s 会话。", agentAIProviderDisplayName(provider))
	case "model":
		newModel := strings.TrimSpace(args)
		m.mu.Lock()
		cur := session.model
		m.mu.Unlock()
		if newModel != "" {
			return "远程 /model 不会修改服务器会话配置。请在手机会话工具中切换模型，修改将在下一轮生效。"
		}
		if cur == "" {
			return "当前未指定模型(使用服务器或 CLI 默认)。请在手机会话工具中切换。"
		}
		return fmt.Sprintf("当前模型: %s。请在手机会话工具中切换。", cur)
	case "help":
		return "本机命令(remote=local): /clear  /model  /help  /cost  /compact\n注: 模型切换请使用会话设置；/clear 会真实清空内核会话上下文（内存 + journal），/compact 对已连接的 Claude/Codex/内置内核执行真实压缩。"
	case "cost":
		// Per-turn usage is emitted as ai.usage but not accumulated on the
		// session, so there is no running total to report here.
		return "⚠ headless agent 未累计会话用量。请在桌面端查看 token / 费用统计。"
	case "compact":
		return fmt.Sprintf("⚠ 无法确定 %s 的原生会话类型，/compact 未执行。请先在会话设置里固定 provider。", agentAIProviderDisplayName(provider))
	}
	return ""
}

// agentAINativeCompactTurn reports whether this turn is a raw /compact passed
// through to the Claude CLI instead of being answered locally.
//
// Claude Code's headless binary implements /compact itself: with --resume it
// summarizes the native session and continues from the compacted context.
// Agent-side wrapping (prompt builder, phone contract, history append) would
// turn the command text into an ordinary user message and the CLI would never
// see the slash command, so this turn runs bare. Only claude/claudecode
// qualify — codex has its own compact path, and agentcore compaction is managed
// by the kernel.

func agentAINativeCompactTurn(content, provider, resumeSessionID string) bool {
	if strings.TrimSpace(resumeSessionID) == "" {
		return false
	}
	switch provider {
	case "claude", "claudecode":
	default:
		return false
	}
	name, _, ok := parseLocalSlashCommand(content)
	return ok && name == "compact"
}

func agentAIProviderDisplayName(provider string) string {
	switch provider {
	case "codex":
		return "Codex"
	case "opencode":
		return "OpenCode"
	case "claude", "claudecode":
		return "Claude Code"
	default:
		return "AI"
	}
}

func (m *agentAIManager) handleCodexCompactCommand(session *agentAISession, messageID string, writeJSON agentTerminalWriter) {
	m.mu.Lock()
	threadID := strings.TrimSpace(session.resumeSessionID)
	projectPath := session.projectPath
	mode := session.mode
	m.mu.Unlock()
	assistantID := agentAssistantMessageID(messageID)
	_ = writeJSON(map[string]interface{}{
		"type":         models.AgentEventAIRunStarted,
		"session_id":   session.id,
		"message_id":   assistantID,
		"provider":     "codex",
		"mode":         mode,
		"project_path": projectPath,
		"state":        "running",
	})
	if threadID == "" {
		_ = writeJSON(map[string]interface{}{
			"type": models.AgentEventAIDelta, "session_id": session.id, "message_id": assistantID,
			"channel": "stdout", "delta": "当前 Codex 会话还没有可压缩的原生线程。\n",
		})
		_ = writeJSON(map[string]interface{}{"type": models.AgentEventAIDone, "session_id": session.id, "message_id": assistantID})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := compactCodexThread(ctx, projectPath, threadID); err != nil {
		_ = writeJSON(agentAIErrorPayload(session.id, messageID, err))
		return
	}
	_ = writeJSON(map[string]interface{}{
		"type": models.AgentEventAIDelta, "session_id": session.id, "message_id": assistantID,
		"channel": "stdout", "delta": "✓ Codex 原生线程已压缩。\n",
	})
	_ = writeJSON(map[string]interface{}{
		"type": models.AgentEventAIDone, "session_id": session.id, "message_id": assistantID,
		"codex_thread_id": threadID, "source_session_id": threadID,
	})
}

func compactCodexThread(ctx context.Context, projectPath, threadID string) error {
	path, err := lookPathCLI("codex")
	if err != nil {
		return err
	}
	if !codexAppServerAvailable() {
		return errors.New("Codex app-server is unavailable; cannot compact this thread")
	}
	cmd := newBackgroundCommandContext(ctx, path, "app-server", "--stdio")
	cmd.Dir = projectPath
	cmd.Env = agentChildProcessEnv()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	var stderrBuf strings.Builder
	var stderrWG sync.WaitGroup
	stderrWG.Add(1)
	go func() {
		defer stderrWG.Done()
		captureAgentAIStderr(stderr, &stderrBuf)
	}()
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		stderrWG.Wait()
	}()
	send := func(payload map[string]interface{}) error {
		raw, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return marshalErr
		}
		_, writeErr := stdin.Write(append(raw, '\n'))
		return writeErr
	}
	if err := send(map[string]interface{}{
		"method": "initialize", "id": 0,
		"params": map[string]interface{}{"clientInfo": map[string]interface{}{"name": "alianggate", "title": "Aliang Agent", "version": "0.1.0"}},
	}); err != nil {
		return err
	}
	if err := send(map[string]interface{}{"method": "initialized", "params": map[string]interface{}{}}); err != nil {
		return err
	}
	if err := send(map[string]interface{}{"method": "thread/compact/start", "id": 1, "params": map[string]interface{}{"threadId": threadID}}); err != nil {
		return err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var message map[string]interface{}
		if json.Unmarshal(scanner.Bytes(), &message) != nil || fmt.Sprint(message["id"]) != "1" {
			continue
		}
		if detail, failed := codexAppServerResponseError(message); failed {
			return fmt.Errorf("Codex thread compact failed: %s", detail)
		}
		return nil
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if detail := strings.TrimSpace(stderrBuf.String()); detail != "" {
		return fmt.Errorf("Codex app-server exited during compact: %s", truncateForCloud(detail))
	}
	return errors.New("Codex app-server exited before compact completed")
}

// steerReviveViaMessage 把「内存里没有、磁盘上有历史」的休眠会话收到的插话
// 转正为新消息，委托 message() 的懒建恢复路径（spec §4.3 跨端/重启续聊）。
// 返回 true 表示已接管（成功唤醒或已向客户端回错误帧），调用方直接 return；
// 返回 false 表示不适用（非 agentcore / journal 无记录 / 项目路径校验失败
// 属于真未知会话），调用方保持原 not_running 报错。
//
// 触发场景（2026-11-13 手机实锤）：agent 重启/重装升级后内存注册表清空、
// 手机 vibe_sessions 列表照常展示磁盘快照，用户对休眠会话插话命中 steer 的
// "ai session not found"——同一场景 ai.message 走懒建恢复，两条通道语义
// 不一致。修复后用户感知只是首回合稍慢（装回历史），不再报错。
//
// 委托帧字段口径：
//   - provider/project_path/model/effort 等运行参数从 journal 头行恢复（与
//     ai.message 懒建同源，载荷权威、头行兜底的纪律在 message 内自洽）；
//   - source_session_id 带绑定表里的原生会话 id（confirmed 绑定供种 resume，
//     细节同 message 懒建分支的 seedConfirmedBindingResumeLocked）；
//   - surface/origin 标记照抄原帧，保证回程扇出寻址与手机渲染不变；
//   - attachments 丢弃并显式告知：steer 的附件已在上游半物化，与 message
//     的附件管线（附件 id 物化）语义不同，不能二次穿越。

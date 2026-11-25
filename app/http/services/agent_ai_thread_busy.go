package services

// codex 线程单写者冲突的「询问式 fork」处置（2026-11-25 P0+P1，用户拍板）：
//
// 手机续聊一个原进程仍存活的 tui/ide-origin codex 会话时，codex 0.172 以
// -32600 "thread-store conflict: already has an active writer" 拒绝 resume
// （单写者锁，写者 = 用户自己的 VS Code/终端）。此前的表现是泛化失败且无
// 原因。本文件三件事：
//
//  1. P0：询问不可用/用户拒绝 fork 时，终态 ai.error 带稳定 error_code
//     "codex_thread_busy" 与特征文本（"codex thread busy: ..."），手机按
//     tui_busy 同族映射成「原会话仍在其他 Codex 客户端中打开」文案。
//  2. P1：先发 tool_ask.request 询问（选项：我先去关闭原会话（默认）/
//     复制会话继续），短 deadline（默认 10min，env
//     ALIANG_AI_THREAD_BUSY_ASK_TIMEOUT），到期强制收帧（硬性原则：所有
//     异常必须有状态返回；内核 timeout 必须短于 server tool_ask SLA，
//     由内核先翻卡）。默认不 fork；绝不修改原会话——fork 走 codex 原生
//     thread/fork 复制出新线程，源 rollout/写锁/原进程零接触。
//  3. fork 二趟撞锁/不可用时的压平历史 fresh 兜底（fork_fallback_fresh
//     notice），保证「复制继续」永远有出路。
//
// 询问通道复用 tool_ask bridge 的 pending/deliver 机器（按 interaction_id
// 路由，与 run 来源解耦），但不走 MCP token 注册——直接构造
// agentToolAskRunContext 指向本回合的 writeJSON。中文文案为本部署受众
// 取舍，与 server 侧「AI 正在等待你的回答。」通知同语言。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
)

const (
	// agentAIThreadBusyOptionClose 默认选项：不 fork。用户去关闭原客户端后
	// 重发消息即可直接 resume（锁已释放）。
	agentAIThreadBusyOptionClose = "close_and_retry"
	// agentAIThreadBusyOptionFork 显式选择：复制会话继续。
	agentAIThreadBusyOptionFork = "fork_continue"
	// agentAIThreadBusyDefaultAskTO 询问 deadline。必须短于 server 侧
	// tool_ask SLA（默认 25min）——内核超时先到，主动 tool_ask.cancel 翻卡，
	// 避免卡在手机上永挂。
	agentAIThreadBusyDefaultAskTO = 10 * time.Minute
	agentAIThreadBusyAskTOEnv     = "ALIANG_AI_THREAD_BUSY_ASK_TIMEOUT"
	// agentAICodexThreadBusyErrorCode 手机/服务端分类用的稳定细分码
	// （克隆 tui_busy 的 agentErrorCode 通道）。
	agentAICodexThreadBusyErrorCode = "codex_thread_busy"
)

// agentAICodexThreadBusyError 实现 ErrorCode()，agentAIErrorPayload 据此在
// ai.error 帧上带 error_code（先例：agentExternalTUIBusyError → "tui_busy"）。

type agentAICodexThreadBusyError struct{ msg string }

func (e *agentAICodexThreadBusyError) Error() string { return e.msg }
func (e *agentAICodexThreadBusyError) ErrorCode() string {
	return agentAICodexThreadBusyErrorCode
}

// emitAgentAICodexThreadBusyError 落 P0 终态。特征文本以 "codex thread busy:"
// 开头（字符串分类兜底），error_code 走结构化通道。
func emitAgentAICodexThreadBusyError(writeJSON agentTerminalWriter, run *agentAIRun, threadID, because string) {
	if writeJSON == nil {
		return
	}
	msg := fmt.Sprintf(
		"codex thread busy: codex thread %s is open in another Codex client (VS Code/terminal) and cannot be resumed here (%s); close that client and resend, or retry and choose 复制会话继续 to fork",
		strings.TrimSpace(threadID), because,
	)
	_ = writeJSON(agentAIErrorPayload(run.sessionID, run.messageID, &agentAICodexThreadBusyError{msg: msg}))
}

// emitAgentAICodexForkFallbackNotice fork 兜底 notice（信息性帧；未知 code 的
// 旧手机端会整帧丢弃，前向安全）。
func emitAgentAICodexForkFallbackNotice(writeJSON agentTerminalWriter, run *agentAIRun) {
	if writeJSON == nil {
		return
	}
	_ = writeJSON(map[string]interface{}{
		"type":       models.AgentEventAIRunNotice,
		"session_id": run.sessionID,
		"message_id": agentAssistantMessageID(run.messageID),
		"code":       "fork_fallback_fresh",
		"detail":     "native thread busy; fork unavailable, continuing with flattened history as a fresh conversation",
	})
}

// handleAgentAICodexThreadBusy 驱动层入口（runCLI 的 codex 分支，
// agentAIRunThreadBusy outcome 到此）。每回合至多询问一次
// （threadBusyHandled 钉死，fork 二趟再撞锁直接兜底，绝不弹第二张卡）。
func (m *agentAIManager) handleAgentAICodexThreadBusy(ctx context.Context, run *agentAIRun, writeJSON agentTerminalWriter) {
	threadID := strings.TrimSpace(run.resumeSessionID)
	if run.threadBusyHandled || threadID == "" {
		// thread/resume 之外（fork 二趟）不应进本函数；防御性直落 P0。
		emitAgentAICodexThreadBusyError(writeJSON, run, threadID, "resume")
		return
	}
	run.threadBusyHandled = true

	choice, delivered := m.askAgentAIThreadBusyFork(ctx, run, writeJSON, threadID)
	if ctx.Err() != nil {
		// 等待期间回合被停/取消：cancel 帧已发，终态由停机路径统一收口
		// （与 resumeMissing 的 ctx 静默同形，不双发）。
		return
	}
	if !delivered {
		// 询问帧没送出去（write 通道故障）→ 优雅降级 P0，不阻塞用户。
		emitAgentAICodexThreadBusyError(writeJSON, run, threadID, "ask delivery failed")
		return
	}
	if choice != agentAIThreadBusyOptionFork {
		// 默认路径：用户选了关闭原会话/没选/作答过期 → P0 终态。
		emitAgentAICodexThreadBusyError(writeJSON, run, threadID, "user chose close-and-retry")
		return
	}

	// P1 fork 二趟：thread/fork 复制源线程历史 → 新线程上跑本回合。
	// 源线程的 rollout/写锁/原进程零接触。
	forkSource := threadID
	run.forkFromSessionID = forkSource
	run.resumeSessionID = ""
	outcome := m.runCodexAppServer(ctx, *run, writeJSON)
	if outcome == agentAIRunThreadBusy && ctx.Err() == nil {
		// fork 也被单写者拒（daemon 对持锁源线程 fork 的行为未知，防御兜底）：
		// 压平历史 fresh 兜底，保证「复制继续」永远有出路。
		logger.Warn(fmt.Sprintf("ai.codex.thread-busy: session=%s runSeq=%d fork of %s also writer-conflicted; falling back to flattened fresh",
			run.sessionID, run.runSeq, forkSource))
		emitAgentAICodexForkFallbackNotice(writeJSON, run)
		run.forkFromSessionID = ""
		run.prompt = firstNonEmpty(run.freshPrompt, run.prompt)
		_ = m.runCodexAppServer(ctx, *run, writeJSON)
	}
	// 其余 outcome（Done/停机）：帧已由 pass 内部收口——fork 成功时 ai.done
	// 带 codex_thread_id=新 id + forked_from，服务端据此换绑。
}

// askAgentAIThreadBusyFork 发 tool_ask.request 询问并阻塞等作答。
// 返回 (selectedOptionID, delivered)：delivered=false 表示询问未送达/超时/取消
// （cancel 帧已由本函数收口）；delivered=true 时 choice 为用户所选项
// （作答过期/异常一律归到默认不 fork）。
func (m *agentAIManager) askAgentAIThreadBusyFork(ctx context.Context, run *agentAIRun, writeJSON agentTerminalWriter, threadID string) (string, bool) {
	b := m.ensureToolAskBridge()
	askTO := resolveEnvDuration(agentAIThreadBusyAskTOEnv, agentAIThreadBusyDefaultAskTO)

	rc := &agentToolAskRunContext{
		sessionID: run.sessionID,
		runID:     run.runID,
		deviceID:  currentAgentDeviceIDForNotify(),
		writeJSON: writeJSON,
	}
	interactionID := "ta_" + uuid.NewString()
	pending := &pendingToolAsk{run: rc, answer: make(chan models.AgentToolAskResponse, 1)}
	b.mu.Lock()
	b.pending[interactionID] = pending
	b.mu.Unlock()
	popPending := func() {
		b.mu.Lock()
		delete(b.pending, interactionID)
		b.mu.Unlock()
	}
	// 全出口兜底清 pending（正常路径各分支已 pop，这里防泄漏）。
	defer popPending()

	toolInput := map[string]interface{}{
		"title": fmt.Sprintf(
			"原 Codex 会话 %s 正在另一个 Codex 客户端（如 VS Code）中打开，无法直接续聊。要怎么处理？",
			threadID,
		),
		"options": []map[string]interface{}{
			{
				"id":          agentAIThreadBusyOptionClose,
				"label":       "我去关闭原会话后重试（默认）",
				"description": "关闭 VS Code/终端里的原会话后，重新发送这条消息即可直接续聊；本回合先以失败结束。",
			},
			{
				"id":          agentAIThreadBusyOptionFork,
				"label":       "复制会话继续",
				"description": "把原会话历史复制成一个全新 Codex 会话并继续本回合；原会话保持原样不动。",
			},
		},
		"allow_custom": false,
		"multi":        false,
	}
	inputJSON, err := json.Marshal(toolInput)
	if err != nil {
		return "", false
	}
	payload := map[string]interface{}{
		"type":           models.AgentEventToolAskRequest,
		"session_id":     rc.sessionID,
		"interaction_id": interactionID,
		"device_id":      rc.deviceID,
		"tool_name":      models.AgentToolAskTools[0],
		"tool_input":     json.RawMessage(inputJSON),
		"expires_at":     time.Now().Add(askTO).UnixMilli(),
		"run_id":         rc.runID,
		"event_seq":      rc.nextSeq.Add(1),
	}
	logger.Info(fmt.Sprintf("thread-busy: ask session=%s run=%s interaction=%s thread=%s timeout=%s",
		run.sessionID, run.runID, interactionID, threadID, askTO))
	if err := writeAgentAIApprovalRequest(ctx, writeJSON, payload, 3, time.Second); err != nil {
		logger.Warn(fmt.Sprintf("thread-busy: ask delivery failed session=%s interaction=%s error=%v",
			run.sessionID, interactionID, err))
		return "", false
	}

	// 看门狗豁免（与 requestApproval 同形）：等待期不计入空闲杀。
	run.activity.setAwaitingApproval(true)
	defer run.activity.setAwaitingApproval(false)

	timer := time.NewTimer(askTO)
	defer timer.Stop()
	select {
	case resp := <-pending.answer:
		selected := ""
		if resp.Answer != nil {
			selected = strings.TrimSpace(resp.Answer.SelectedOptionID)
		}
		if resp.Status == models.AgentToolAskStatusAnswered && selected != "" {
			b.emitResolved(rc, interactionID, resp.Answer)
			return selected, true
		}
		// 过期/空作答：默认不 fork（用户拍板的默认语义）。已无 pending 卡
		// 可翻（server 侧已终态），不发 cancel。
		return "", true
	case <-timer.C:
		popPending()
		b.emitCancel(rc, interactionID, models.AgentToolAskReasonTimeout)
		return "", false
	case <-ctx.Done():
		popPending()
		b.emitCancel(rc, interactionID, models.AgentToolAskReasonRunEnded)
		return "", false
	}
}

package services

import (
	"fmt"
	"strings"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
)

type agentAIOutputFormat string

const (
	agentAIOutputText             agentAIOutputFormat = "text"
	agentAIOutputCodexJSON        agentAIOutputFormat = "codex_json"
	agentAIOutputClaudeStreamJSON agentAIOutputFormat = "claude_stream_json"
	agentAIOutputOpenCodeJSON     agentAIOutputFormat = "opencode_json"
)

// agentAIOptionSystemPrompt 引导 Claude 在需要用户多方案抉择时，输出结构化 aliang-options 块，
// 以便 agent 提取并发 ai.option.request。claude(--append-system-prompt)与
// agentcore(拼进 system)两路注入；codex 不注入（走原生 requestUserInput 映射）。
// v2 契约：新增 header/summary/response/previews 全部可选；围栏换行格式是硬规则
// （JSON 粘着标签行/收尾行会被提取正则静默丢弃），必须向模型言明。

const agentAIOptionSystemPrompt = `When you have multiple viable approaches/options and the best choice depends on user preference, you MUST let the user choose instead of deciding for them. Present them as a fenced code block with language tag aliang-options, formatted exactly: the tag line contains ONLY the three backticks followed by aliang-options and ends with a newline (never append the tag to the end of a sentence line — it must start on its own line), the next line is exactly one JSON object and nothing else, and the closing three backticks are alone on their own line and must always be emitted (never omit them and never substitute an ellipsis) — a JSON glued to the tag line or to the closing fence, or prose right after the JSON, is silently discarded. JSON shape: {"v":2,"title":string,"header":string,"summary":string,"options":[{"id":string,"label":string,"description":string,"response":string,"previews":[string]}],"allow_custom":bool,"multi":bool}. Only "title" and "options" are required; every other field is optional — omit anything you don't need. Rules: "id" is a short stable ASCII slug (letters, digits, hyphen, underscore; max 48 chars; unique per option); never reuse the label as the id. "label" is what the user reads — plain text only, no markdown syntax and no backticks (emoji/kaomoji are fine), aim under 60 chars. "description" is one short sentence. "response" (write only when it helps) is the receipt text returned as the user's answer when that option is picked — phrase it as a confirmation of that choice. "previews" (write only when it helps, e.g. commands or a short code sketch) is up to 8 one-line strings shown under the option. "header" is a category tag of 16 chars or fewer shown above the title. "summary" is one or two sentences of context. Maximum 8 options. Set "multi":true only when the user should pick several options at once — their picks are sent back together as one answer. Set "allow_custom":true when a free-text answer makes sense. After the fence, stop and wait for the user's choice in the next message; do not proceed on assumption and do not list the options again as prose.`

func newAgentAIManager() *agentAIManager {
	m := &agentAIManager{
		sessions:                 make(map[string]*agentAISession),
		approvals:                make(map[string]*agentAIApprovalWaiter),
		completedApprovals:       make(map[string]*agentAICompletedApproval),
		codexInputs:              make(map[string]*agentAICodexInputWaiter),
		pendingTerminals:         make(map[string]map[string]interface{}),
		pendingSubagentTerminals: make(map[string]map[string]interface{}),
		pendingSubagentStamp:     make(map[string]uint64),
		pendingApprovalRequests:  make(map[string]map[string]interface{}),
		bindings:                 make(map[string]agentAIBindingRecord),
		processedRuns:            make(map[string]agentAIProcessedRun),
		coreJournal:              newAgentCoreJournal(agentCoreJournalDir()),
		aiSockets:                make(map[string]*agentAISocket),
	}
	return m
}

func (m *agentAIManager) runEmitter(run agentAIRun, write agentTerminalWriter) *agentAIRunEmitter {
	e := newAgentAIRunEmitter(run, write)
	// Only protocol-v2 cloud runs have a Server ACK peer. Local in-app chat and
	// legacy servers omit run_id; journaling those terminals would leak forever.
	if strings.TrimSpace(run.runID) != "" {
		e.onTerminal = func(payload map[string]interface{}) error {
			if binding, ok := m.bindingForConversation(run.sessionID); ok {
				payload["native_session_id"] = binding.NativeSessionID
				payload["source_session_id"] = binding.NativeSessionID
				payload["binding_version"] = binding.BindingVersion
			}
			// One line per run terminal. Its absence is how the 2026-10-21
			// incident stayed invisible: the run died with no trace of whether a
			// terminal was ever produced, journaled, or merely failed to send.
			logger.Info(fmt.Sprintf("[ai.run.terminal] status=%s type=%s session=%s run=%s seq=%v",
				remoteString(payload, "status"), remoteString(payload, "type"), run.sessionID, run.runID, payload["event_seq"]))
			if err := m.rememberPendingTerminal(payload); err != nil {
				// 持久化失败不能拦下帧本身：这里早退会让 emit 连写线都不做——
				// 终态帧对全端静默丢失，健康连接数天不重连=云端永卡 running
				// （审计 2026-11-17 P1）。内存账在 remember 里已落（重连即重放），
				// 这里只降级成响亮日志继续走。
				logger.Error(fmt.Sprintf("[ai.run.terminal] 持久化失败 run=%s session=%s err=%v——终态帧照常上行,重连由内存账重放", run.runID, run.sessionID, err))
			}
			return m.completeProcessedRun(run.runID, payload)
		}
		// 子 agent 终态帧断线暂存（P2 #4）：仅 v2 云端 run 接线（与 onTerminal
		// 同口径——本地纯聊天无重连补发通道，暂存只会泄漏）。 幂等投递：服务端
		// (session,msg,task) 单行 upsert + 终态防回退。
		e.onSubagentTerminalDrop = func(payload map[string]interface{}) {
			m.rememberPendingSubagentTerminal(payload)
		}
	}
	// 跨端同态（spec §4.1）：origin 仍内联，事件副本扇出到订阅 socket。
	// originSocketID 从消息分发路径盖的 __origin_socket 来（Task 5 起
	// messageRun/保留绑定错误发射器/optionResponse/runUserMessage 四路
	// 填充）；恒空串=无 origin 去重，扇出与 origin 内联并存（仅测试/内部
	// 路径）。
	e.write = m.fanOutWrite(run.originSocketID, run.sessionID, e.write)
	return e
}

// registerAISessionLocked stores a new session, evicting the oldest idle
// session first if the resident map is at capacity. The session must NOT
// already be present — both call sites (create + message lazy-create) resolve
// or update an existing session before reaching here. MUST be called with m.mu
// held.

func (m *agentAIManager) registerAISessionLocked(session *agentAISession) {
	m.evictOldestIdleAISessionLocked()
	m.sessions[session.id] = session
	// create-only 会话不在磁盘指纹面,纳管即打脏标让 digest tick 放行即时
	// hello(2026-11-07 S1 实时性)。
	markAgentVibeMemorySnapshotDirty()
}

// evictOldestIdleAISessionLocked drops the non-running session (cancel == nil)
// with the oldest lastActiveAt when the resident map is at/over
// agentAISessionResidentCap, bounding memory (each session holds up to
// agentAIHistoryCaptureMaxBytes). It never evicts a running turn; if every
// resident session is mid-turn it is a no-op and the caller adds anyway
// (temporary overshoot, re-bounded as soon as a turn settles). Eviction is
// silent to the server: an evicted conversation rebuilds on its next ai.message
// (lazy-create) and resumes from disk via resume_session_id. MUST be called
// with m.mu held.

func (m *agentAIManager) evictOldestIdleAISessionLocked() {
	if len(m.sessions) < agentAISessionResidentCap {
		return
	}
	var victim *agentAISession
	for _, s := range m.sessions {
		if s.cancel != nil {
			continue // never evict a running turn
		}
		if victim == nil || s.lastActiveAt.Before(victim.lastActiveAt) {
			victim = s
		}
	}
	if victim == nil {
		return // every resident session is running — allow temporary overshoot
	}
	m.clearPendingApprovalsLocked(victim.id, victim.runSeq, models.AgentAIApprovalDecisionCancel)
	victim.pendingOption = nil
	delete(m.sessions, victim.id)
	// reserved 绑定随会话一并丢弃：reserved 是 server 下发的占位 id，会话没了
	// 它就失去兑现语境，留着还会在「原生 id 已绑定别的会话」检查里误导后续
	// confirm 的报错方向。confirmed 是合法 resume 锚（驱逐后 lazy-create 仍能
	// 从它恢复），保留。persistIdentityState 自带锁，不能在持锁临界区里调，
	// 丢 goroutine 异步落盘，失败仅日志。
	reservedDropped := ""
	if binding, ok := m.bindings[victim.id]; ok && binding.State == "reserved" {
		reservedDropped = binding.NativeSessionID
		delete(m.bindings, victim.id)
	}
	logger.Info(fmt.Sprintf("ai.session: evicted idle session %s to bound resident set (cap=%d, now=%d)",
		victim.id, agentAISessionResidentCap, len(m.sessions)))
	if reservedDropped != "" {
		logger.Info(fmt.Sprintf("ai.session: evicted session %s 的 reserved 绑定 %q 一并丢弃", victim.id, reservedDropped))
		go func(sessionID, native string) {
			if err := m.persistIdentityState(); err != nil {
				logger.Warn(fmt.Sprintf("ai.session: evict reserved binding persist_failed session=%s native=%s err=%v", sessionID, native, err))
			}
		}(victim.id, reservedDropped)
	}
}

// approvalService returns the AgentService that owns this manager (used to
// evaluate the device approval policy), falling back to the process-wide
// shared service for standalone test managers that have no owner.

func (m *agentAIManager) approvalService() *AgentService {
	if m.service != nil {
		return m.service
	}
	return GetSharedAgentService()
}

// agentAIActivity tracks whether an AI run is alive. A run is considered active
// while it emits output (bump), while it is blocked awaiting a human approval
// decision, or while Claude has dispatched tool/subagent work and is waiting for
// the corresponding tool_result. The watchdog cancels only when the run goes
// silent for longer than agentAIIdleWindow without one of those waits in flight;
// agentAIHardCeiling is a runaway backstop. nil-safe so call sites need no guards.

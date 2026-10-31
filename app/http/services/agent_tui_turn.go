package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
)

// TUI 执行回合的机器可读错误码。与 agentExternalTUIBusyError 同构：实现
// ErrorCode() interface，由 agentAIErrorPayload 经 errors.As 自动拾取，挂到
// ai.error 的 error_code 字段上（agent_ai.go:7613-7617）——服务端与手机据此
// 分支，无需对错误字符串做脆弱匹配。
const (
	agentTUIErrUnavailable    = "tui_unavailable"       // 注入前判定中途失效（TUI 退出/记录消失）或回合超上限
	agentTUIErrInjectFailed   = "tui_inject_failed"     // 拨号或写入 UDS 失败
	agentTUIErrHeld           = "tui_held"              // 接收方（bypass 门）静默扣留注入消息
	agentTUIErrNoTurn         = "tui_no_turn"           // 消息被接收（入队）但窗口内从未起跑（2026-10-30 事故形态）
	agentTUIErrTranscriptLost = "tui_transcript_lost"   // tail 期间 transcript 消失
	agentTUIErrAttachment     = "tui_attachment_failed" // 附件下载失败（注入前）
)

// agentTUITurnError 是注入 TUI 回合的结构化失败。msg 面向人（日志/手机提示），
// code 面向程序。
type agentTUITurnError struct {
	code string
	msg  string
}

func (e agentTUITurnError) Error() string { return e.msg }

func (e agentTUITurnError) ErrorCode() string { return e.code }

func newAgentTUITurnError(code, msg string) error {
	return agentTUITurnError{code: code, msg: msg}
}

// tuiTurnInput 是路由判定的输入快照。全部字段在调度前一次性取到，避免判定
// 过程中读可变状态。
type tuiTurnInput struct {
	Home            string
	Provider        string
	NativeSessionID string
	ReadOnly        bool
	HasGoalIdentity bool
	// JSONLPath 由调用方经 ccPeerSessionJSONL 解析后传入；为空时本函数自行解析。
	JSONLPath string
}

// tuiTurnPlan 描述一次「注入 TUI」的执行计划。字段在判定时一次性快照，执行
// 期间不再重读 pid 记录（记录可能被 CC 重写）。
type tuiTurnPlan struct {
	Home            string
	NativeSessionID string
	SocketPath      string
	PID             int
	JSONLPath       string
	PeerToken       string
	// FromMode 是接收方 TUI 的 permission class（"bypass"/"prompting"/""），
	// CC 2.1.280 的入站 parity 门会按它决定是否扣留（见 ccPeerTUIPermissionClass）。
	FromMode string
	// JSONLOffset 是注入前的 transcript 大小，tail 桥从这里开始读。
	JSONLOffset int64
}

// tuiTurnDecide 实现 spec §4.2 的七道门。任一不满足返回 nil（调用方落回既有
// spawn 路径，行为与今天完全一致）。
//
// 这是纯判定：除读 pid 记录 / transcript 元数据外无副作用，便于单测全覆盖。
// 调用方必须把它放在任何 run 状态变更之前——注入失败不回退 spawn，
// 「一次回合只走一条路」是不变量（spec §4.5）。
func tuiTurnDecide(in tuiTurnInput) *tuiTurnPlan {
	// ① provider：claude/claudecode/auto。入参已被 normalizeAgentAIProvider
	// （agent_execution_guard.go:438）归一化成 auto|codex|claude|claudecode|
	// opencode 小写，这里再做一次 Trim/ToLower 是防御拷贝。
	//
	// ⚠️ 已知限制：auto 在 spawn 路径可能解析到 codex，但注入路径只会命中
	// claude 的 pid 记录——即「auto + 存在空闲 claude TUI」时回合固定走 TUI。
	// 这是有意的（用户决策：注入回合用 TUI 自身环境）；用户显式选 codex 时
	// provider 就是 codex，本函数直接返回 nil 走 spawn。
	switch strings.TrimSpace(strings.ToLower(in.Provider)) {
	case "claude", "claudecode", "auto":
	default:
		return nil
	}
	// ② 必须有 native id：fresh spawn 没有可注入的对话
	nativeID := strings.TrimSpace(in.NativeSessionID)
	if nativeID == "" {
		return nil
	}
	// ③ goal/fork 回合有沙箱与报告契约，一律不进 TUI 路径
	if in.ReadOnly || in.HasGoalIdentity {
		return nil
	}
	home := strings.TrimSpace(in.Home)
	if home == "" {
		return nil
	}
	// ④⑤ live 记录 + idle。busy 交给 guardExternalTUIClaudeSpawn 的 tui_busy
	// 拒绝路径——本函数不负责「拒绝」，只负责「改道」。
	record, live := liveClaudeTUIRecord(home, nativeID)
	if !live {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(record.Status), "idle") {
		return nil
	}
	// ⑥ unix UDS 能力（Windows named pipe 是 v2，明确不支持）
	socketPath := strings.TrimSpace(record.MessagingSocketPath)
	if socketPath == "" || !strings.HasPrefix(socketPath, "/") {
		return nil
	}
	// ⑦ transcript 可定位：tail 桥的前提
	jsonlPath := strings.TrimSpace(in.JSONLPath)
	if jsonlPath == "" {
		jsonlPath = ccPeerSessionJSONL(home, nativeID)
	}
	if jsonlPath == "" {
		return nil
	}
	info, err := os.Stat(jsonlPath)
	if err != nil || info.Size() <= 0 {
		return nil
	}
	return &tuiTurnPlan{
		Home:            home,
		NativeSessionID: nativeID,
		SocketPath:      socketPath,
		PID:             record.PID,
		JSONLPath:       jsonlPath,
		PeerToken:       loadClaudePeerToken(home, record.PID),
		FromMode:        ccPeerTUIPermissionClass(jsonlPath),
		JSONLOffset:     info.Size(),
	}
}

// transcriptLineKind 是 tail 桥对 transcript 一行的分类。
type transcriptLineKind int

const (
	transcriptIgnore    transcriptLineKind = iota // 元数据/噪声/空/坏行
	transcriptUserEcho                            // 真实用户消息（含我们注入的那条）
	transcriptAssistant                           // 主链 assistant 块
	transcriptTurnEnd                             // CC 亲写的回合结束标记（turn_duration / stop_hook_summary）
)

// transcriptLocalPrefixes 是 CC 写进 transcript 的本地命令回显前缀。它们不是
// 用户对话内容，回显到手机只会制造噪声（2026-10-20 在本机 transcript 实测：
// <local-command-caveat>/<command-name>/<local-command-stdout> 三类都存在）。
var transcriptLocalPrefixes = []string{
	"<command-name>",
	"<local-command-caveat>",
	"<local-command-stdout>",
}

// classifyTranscriptLine 把一行 transcript 分类。transcript 的 user 行有三类
// 噪声必须过滤：isMeta 本地命令回显、本地命令前缀内容、tool_result block——
// 不过滤就会把 TUI 的本地命令当成对话回显到手机。
//
// system 行里的 turn_duration / stop_hook_summary 是 CC 在**每个回合结束时**
// 亲写的结束标记（2026-10-24 在本机 2.1.286 实测：与真实回合 1:1 对齐，成对
// 出现、间隔 <5ms）。它们是 TUI 回合完成判定的最权威信号——比「quiet 轮 +
// pid 记录 idle」的启发式强得多；分类器此前把它们丢进 default 忽略，正是
// 「回合早结束、手机却挂到超时失败」的根因之一。
func classifyTranscriptLine(line []byte) transcriptLineKind {
	if len(bytes.TrimSpace(line)) == 0 {
		return transcriptIgnore
	}
	var row struct {
		Type        string `json:"type"`
		Subtype     string `json:"subtype"`
		UUID        string `json:"uuid"`
		IsSidechain bool   `json:"isSidechain"`
		IsMeta      bool   `json:"isMeta"`
		Message     struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &row); err != nil {
		return transcriptIgnore
	}
	switch row.Type {
	case "user":
		if row.IsMeta || row.IsSidechain {
			return transcriptIgnore
		}
		// content 为字符串：按前缀过滤本地命令回显，其余算真实用户消息。
		var text string
		if err := json.Unmarshal(row.Message.Content, &text); err == nil {
			trimmed := strings.TrimSpace(text)
			for _, prefix := range transcriptLocalPrefixes {
				if strings.HasPrefix(trimmed, prefix) {
					return transcriptIgnore
				}
			}
			return transcriptUserEcho
		}
		// content 为数组：tool_result 是工具回填，不是用户消息。
		var blocks []struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(row.Message.Content, &blocks); err != nil {
			return transcriptIgnore
		}
		for _, b := range blocks {
			if b.Type == "tool_result" {
				return transcriptIgnore
			}
		}
		// 非 tool_result 的数组内容（罕见）按真实用户消息处理。
		return transcriptUserEcho
	case "assistant":
		if row.IsSidechain {
			return transcriptIgnore
		}
		// 无 uuid 的行无法去重，宁可不发也不重发。
		if strings.TrimSpace(row.UUID) == "" {
			return transcriptIgnore
		}
		return transcriptAssistant
	case "system":
		if row.IsSidechain {
			return transcriptIgnore
		}
		switch strings.TrimSpace(row.Subtype) {
		case "turn_duration", "stop_hook_summary":
			return transcriptTurnEnd
		}
		return transcriptIgnore
	default:
		return transcriptIgnore
	}
}

// transcriptTurnDurationMs 从 turn_duration 行提取回合真实耗时（毫秒）。
// 非 turn_duration 行返回 0。
func transcriptTurnDurationMs(line []byte) int64 {
	var row struct {
		Type       string `json:"type"`
		Subtype    string `json:"subtype"`
		DurationMs int64  `json:"durationMs"`
	}
	if json.Unmarshal(line, &row) != nil {
		return 0
	}
	if row.Type != "system" || row.Subtype != "turn_duration" {
		return 0
	}
	return row.DurationMs
}

// transcriptBlock 是一条 assistant 行解出的一个内容块。transcript 是块级整行
// 写入（spec §4.3 已核实：apiBlockIndex 递增、uuid 唯一不重写），故块是
// delta 的天然粒度——v1 不做 token 级流式。
type transcriptBlock struct {
	Kind      string // "thinking" | "text" | "tool_use"
	Text      string
	ToolName  string
	ToolInput json.RawMessage
}

// transcriptFileTools 是会改动工作区的工具名，与 spawn 路径的
// claudeMessageToolUseFilePaths（agent_ai.go）完全一致——files_touched_count
// 在两条执行路径上必须同口径，否则手机端统计会随路由漂移。
var transcriptFileTools = map[string]struct{}{
	"Write":     {},
	"Edit":      {},
	"MultiEdit": {},
}

func extractTranscriptBlocks(line []byte) []transcriptBlock {
	if classifyTranscriptLine(line) != transcriptAssistant {
		return nil
	}
	var row struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &row); err != nil {
		return nil
	}
	var raw []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		Thinking string          `json:"thinking"`
		Name     string          `json:"name"`
		Input    json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(row.Message.Content, &raw); err != nil {
		return nil
	}
	out := make([]transcriptBlock, 0, len(raw))
	for _, b := range raw {
		switch b.Type {
		case "thinking":
			out = append(out, transcriptBlock{Kind: "thinking", Text: b.Thinking})
		case "text":
			out = append(out, transcriptBlock{Kind: "text", Text: b.Text})
		case "tool_use":
			out = append(out, transcriptBlock{Kind: "tool_use", ToolName: b.Name, ToolInput: b.Input})
		}
	}
	return out
}

// feedTUITurnActivity 把 transcript 的新行喂给 activity，让看门狗看到真实进展。
//
// 不喂的后果（2026-10-20 事故）：activity.lastActivityAt 永远停在 run 起点，
// 10 分钟 idle 看门狗把合法的长回合当卡死杀掉；更糟的是当时收尾只发非终态的
// ai.status(stopping)，服务端只能等 10 分钟 stale sweeper 把卡片打翻成失败。
//
// 判据与 spawn 路径的流式回调同口径：
//   - 任何新行 → bump（证明 TUI 还在写）
//   - text / thinking 块 → markProgress（有产出）
//   - tool_use 块 → beginToolUseWait（工具执行期暂停 idle 判定）
//   - tool_result 块 → endToolUseWait（工具已回填）
//
// 注意：含 tool_result 的 user 行会被 classifyTranscriptLine 判成 ignore，
// 所以这里必须自己解 content 块，不能复用分类结果。
func feedTUITurnActivity(activity *agentAIActivity, line []byte) {
	if activity == nil || len(bytes.TrimSpace(line)) == 0 {
		return
	}
	activity.bump()
	var row struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &row); err != nil {
		return
	}
	// content 为字符串（普通用户消息）时解成数组会失败，bump 已足够。
	var blocks []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(row.Message.Content, &blocks); err != nil {
		return
	}
	for _, b := range blocks {
		switch b.Type {
		case "text", "thinking":
			activity.markProgress()
		case "tool_use":
			// 工具发起也算进展：模型已经想完并动了手。
			activity.markProgress()
			activity.beginToolUseWait()
		case "tool_result":
			activity.endToolUseWait()
		}
	}
}

// toolUseTouchedPaths 从 tool_use 块提取被改文件路径（保序去重），供 ai.done 的
// files_touched_count 使用。只认 transcriptFileTools 里的工具、只取 file_path
// 键——与 spawn 路径同口径。
func toolUseTouchedPaths(blocks []transcriptBlock) []string {
	seen := make(map[string]struct{}, len(blocks))
	out := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Kind != "tool_use" {
			continue
		}
		if _, ok := transcriptFileTools[b.ToolName]; !ok {
			continue
		}
		var in struct {
			FilePath string `json:"file_path"`
		}
		if err := json.Unmarshal(b.ToolInput, &in); err != nil {
			continue
		}
		fp := strings.TrimSpace(in.FilePath)
		if fp == "" {
			continue
		}
		if _, dup := seen[fp]; dup {
			continue
		}
		seen[fp] = struct{}{}
		out = append(out, fp)
	}
	return out
}

// tuiTurnPollInterval 是 tail 桥的轮询间隔。可经测试覆写。
var tuiTurnPollInterval = 200 * time.Millisecond

// tuiTurnQuietRounds 是判定「回合已安静结束」所需的连续无新 assistant 行的
// 轮询次数。CC 写完最后一个块到把 pid record 翻成 idle 之间有短暂窗口。
var tuiTurnQuietRounds = 3

// tuiTurnStartWindow 是注入后等待**本回合回显**的窗口：窗口内 transcript 没有
// 出现本回合注入消息的 user 回显（dequeue 的产物——入队与元数据行不算，事故
// 形态恰恰是它们在涨而回合永不开始），判为「接收了但没跑」，以 tui_no_turn
// 快速失败。不杀 TUI、不回收消息——消息仍在 TUI 队列里，若它后续起跑，
// tuiSync 会把产出同步进会话；这里只是让手机立刻拿到诚实的失败态，而不是等
// 10 分钟 idle 看门狗收尸（2026-10-30 1dc69343 事故）。
//
// 起跑信号刻意只认本回合自己的回显（ownEchoMatcher）：他人回合/前一条滞留
// 消息的回显与产出都不算——它们先于本回合消息出队时，旧的「任何 user 行都
// 算起跑」会把新回合的 tail 桥错误锚定在别人的输出上（2026-10-31 评审定谳）。
// 窗口期内心跳照常发送（见 runTUITurn 里 ticker 先行），任意窗口长度都不会
// 撞上服务端 10 分钟安静 sweeper。默认 5 分钟（覆盖网关抖动期实测数分钟的
// 静默重试），可经 ALIANG_TUI_TURN_START_WINDOW 覆写；"0" = 关闭守卫
// （resolveEnvDurationAllowZero 约定）。
var tuiTurnStartWindow = resolveEnvDurationAllowZero("ALIANG_TUI_TURN_START_WINDOW", 5*time.Minute)

// ownEchoMatcher 返回「本回合注入消息的回显」判定：transcript 的 user 行文本
// 包含本回合原始内容即视为本回合 echo（注入帧是 content 的超集——前面拼了
// surface 契约、后面拼了附件清单，CC 的 dequeued 回显写的是完整注入文本）。
// content 为空时退化为任意真实 user 行（保底不比旧行为更差）。
func ownEchoMatcher(content string) func(line []byte) bool {
	needle := strings.TrimSpace(content)
	return func(line []byte) bool {
		if classifyTranscriptLine(line) != transcriptUserEcho {
			return false
		}
		if needle == "" {
			return true
		}
		return strings.Contains(transcriptUserText(line), needle)
	}
}

// tuiTurnCeiling 是注入回合的全局上限。没有它，一个永不回显也永不结束的注入
// （TUI 卡死 / transcript 不写 / 判定条件永不满足）会让这个 goroutine 永久占着
// session.cancel，该会话此后再也无法发起回合。兜底必须有。
//
// 「卡死」的主判据是 idle 看门狗（transcript 10 分钟没有新行 → kill），所以这里
// 只兜「一直在写、永远不结束」的病态回合，窗口必须远大于合法长回合：2026-10-20
// 实测一个正常的 TUI 回合连续跑了 78 分钟，旧的 30 分钟上限会把它误杀成失败。
// 可经 ALIANG_TUI_TURN_CEILING（time.ParseDuration，如 "6h"）覆写。
var tuiTurnCeiling = resolveEnvDuration("ALIANG_TUI_TURN_CEILING", 4*time.Hour)

// runTUITurn 把一个手机回合注入目标 TUI 执行，并 tail transcript 合成 run
// 生命周期回传手机（spec §4.3）。ctx 取消 = ai.stop。
//
// content 是用户的原始文本——不是 run.prompt（那是 buildAgentAIPrompt 包装过
// 的，带会话前缀与全量历史）。附件从 run.attachments 取，注入前先下载到持久
// 缓存文件：注入通道是纯文本，图片只有落成本地文件、让 TUI 侧的 claude 用
// Read 读，才能成为视觉输入；缓存保留供同一会话后续回合再次 Read。
//
// 失败不回退 spawn：一次回合只走一条路（spec §4.5）。注入失败以 ai.error 收尾，
// 由服务端决定重试。
func (m *agentAIManager) runTUITurn(ctx context.Context, session *agentAISession, run agentAIRun, content string, plan *tuiTurnPlan, emitter *agentAIRunEmitter) {
	write := agentTerminalWriter(emitter.emit)
	fail := func(code, msg string) {
		logger.Info(fmt.Sprintf("ai.tui.turn: %s session=%s native=%s", code, session.id, plan.NativeSessionID))
		_ = write(agentAIErrorPayload(session.id, run.messageID, newAgentTUITurnError(code, msg)))
	}
	assistantMsgID := agentAssistantMessageID(run.messageID)
	// 子 agent 感知：TUI 回合的 sidechain 落在 <jsonl 同目录>/subagents/，
	// tail 轮询顺带 watch（见 tuiSubagentWatcher）。
	subWatch := newTUISubagentWatcher(plan.JSONLPath)

	// 1) 登记：与 spawn 路径同一组字段，使 stop()/审批通道/LRU 无需改动即可工作。
	runCtx, cancel := context.WithCancel(ctx)
	activity := newAgentAIActivity()
	// finalizeSubs 必须先于任何终态写——emitter 是终态屏障，之后的 interrupted
	// 会被丢弃。
	finalizeSubs := func() {
		for _, record := range run.subagents.finalize(time.Now()) {
			activity.endSubagent()
			_ = write(agentAISubagentPayload(session.id, record))
		}
	}
	// emitStopStatus 是 ctx 取消（用户 stop / 看门狗 kill / 硬上限）的唯一收尾。
	// 必须发**终态** ai.status：只发 stopping（非终态）会让服务端把 run 一直挂在
	// running 上，直到 10 分钟 stale sweeper 打翻成 timed_out/error——手机上一张
	// 已经停下的卡片被标成失败（2026-10-20 事故）。状态名与 spawn 路径同源
	// （agentAIRunStoppedStatus），看门狗 kill 的原因才能如实上报。
	emitStopStatus := func() {
		status, errMsg := agentAIRunStoppedStatus(activity, nil)
		finalizeSubs()
		payload := map[string]any{
			"type": models.AgentEventAIStatus, "session_id": session.id, "status": status,
		}
		if errMsg != "" {
			payload["error"] = errMsg
		}
		_ = write(payload)
	}

	m.mu.Lock()
	session.cancel = cancel
	session.activeWriter = write
	session.activity = activity
	session.tuiTurn = plan
	session.runSeq++
	session.activeRunID = run.runID
	run.runSeq = session.runSeq
	session.history = append(session.history, agentAIMessage{
		Role: "user", MessageID: run.messageID, Content: content, CreatedAt: time.Now().UTC(),
	})
	m.mu.Unlock()
	defer m.clearTUITurnSessionState(session)
	// 停止语义：ctx 取消时对 TUI pid 发一次 SIGINT（=一次 Esc，TUI 保持打开）。
	defer func() {
		if runCtx.Err() != nil {
			interruptExternalClaudeTurn(plan.Home, plan.NativeSessionID)
		}
	}()

	// 2) ai.run.started。executor=tui 让手机 UI 区分执行通道（该回合的审批在
	// 终端内完成、账号模型用 TUI 自己的）。
	_ = write(map[string]any{
		"type": models.AgentEventAIRunStarted, "session_id": session.id,
		"message_id": assistantMsgID, "provider": run.provider,
		"mode": run.mode, "project_path": run.projectPath,
		"state": "running", "executor": "tui",
	})

	// 2.2) 进度心跳先行：附件下载、注入、起跑观察（窗口可达数分钟）都是
	// 「run 活着但没有产出」的阶段——心跳让服务端 10 分钟安静 sweeper 在任意
	// 窗口配置下都不会把这一段误判成超时（2026-10-31 评审定谳）。TUI 长时间
	// 思考时手机也需要「仍活着」的信号（与 spawn 两条路径同间隔）。
	progressStop := make(chan struct{})
	defer close(progressStop)
	go func() {
		ticker := time.NewTicker(agentAIRunProgressInterval)
		defer ticker.Stop()
		for {
			select {
			case <-progressStop:
				return
			case <-ticker.C:
				_ = write(map[string]any{
					"type":              models.AgentEventAIRunProgress,
					"session_id":        session.id,
					"message_id":        assistantMsgID,
					"git_changed_count": countGitChanged(session.projectPath),
				})
			}
		}
	}()

	// 2.5) 附件落地。TUI 注入帧只有文本：图片必须先由 agent 下载成本地文件，
	// 再把路径交给终端里的 Claude Code 用 Read 打开。spawn 路径在
	// buildClaudeImageBlocks 里做同样的事；这里若只拼预签名 URL，模型不会
	// 主动去读，图片实际从未进入回合（2026-10-23 事故）。
	// 用持久缓存目录：同一会话后续回合可能再次 Read 同一张图，回合结束就删
	// 会让第二次 Read 失败；过期文件由下载器按 TTL 机会式清理。home 传 TUI
	// 用户 home，root 运行的 Core 才不会把文件写进 /var/root。
	attachments, cleanupAttachments, err := downloadAgentAIAttachmentsToCache(runCtx, run.attachments, run.attachmentAllowedHosts, plan.Home)
	if err != nil {
		if runCtx.Err() != nil {
			// 下载期间被 stop：按停止收尾，不算回合失败。
			emitStopStatus()
			return
		}
		fail(agentTUIErrAttachment, "附件下载失败："+err.Error())
		return
	}
	defer cleanupAttachments() // 缓存路径是 no-op，保留文件供后续回合 Read

	// 3) 注入。内容 = 原始用户文本 + 本地附件清单 + 明确的 Read 指令。
	// 走 ccPeerDialInject 旋钮（与 tuiSync 一致），测试才能拦截注入动作。
	// 手机行为契约只加在注入帧上（TUI 里的 CC 看不到宿主设置，这是唯一能
	// 影响它的通道）；上面 history 落的是原文，不回放契约。
	injectText := wrapAgentAIPhoneContractForSurface(run.surface, content) + agentAITUIAttachmentPromptSuffix(attachments)
	if err := ccPeerDialInject(plan.SocketPath, plan.PeerToken, injectText, plan.FromMode); err != nil {
		fail(agentTUIErrInjectFailed, "无法把消息送进终端会话")
		return
	}
	// held 检测：bypass 门会静默扣留且不弹任何提示，transcript 是唯一观测点。
	if ccPeerWatchHeld(plan.JSONLPath, plan.JSONLOffset, ccPeerHeldWatchWindow) {
		fail(agentTUIErrHeld, "终端会话拒绝了这条消息，请在终端中处理")
		return
	}
	if runCtx.Err() != nil {
		// 注入期间（含上面 held 观测窗口内）被 stop。停止信号由 defer 发，但
		// 终态 ai.status 必须在这里补——裸 return 会让手机只看到信号、
		// 看不到状态，UI 卡在「执行中」。
		emitStopStatus()
		return
	}
	// 起跑检测：接收≠起跑。窗口内没有出现本回合自己的回显（入队/元数据行/
	// 他人回合的回显都不算，见 tuiTurnStartWatch）判 tui_no_turn——手机立刻
	// 拿到诚实失败，而不是等 10 分钟 idle 看门狗（2026-10-30 事故形态）。
	// 失败不杀 TUI：消息可能仍在队列里，后续起跑由 tuiSync 补同步。
	outcome, echoEnd := tuiTurnStartWatch(runCtx, plan.JSONLPath, plan.JSONLOffset, tuiTurnStartWindow, ownEchoMatcher(content))
	if runCtx.Err() != nil {
		emitStopStatus()
		return
	}
	if outcome == tuiTurnNotStarted {
		fail(agentTUIErrNoTurn, "终端会话接收了消息但一直未开始执行；消息可能仍留在终端队列中，请到终端确认处理")
		return
	}
	// 本回合回显显式回传（tail 桥起点在本回 echo 之后，不会再读到它）。
	// Uncorrelated（回显认不出）时不发——旧语义里这行由 tail 桥的 user 行承担；
	// transcript 已消失（TranscriptGone）时跳过——tail 桥首轮立刻上报
	// tui_transcript_lost。
	if outcome == tuiTurnStarted {
		_ = write(map[string]any{
			"type": models.AgentEventAIDelta, "session_id": session.id,
			"message_id": assistantMsgID,
			"channel":    "user", "delta": content,
		})
	}

	// 4) tail 桥 + 完成判定。起点 = 本回合 echo 之后（watch 返回的锚点）：
	// 先于本回合出队的滞留消息与他人回合的产出被天然跳过，不会被错误归入
	// 本回合（2026-10-31 评审定谳）。
	m.startAIWatchdog(runCtx, activity, cancel)

	emitted := make(map[string]struct{}) // 已发过的行 uuid，防重
	touched := make(map[string]struct{})
	quiet := 0
	// 本回合回显已在起跑观察阶段确认，tail 从其后开始（TranscriptGone 时让位
	// tail 桥首轮上报，二者都到不了完成判定）。Uncorrelated 也算看到过活动
	// （旧语义的 quiet 累计前提），否则零新行时完成判定永不满足。
	sawUserEcho := outcome == tuiTurnStarted || outcome == tuiTurnStartedUncorrelated
	// 回合结束的权威信号与辅助信号（见下方完成判定的分层注释）。
	sawTurnEnd := false        // CC 亲写的 turn_duration/stop_hook_summary 行
	sawBusyTransition := false // pid 记录出现过 busy——回显分类失配时证明回合真实跑过
	turnDurationMs := int64(0)
	offset := echoEnd
	ceiling := time.After(tuiTurnCeiling)
tail:
	for {
		select {
		case <-runCtx.Done():
			// 用户 stop 或看门狗 kill：发终态（stopped / idle_timeout / ...），
			// 服务端据此立刻关闭 run，而不是等 10 分钟 stale sweeper 猜。
			emitStopStatus()
			return
		case <-ceiling:
			// 兜底：注入回合超上限。按 error 收尾（不是 done）——手机必须知道
			// 这一回合没有正常完成，而不是拿到一个假的成功。
			fail(agentTUIErrUnavailable, "终端会话执行超时")
			return
		default:
		}
		lines, newOffset, err := readTranscriptFrom(plan.JSONLPath, offset)
		if err != nil {
			fail(agentTUIErrTranscriptLost, "终端会话记录消失")
			return
		}
		offset = newOffset
		grew := false
		for _, line := range lines {
			// 先喂活跃度，再分类：不喂的话看门狗只看到 run 起点，10 分钟就误杀
			// 合法长回合（2026-10-20 事故）。
			feedTUITurnActivity(activity, line)
			switch classifyTranscriptLine(line) {
			case transcriptUserEcho:
				// 回显注入的消息（含 TUI 侧真实用户输入），手机确认「已进终端」。
				// channel 用 "user" 区分于 spawn 路径的 "assistant"。
				sawUserEcho = true
				if text := transcriptUserText(line); text != "" {
					_ = write(map[string]any{
						"type": models.AgentEventAIDelta, "session_id": session.id,
						"message_id": assistantMsgID,
						"channel":    "user", "delta": text,
					})
				}
				quiet = 0
			case transcriptAssistant:
				uuid := transcriptLineUUID(line)
				if _, dup := emitted[uuid]; dup {
					continue
				}
				emitted[uuid] = struct{}{}
				grew = true
				itemID := "tb" + transcriptBlockIndex(line) // ai.command/ai.thinking 的 item_id
				for _, b := range extractTranscriptBlocks(line) {
					switch b.Kind {
					case "text":
						_ = write(map[string]any{
							"type": models.AgentEventAIDelta, "session_id": session.id,
							"message_id": assistantMsgID,
							"channel":    "assistant", "delta": b.Text,
						})
					case "thinking":
						// claude 路径的 thinking 走独立事件，不进正文通道。
						_ = write(map[string]any{
							"type": models.AgentEventAIThinking, "session_id": session.id,
							"message_id": assistantMsgID, "item_id": itemID, "delta": b.Text,
						})
					case "tool_use":
						for _, fp := range toolUseTouchedPaths([]transcriptBlock{b}) {
							touched[fp] = struct{}{}
						}
						// ai.command 必填 item_id + status，缺了服务端判畸形。
						payload := map[string]any{
							"type": models.AgentEventAICommand, "session_id": session.id,
							"message_id": assistantMsgID, "item_id": itemID,
							"status": "started", "command": b.ToolName,
						}
						if cwd := toolUseCwd(b.ToolInput); cwd != "" {
							payload["cwd"] = cwd
						}
						_ = write(payload)
					}
				}
			case transcriptTurnEnd:
				// CC 亲写的回合结束标记。turn_duration 的 durationMs 是回合
				// 真实耗时，透传给手机展示；stop_hook_summary 与其成对出现，
				// 任一见到都算回合结束（老版本 CC 两者皆无 → 落到下层信号）。
				sawTurnEnd = true
				if ms := transcriptTurnDurationMs(line); ms > 0 {
					turnDurationMs = ms
				}
			}
		}
		if grew {
			quiet = 0
		} else if sawUserEcho || sawBusyTransition {
			// quiet 只在「回合确实动过」后才有意义累加：注入从未被 TUI 处理时
			// transcript 全静默、记录仍是 idle——此时累加 quiet 会把「没跑」
			// 误判成「跑完了」。
			quiet++
		}
		// sidechain 目录 watch：新子 agent→started、增长→progress、静默→completed
		subWatch.poll(run.subagents, activity, session.id, assistantMsgID, write)
		// 回合进行中轮询 pid 记录，捕捉 busy→idle 转变（②的信号源）。busy 一旦
		// 见到就不再轮询，长回合不会为此持续读文件。
		if !sawTurnEnd && !sawBusyTransition {
			if rec, live := liveClaudeTUIRecord(plan.Home, plan.NativeSessionID); live &&
				strings.EqualFold(strings.TrimSpace(rec.Status), "busy") {
				sawBusyTransition = true
			}
		}
		// 完成判定，按权威度分层（任一满足即完成）：
		// ① turn end 标记：CC 亲自宣告回合结束——最权威，见到即完成，不等
		//    安静轮也不再看 pid 记录（记录可能因 CC 版本/崩溃滞留 busy）。
		// ② busy→idle 转变 + 安静轮：回显分类失配（CC 版本差异改了回显形态）
		//    时，记录先 busy 后 idle 仍证明回合真实跑完。
		// ③ 回显 + 安静轮 + 记录 idle/消失：老 CC 无结束标记时的原有路径。
		// 极小竞态（用户恰在注入瞬间自己在 TUI 发言、其回合先结束）会提前完成
		// 一次；该场景 tuiSync 会把后续内容补进会话，可接受。
		if sawTurnEnd {
			break tail
		}
		if quiet >= tuiTurnQuietRounds {
			rec, live := liveClaudeTUIRecord(plan.Home, plan.NativeSessionID)
			if !live {
				if sawUserEcho {
					break tail
				}
			} else if strings.EqualFold(strings.TrimSpace(rec.Status), "idle") {
				if sawUserEcho || sawBusyTransition {
					break tail
				}
			}
		}
		select {
		case <-runCtx.Done():
			emitStopStatus()
			return
		case <-time.After(tuiTurnPollInterval):
		}
	}

	// 5) ai.done（经 emitter → 自动 run_id/event_seq + 终态落盘 + 断线补推）。
	// 先收敛仍存活的子 agent（后台 async 子 agent 在 done 时可能还在跑）。
	finalizeSubs()
	donePayload := map[string]any{
		"type": models.AgentEventAIDone, "session_id": session.id,
		"message_id":          assistantMsgID,
		"files_touched_count": len(touched),
		"git_changed_count":   countGitChanged(session.projectPath),
		"executor":            "tui",
	}
	if turnDurationMs > 0 {
		donePayload["turn_duration_ms"] = turnDurationMs
	}
	_ = write(donePayload)
}

// tuiTurnStartOutcome 是起跑观察的结论。transcript 消失必须是独立一态：
// 它是 tail 桥的 tui_transcript_lost 职责，绝不能在观察窗里被顶成 tui_no_turn
// （第一版实现的真 bug，TestRunTUITurnFailures/transcript_lost 锁死）。
// Started 与 StartedUncorrelated 都算「已起跑」——差别只在回显归因：CC 版本
// 改过回显形态（2026-10-20 实录），完成/起跑绝不能依赖认得出回显。
type tuiTurnStartOutcome int

const (
	tuiTurnStarted             tuiTurnStartOutcome = iota // 本回合 echo 已出现（可归因锚定）
	tuiTurnStartedUncorrelated                            // 有对话活动但认不出本回合 echo → 按旧语义起跑
	tuiTurnNotStarted                                     // 窗口耗尽零对话活动 → tui_no_turn
	tuiTurnTranscriptGone                                 // transcript 读错误 → 让位 tail 桥上报
)

// tuiTurnStartWatch 阻塞观察注入后的「本回合起跑」信号，分层判定：
//
//   - 本回合注入消息的 user 回显（isOwnEcho）→ (tuiTurnStarted, 该 echo 行之后
//     的偏移)。调用方以它为 tail 桥起点，天然跳过先于本回合出队的滞留消息与他
//     人回合的产出（错锚防护，2026-10-31 评审定谳）。
//   - 其他任何对话活动（assistant/turn-end/认不出的 user 行）→ 立即
//     (tuiTurnStartedUncorrelated, 该行之后的偏移)。刻意不等窗口：回显形态
//     漂移时旧语义照样要能跑（TestRunTUITurnCompletesOnTurnEnd 锁死），窗口
//     绝不能拖慢健康回合。
//   - 窗口耗尽仍零活动 → (tuiTurnNotStarted, seen)——这才是事故形态：入队/
//     元数据行（ignore 类）不算活动，文件可以一直涨但回合永不开始。
//
// ctx 取消返回 NotStarted（调用方先查 runCtx 走停止收尾）；transcript 读错误
// 返回 (tuiTurnTranscriptGone, offset)。
func tuiTurnStartWatch(ctx context.Context, jsonlPath string, offset int64, window time.Duration, isOwnEcho func([]byte) bool) (tuiTurnStartOutcome, int64) {
	if isOwnEcho == nil {
		isOwnEcho = func([]byte) bool { return false }
	}
	deadline := time.Now().Add(window)
	seen := offset
	for {
		lines, newOffset, err := readTranscriptFrom(jsonlPath, seen)
		if err != nil {
			// transcript 消失是独立失败（tui_transcript_lost）：立即让位，交给
			// tail 桥首轮上报，绝不在这里干等窗口把错误顶成 tui_no_turn。
			return tuiTurnTranscriptGone, offset
		}
		for _, line := range lines {
			if isOwnEcho(line) {
				return tuiTurnStarted, seen + int64(len(line)+1)
			}
			if classifyTranscriptLine(line) != transcriptIgnore {
				// 他人回合/回显形态漂移：按旧语义立即起跑，不拖窗口。锚点回到
				// 原始 offset——tail 桥重读全部活动行（delta 一个不吞），与旧
				// 行为逐字节一致；只有认得出本回合 echo 时才跳到 echo 之后。
				return tuiTurnStartedUncorrelated, offset
			}
		}
		seen = newOffset
		if ctx.Err() != nil {
			return tuiTurnNotStarted, offset
		}
		if time.Now().After(deadline) {
			return tuiTurnNotStarted, seen
		}
		select {
		case <-ctx.Done():
			return tuiTurnNotStarted, offset
		case <-time.After(tuiTurnPollInterval):
		}
	}
}

// readTranscriptFrom 从 offset 读完整新行。只消费以 \n 结尾的部分——半行留在
// 文件里等下一轮，故不会解析到半个 JSON。
func readTranscriptFrom(path string, offset int64) ([][]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, offset, err
	}
	if info.Size() <= offset {
		return nil, offset, nil
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	buf := make([]byte, info.Size()-offset)
	if _, err := io.ReadFull(f, buf); err != nil && err != io.ErrUnexpectedEOF {
		return nil, offset, err
	}
	end := int64(len(buf))
	if idx := bytes.LastIndexByte(buf, '\n'); idx >= 0 {
		end = int64(idx) + 1 // 只消费完整行
	}
	if end <= 0 {
		return nil, offset, nil
	}
	lines := bytes.Split(bytes.TrimRight(buf[:end], "\n"), []byte("\n"))
	return lines, offset + end, nil
}

func transcriptLineUUID(line []byte) string {
	var row struct {
		UUID string `json:"uuid"`
	}
	_ = json.Unmarshal(line, &row)
	return strings.TrimSpace(row.UUID)
}

// transcriptBlockIndex 取行的 apiBlockIndex（transcript 块级写入的序号），用作
// ai.command / ai.thinking 的 item_id。缺失时退化为 "b"。
func transcriptBlockIndex(line []byte) string {
	var row struct {
		APIBlockIndex *int `json:"apiBlockIndex"`
	}
	if json.Unmarshal(line, &row) == nil && row.APIBlockIndex != nil {
		return strconv.Itoa(*row.APIBlockIndex)
	}
	return "b"
}

// toolUseCwd 从 tool_use 的 input 里取 cwd（Bash 类工具有），供 ai.command 的
// 可选 cwd 字段使用。
func toolUseCwd(input json.RawMessage) string {
	var in struct {
		Cwd string `json:"cwd"`
	}
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	return strings.TrimSpace(in.Cwd)
}

func transcriptUserText(line []byte) string {
	var row struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &row) != nil {
		return ""
	}
	var text string
	if json.Unmarshal(row.Message.Content, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(row.Message.Content, &blocks) != nil {
		return ""
	}
	for _, b := range blocks {
		if b.Type == "text" {
			return b.Text
		}
	}
	return ""
}

// agentAITUIAttachmentPromptSuffix 在通用附件清单后补一条 Read 指令。
// TUI 注入只能传文本，图片必须由 agent 先下载成本地文件；不明确要求 Read，
// 模型只会看到一串路径，不会主动打开图片（2026-10-23 事故）。
func agentAITUIAttachmentPromptSuffix(attachments []agentAIAttachment) string {
	suffix := agentAIAttachmentPromptSuffix(attachments, true)
	if suffix == "" {
		return ""
	}
	// 只有落成本地文件的附件才需要 Read；纯内联文本没有路径可读。
	hasLocalFile := false
	for _, attachment := range attachments {
		if attachment.Path != "" {
			hasLocalFile = true
			break
		}
	}
	if !hasLocalFile {
		return suffix
	}
	return suffix + "\n\n请使用 Read 工具读取上面列出的本地附件文件（图片必须用 Read 打开），它们是本次用户消息的附件，不是远程链接。"
}

// clearTUITurnSessionState 清掉本回合在 session 上的占用标记，让后续回合可跑。
func (m *agentAIManager) clearTUITurnSessionState(session *agentAISession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session.cancel = nil
	session.activeWriter = nil
	session.activity = nil
	session.tuiTurn = nil
	session.activeRunID = ""
}

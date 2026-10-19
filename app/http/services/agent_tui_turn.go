package services

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
)

// TUI 执行回合的机器可读错误码。与 agentExternalTUIBusyError 同构：实现
// ErrorCode() interface，由 agentAIErrorPayload 经 errors.As 自动拾取，挂到
// ai.error 的 error_code 字段上（agent_ai.go:7613-7617）——服务端与手机据此
// 分支，无需对错误字符串做脆弱匹配。
const (
	agentTUIErrUnavailable    = "tui_unavailable"     // 注入前判定中途失效（TUI 退出/记录消失）或回合超上限
	agentTUIErrInjectFailed   = "tui_inject_failed"   // 拨号或写入 UDS 失败
	agentTUIErrHeld           = "tui_held"            // 接收方（bypass 门）静默扣留注入消息
	agentTUIErrTranscriptLost = "tui_transcript_lost" // tail 期间 transcript 消失
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
func classifyTranscriptLine(line []byte) transcriptLineKind {
	if len(bytes.TrimSpace(line)) == 0 {
		return transcriptIgnore
	}
	var row struct {
		Type        string `json:"type"`
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
	default:
		return transcriptIgnore
	}
}

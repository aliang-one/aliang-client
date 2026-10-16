package models

import "encoding/json"

// tool_ask bridge 契约(2026-10-16 方案定稿):agent 自有 MCP 工具
// (mcp__aliang__*)作为 one-shot --print 下的阻塞交互通道——CC 同步等待
// tool_result,agent 经三帧与 server 交换"手机作答"后原路回填 MCP 结果。
// 本文件是 server 侧 src/modules/ai/toolAsk.ts 契约的 Go 镜像;只承载
// wire 形状与白名单,不承载投递/状态机(server 侧 registry 负责)。
//
// 安全边界:白名单是精确工具名集合,不是前缀——full 档原生加载项目
// .mcp.json,恶意仓库可声明同名 server "aliang" 注入未知 mcp__aliang__*
// 工具;白名单之外的一律落回正常审批策略(handleClaudeApprovalHook 短路
// 仅对白名单内工具自动批)。

// AgentToolAskTools 精确白名单:新增交互工具必须显式追加(并同步 server 侧)。
var AgentToolAskTools = []string{
	"mcp__aliang__ask_user",
	"mcp__aliang__request_approval",
	"mcp__aliang__report_progress",
}

// IsAgentToolAskTool 精确匹配;未知 mcp__aliang__* 不得命中(命名空间碰撞防护)。
func IsAgentToolAskTool(name string) bool {
	for _, tool := range AgentToolAskTools {
		if name == tool {
			return true
		}
	}
	return false
}

const (
	AgentToolAskStatusAnswered = "answered"
	AgentToolAskStatusExpired  = "expired"

	AgentToolAskReasonTimeout   = "timeout"      // 手机应答 SLA 到期,agent 回 is_error tool_result
	AgentToolAskReasonRunEnded  = "run_ended"    // run 终结/agent 关停,batch 取消
	AgentToolAskReasonClaudeErr = "claude_error" // CC 进程异常退出
)

// AgentToolAskRequest agent→server:tool_ask.request。interaction_id 由 agent
// bridge 生成(MCP tools/call 拿不到 CC 的 tool_use_id),是全链幂等键;
// expires_at 为 agent 计算的作答 SLA(ms epoch);device_id 仅信息性(server
// 从 WS 连接即知设备),可为空。
type AgentToolAskRequest struct {
	Type          string          `json:"type"`
	RunID         string          `json:"run_id"`
	EventSeq      int64           `json:"event_seq"`
	InteractionID string          `json:"interaction_id"`
	SessionID     string          `json:"session_id"`
	DeviceID      string          `json:"device_id"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
	ExpiresAt     int64           `json:"expires_at"`
}

// AgentToolAskAnswer 手机作答载荷;与 server 审批卡 options/message 契约同构。
type AgentToolAskAnswer struct {
	SelectedOptionID string `json:"selected_option_id,omitempty"`
	Message          string `json:"message,omitempty"`
}

// AgentToolAskResponse server→agent:tool_ask.response。answered 必带 Answer,
// expired 不得携带(双向 schema 均拒绝);agent 收到后按 interaction_id 幂等
// 回填——已决/未知 interaction 的迟到帧只回 ack,绝不产生第二个 tool_result。
type AgentToolAskResponse struct {
	Type          string              `json:"type"`
	InteractionID string              `json:"interaction_id"`
	DeliveryID    string              `json:"delivery_id"`
	Attempt       int                 `json:"attempt"`
	Status        string              `json:"status"`
	Answer        *AgentToolAskAnswer `json:"answer,omitempty"`
}

// AgentToolAskCancel agent→server:tool_ask.cancel,主动翻卡终态。
type AgentToolAskCancel struct {
	Type          string `json:"type"`
	InteractionID string `json:"interaction_id"`
	Reason        string `json:"reason"`
}

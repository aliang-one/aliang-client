package services

// D1(spec 2026-11-02):aliang MCP bridge(`/mcp/aliang/<token>`,CLI provider
// stream-json 回合经 --mcp-config 注入的既有面)补注册 peer 五工具,使 claude
// 族 CLI 回合获得与 agentcore 内核/外部 stdio 面同一套设备互访能力。
//
// 命名映射(防漂移断言见 agent_ai_tool_ask_peer_test.go 与 cmd/mcp_test.go):
//   agentcore 点号名(agent_peer_tools.go:46-50) ↔ 本桥(全前缀下划线) ↔ stdio 面(cmd/mcp.go,后三个无 peer_ 前缀=历史现状)
//   peer.exec            ↔ peer_exec        ↔ peer_exec
//   peer.fetch_file      ↔ peer_fetch_file  ↔ peer_fetch_file
//   peer.device_info     ↔ peer_device_info ↔ device_info
//   peer.device_list     ↔ peer_device_list ↔ device_list
//   peer.device_note     ↔ peer_device_note ↔ device_note
//
// handler 复用注册表路径同一批 payload 函数(agent_peer_tools.go:828-916),
// 门禁不变:目标设备 peer_exec_enabled(默认关)+ 云端 peer ACL + 全量审计;
// 出网凭据留在 agent 进程,CLI/LLM 接触不到。readOnly/goal-fork 回合本就不
// 注入 bridge(agent_ai.go 注入点门控),与内核「readOnly 剥离 peer 工具」
// 口径一致。

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
)

type agentPeerMCPToolDef struct {
	name        string
	description string
	inputSchema json.RawMessage
	handle      func(ctx context.Context, args map[string]interface{}) (string, bool)
}

// peerMCPMsg payload 函数吃 msg map;注入合成 request_id(mcp_ 前缀,不与
// WS 注册表路径冲突),其余键原样透传。
func peerMCPMsg(args map[string]interface{}) map[string]interface{} {
	msg := make(map[string]interface{}, len(args)+1)
	for k, v := range args {
		msg[k] = v
	}
	msg["request_id"] = "mcp_" + uuid.NewString()
	return msg
}

// peerMCPText payload → (text, isErr)。
func peerMCPText(payload map[string]interface{}) (string, bool) {
	if errText, ok := payload["error"].(string); ok && errText != "" {
		return errText, true
	}
	text, _ := payload["result"].(string)
	return text, false
}

func agentPeerMCPBridgeTools() []agentPeerMCPToolDef {
	return []agentPeerMCPToolDef{
		{
			name: "peer_exec",
			description: "Run a one-shot command on another device owned by the same account and " +
				"return its exit code plus stdout/stderr. The remote device must have peer exec enabled. " +
				"Serial per target device: a busy target returns fail_reason=busy immediately. Also " +
				"serial per calling device: only one peer command may be in flight at a time, so run " +
				"peer calls one after another, never in parallel (concurrent calls fail with " +
				"fail_reason=rate_limited). On Windows targets the command runs under Windows " +
				"PowerShell 5.1: \"&&\" is unavailable (use \";\" or PS cmdlets), external .exe may " +
				"be unavailable (prefer PS built-ins like $env:COMPUTERNAME or Get-FileHash, or " +
				"absolute paths), and PS output is UTF-8 (native-exe output in a legacy codepage " +
				"may still be garbled).",
			inputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"device_id":{"type":"string","description":"Target device id"},
					"command":{"type":"string","description":"Shell command to run on the target device"},
					"timeout_ms":{"type":"integer","description":"Command timeout in ms (1000-600000, default 30000; out-of-range values are clamped)"},
					"workdir":{"type":"string","description":"Working directory on the target device (must exist, default home)"}
				},
				"required":["device_id","command"]
			}`),
			handle: func(_ context.Context, args map[string]interface{}) (string, bool) {
				return peerMCPText(agentPeerExecPayload(peerMCPMsg(args)))
			},
		},
		{
			name: "peer_fetch_file",
			description: "Queue an asynchronous file fetch from another device into this machine's " +
				"transfer inbox. The source device must have peer fetch enabled. Returns a transfer_id; " +
				"completion is delivered via transfer.progress.",
			inputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"device_id":{"type":"string","description":"Source device id"},
					"remote_path":{"type":"string","description":"File path on the source device"}
				},
				"required":["device_id","remote_path"]
			}`),
			handle: func(_ context.Context, args map[string]interface{}) (string, bool) {
				return peerMCPText(agentPeerFetchFilePayload(peerMCPMsg(args)))
			},
		},
		{
			name: "peer_device_info",
			description: "Device profile snapshot: hostname, platform, agent version, load summary, " +
				"project count with recent project paths, authorized directory count. Omit device_id " +
				"for this machine; pass one for a remote device in the same account.",
			inputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"device_id":{"type":"string","description":"Target device id; omit for this machine"}
				}
			}`),
			handle: func(_ context.Context, args map[string]interface{}) (string, bool) {
				return peerMCPText(agentDeviceInfoPayload(peerMCPMsg(args)))
			},
		},
		{
			name: "peer_device_list",
			description: "List every device in this account with id, name, platform, online status " +
				"and whether peer exec is enabled. This is the directory to pick device_id from for " +
				"the other peer tools (peer.exec / peer.fetch_file / peer.device_info / peer.device_note).",
			inputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
			handle: func(_ context.Context, _ map[string]interface{}) (string, bool) {
				return peerMCPText(agentPeerDeviceListPayload(peerMCPMsg(nil)))
			},
		},
		{
			name: "peer_device_note",
			description: "Save a named note (key/value metadata) on a device. Omit device_id to note " +
				"this machine. Notes survive reconnects and are visible to every device in the account. " +
				"Key max 64 bytes, value max 4096 bytes.",
			inputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"key":{"type":"string","description":"Note key (max 64 bytes)"},
					"value":{"type":"string","description":"Note value (max 4096 bytes)"},
					"device_id":{"type":"string","description":"Target device id; omit for this machine"}
				},
				"required":["key","value"]
			}`),
			handle: func(_ context.Context, args map[string]interface{}) (string, bool) {
				return peerMCPText(agentDeviceNotePayload(peerMCPMsg(args)))
			},
		},
	}
}

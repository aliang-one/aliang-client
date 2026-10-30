package services

import (
	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/tools"
)

// agentToolRegistryRev bumps whenever the tool set changes so the server can
// detect drift via the heartbeat rev and re-pull tools.list.
// rev 2: read_file 描述符改说真话(移除无实现的 "may be refused" 声明,如实
// 标注 operator policy 的无围栏语义)——描述符内容变了,服务端须靠 rev 漂移
// 重拉 tools.list。
// rev 3: 新增 git_log(项目提交历史,默认臂护栏分发——event git.log 不与
// 任何显式臂冲突,是首个完整走 RunWithGuards 的非 legacy 工具)。
// rev 4: 设备互访阶段 1（spec 2026-10-29 §7）新增四件意图工具 peer_exec/
// peer_fetch_file/device_info/device_note——事件 peer.exec/peer.fetch_file/
// peer.device_info/peer.device_note 同样不与任何显式臂冲突,完整走 default
// 臂护栏分发；服务端靠心跳 rev 漂移自动重拉 tools.list,零服务端点亮代码。
// 写工具免逐次审批的取舍记录在 agent_peer_tools.go 文件头。
const agentToolRegistryRev = 4

// buildAgentToolRegistry is THE single source of truth for remotely-invokable
// read-only tools. Legacy four first (events unchanged = old-server compat);
// future tools get appended here and light up server-side with zero server
// code change. Handler wiring reuses the existing payload funcs verbatim, so
// legacy response shapes are untouched.
// 注意：本表四个工具的 Event 与 agent_remote_ws.go 的显式 switch 臂重合，当前由旧臂直派（护栏不生效）；收编见计划 Phase 2 遗留债务。
func buildAgentToolRegistry() *tools.Registry {
	return tools.NewRegistry(agentToolRegistryRev,
		&tools.Tool{
			ID:    "list_dir",
			Event: models.AgentEventFileList,
			Description: "List entries in a directory (relative to the project path) " +
				"with per-entry git status. Use before reading files.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string", "description": "Directory path relative to project root"},
				},
				"required": []string{"path"},
			},
			Caps:    tools.Caps{ReadOnly: true, NeedsProject: true, TimeoutMs: 10000, MaxOutputBytes: 131072},
			Handler: agentFileListPayload,
			OnError: func(requestID string, err error) map[string]interface{} {
				return agentFileErrorPayload(requestID, err)
			},
		},
		&tools.Tool{
			ID:    "read_file",
			Event: models.AgentEventFileRead,
			Description: "Read a text file (relative to the project path). Binary files " +
				"return base64. Paths are not confined: any file the agent OS user " +
				"can read may be returned (operator policy).",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string", "description": "File path relative to project root"},
				},
				"required": []string{"path"},
			},
			Caps:    tools.Caps{ReadOnly: true, NeedsProject: true, TimeoutMs: 10000, MaxOutputBytes: 262144},
			Handler: agentFileReadPayload,
			OnError: func(requestID string, err error) map[string]interface{} {
				return agentFileErrorPayload(requestID, err)
			},
		},
		&tools.Tool{
			ID:          "git_status",
			Event:       models.AgentEventGitStatus,
			Description: "Is the working directory a git repo? Returns branch + short status.",
			Parameters: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
			Caps:    tools.Caps{ReadOnly: true, NeedsProject: true, TimeoutMs: 10000, MaxOutputBytes: 32768},
			Handler: agentGitStatusPayload,
			OnError: func(requestID string, err error) map[string]interface{} {
				return agentEnvToolErrorPayload(models.AgentEventGitStatusError, requestID, err)
			},
		},
		&tools.Tool{
			ID:    "git_log",
			Event: models.AgentEventGitLog,
			Description: "Show recent commit history for the project's git repository " +
				"(newest first). Returns structured entries (hash, author, date, subject). Read-only.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"limit":  map[string]interface{}{"type": "integer", "description": "Max commits to return (1-100, default 20)"},
					"offset": map[string]interface{}{"type": "integer", "description": "Skip N newest commits for paging (default 0)"},
				},
			},
			Caps:    tools.Caps{ReadOnly: true, NeedsProject: true, TimeoutMs: 10000, MaxOutputBytes: 32768},
			Handler: agentGitLogPayload,
			OnError: func(requestID string, err error) map[string]interface{} {
				return agentEnvToolErrorPayload(models.AgentEventGitLogError, requestID, err)
			},
		},
		&tools.Tool{
			ID:          "env_info",
			Event:       models.AgentEventEnvInfo,
			Description: "Environment snapshot: OS, arch, shell, user, node/git/python3 versions.",
			Parameters: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
			Caps:    tools.Caps{ReadOnly: true, NeedsProject: true, TimeoutMs: 10000, MaxOutputBytes: 32768},
			Handler: agentEnvInfoPayload,
			OnError: func(requestID string, err error) map[string]interface{} {
				return agentEnvToolErrorPayload(models.AgentEventEnvInfoError, requestID, err)
			},
		},
		// 设备互访阶段 1（spec §7 第一批四件）。设备级工具不调 resolveEnvToolCwd
		//（无项目 cwd 语义）；错误统一过 sanitizePeerError 脱敏。
		&tools.Tool{
			ID:    "peer_exec",
			Event: AgentPeerToolEventExec,
			Description: "Run a one-shot command on another device owned by the same account and " +
				"return its exit code plus stdout/stderr. Requires peer exec enabled on the target. " +
				"Targets run commands serially; a busy target fails fast with fail_reason=busy.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"device_id":  map[string]interface{}{"type": "string", "description": "Target device id"},
					"command":    map[string]interface{}{"type": "string", "description": "Shell command to run on the target device"},
					"timeout_ms": map[string]interface{}{"type": "integer", "description": "Command timeout in ms (1000-600000, default 30000)"},
					"workdir":    map[string]interface{}{"type": "string", "description": "Working directory on the target device (must exist, default home)"},
				},
				"required": []string{"device_id", "command"},
			},
			// TimeoutMs 640s 是第三道防线（服务端 fence 600s → 客户端 ctx 630s
			// → 护栏 640s，见 agent_peer_tools.go agentPeerToolTimeoutMs 注释）。
			// MaxOutputBytes 4MiB：契约合法结果最坏形态（stdout/stderr 各 256KiB
			// 裸字节经 JSON 转义可膨胀 1.5-3 倍）必须放得下——护栏超限是整体
			// 替换，卡在 768KiB 会把合法的满输出结果整条吞掉。
			Caps:    tools.Caps{ReadOnly: false, TimeoutMs: 640000, MaxOutputBytes: 4 << 20},
			Handler: agentPeerExecPayload,
		},
		&tools.Tool{
			ID:    "peer_fetch_file",
			Event: AgentPeerToolEventFetchFile,
			Description: "Queue an asynchronous file fetch from another device in the same account " +
				"into this machine's transfer inbox. Returns a transfer_id; completion arrives via " +
				"transfer.progress events.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"device_id":   map[string]interface{}{"type": "string", "description": "Source device id"},
					"remote_path": map[string]interface{}{"type": "string", "description": "File path on the source device"},
				},
				"required": []string{"device_id", "remote_path"},
			},
			Caps:    tools.Caps{ReadOnly: false, TimeoutMs: 65000, MaxOutputBytes: 32768},
			Handler: agentPeerFetchFilePayload,
		},
		&tools.Tool{
			ID:    "device_info",
			Event: AgentPeerToolEventDeviceInfo,
			Description: "Device profile snapshot: hostname, platform, agent version, load summary, " +
				"project count with the most recent project paths, authorized directory count. Omit " +
				"device_id for this machine; pass one for a remote device in the same account.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"device_id": map[string]interface{}{"type": "string", "description": "Target device id; omit for this machine"},
				},
			},
			Caps:    tools.Caps{ReadOnly: true, TimeoutMs: 15000, MaxOutputBytes: 16384},
			Handler: agentDeviceInfoPayload,
		},
		&tools.Tool{
			ID:    "device_note",
			Event: AgentPeerToolEventDeviceNote,
			Description: "Save a named key/value note on a device (this machine when device_id is " +
				"omitted). Notes survive reconnects and are visible to every device in the account.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"key":       map[string]interface{}{"type": "string", "description": "Note key"},
					"value":     map[string]interface{}{"type": "string", "description": "Note value"},
					"device_id": map[string]interface{}{"type": "string", "description": "Target device id; omit for this machine"},
				},
				"required": []string{"key", "value"},
			},
			Caps:    tools.Caps{ReadOnly: false, TimeoutMs: 35000, MaxOutputBytes: 16384},
			Handler: agentDeviceNotePayload,
		},
	)
}

var agentToolsRegistry = buildAgentToolRegistry()

func agentToolRegistry() *tools.Registry { return agentToolsRegistry }

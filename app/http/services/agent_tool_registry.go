package services

import (
	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/tools"
)

// agentToolRegistryRev bumps whenever the tool set changes so the server can
// detect drift via the heartbeat rev and re-pull tools.list.
const agentToolRegistryRev = 1

// buildAgentToolRegistry is THE single source of truth for remotely-invokable
// read-only tools. Legacy four first (events unchanged = old-server compat);
// future tools get appended here and light up server-side with zero server
// code change. Handler wiring reuses the existing payload funcs verbatim, so
// legacy response shapes are untouched.
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
				"return base64. Sensitive or secret-bearing files may be refused.",
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
	)
}

var agentToolsRegistry = buildAgentToolRegistry()

func agentToolRegistry() *tools.Registry { return agentToolsRegistry }

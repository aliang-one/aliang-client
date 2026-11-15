// 返回该会话可用的斜杠命令面：'claude' | 'codex' | 'agentcore' | ''（空串 =
// 无命令面）。后端 agentSlashCommandsListPayload 对每个 provider 都返回基线
// （claude/codex/opencode 内置命令 + 自定义；agentcore 返回本机命令 + 内核
// skills/MCP 能力条目），因此三种内核都开面板。
// 未知 provider 归一成空串保持抑制（防御性：面板空着总比错桶好）。
// 不要据此放宽后端 provider 判定——后端只把字面量 'agentcore' 路由到内置
// 内核（agent_ai.go:3900），agent-core / agent_core 只是防御性宽松匹配。
export function slashProviderFor(provider) {
  const p = String(provider || 'claudecode').toLowerCase();
  if (p === 'agentcore' || p === 'agent-core' || p === 'agent_core') return 'agentcore';
  if (p === 'codex') return 'codex';
  if (p === 'opencode' || p === 'open-code' || p === 'open_code') return 'opencode';
  return 'claude';
}

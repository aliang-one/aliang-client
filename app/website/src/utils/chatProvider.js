// 返回该会话可用的斜杠命令面：'claude' | 'codex' | ''（空串 = 无命令面）。
//
// agentcore 归一成空串是**纯客户端抑制**：调用方见到空串就不发
// slash.commands.list、不打开补全面板。不要据此放宽后端 provider 判定——
// 后端只把字面量 'agentcore' 路由到内置内核（agent_ai.go:3900），
// agent-core / agent_core 只是防御性宽松匹配。
export function slashProviderFor(provider) {
  const p = String(provider || 'claudecode').toLowerCase();
  if (p === 'agentcore' || p === 'agent-core' || p === 'agent_core') return '';
  return p === 'codex' ? 'codex' : 'claude';
}

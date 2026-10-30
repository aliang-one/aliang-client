// 「MCP 与技能」页的展示映射（纯函数，供组件与单测共用）。
//
// 内核注入态 → 徽章文案键 + 配色：三态语义见后端
// app/http/services/agent_integrations.go 的常量定义。

/** 内核态徽章：i18n 键 + Tailwind 类。未知态按「未采集」渲染（fail-safe）。 */
export function kernelStateBadge(state) {
  switch (state) {
    case 'injected':
      return {
        key: 'agent_integrations_kernelInjected',
        className: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300',
      };
    case 'skipped_headers':
      return {
        key: 'agent_integrations_kernelSkippedHeaders',
        className: 'bg-amber-100 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300',
      };
    default:
      return {
        key: 'agent_integrations_kernelNotCollected',
        className: 'bg-slate-100 text-slate-500 dark:bg-slate-700/40 dark:text-slate-400',
      };
  }
}

/** MCP 条目的副标题：stdio 显示命令行，http/sse 显示 URL；都没有则空串。 */
export function mcpEndpointLabel(server) {
  if (!server || typeof server !== 'object') return '';
  const command = typeof server.command === 'string' ? server.command.trim() : '';
  if (command) return command;
  const url = typeof server.url === 'string' ? server.url.trim() : '';
  return url;
}

/** 来源徽章：把后端逗号串（user,plugin / config）拆成可渲染的短标签。 */
export function sourceLabels(source) {
  const raw = typeof source === 'string' ? source : '';
  return raw
    .split(',')
    .map((part) => part.trim())
    .filter(Boolean)
    .map((part) => (part === 'config' ? 'codex' : part));
}

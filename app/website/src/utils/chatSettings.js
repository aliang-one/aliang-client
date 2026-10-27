// 会话设置条（工作目录/模型/effort）的纯逻辑：选项常量、最近项目整理、
// create 载荷组装、目录锁定判定。UI 装配在 ChatPage.vue；本文件保持零依赖
// 纯函数，vitest 直接覆盖（chatSettings.test.js）。

// 自定义模型的 select 哨兵值。真实模型名直接用自身作 value。
export const MODEL_CUSTOM_VALUE = '__custom__';

// 模型清单：前端常量维护（后端无清单接口）。glm-only 是回环网关的环境约束，
// 代码层不强制——自定义值原样透传，合法性与网关裁决。default('') 表示跟随
// 本机配置（agentCoreModel：ANTHROPIC_MODEL/ANTHROPIC_DEFAULT_SONNET_MODEL
// env 兜底，最终回落 glm-5.3）。
export const AGENTCORE_MODEL_OPTIONS = [
  { value: '', label: 'default' },
  { value: 'glm-5.3', label: 'glm-5.3' },
  { value: 'glm-5.3[1m]', label: 'glm-5.3[1m]' },
  { value: 'glm-5.3-flash', label: 'glm-5.3-flash' },
  { value: 'glm-5.3-flash[1m]', label: 'glm-5.3-flash[1m]' },
  { value: 'glm-5.2', label: 'glm-5.2' },
  { value: 'glm-5.1', label: 'glm-5.1' },
  { value: MODEL_CUSTOM_VALUE, label: 'custom' },
];

// effort 档位与内核 NormalizeEffortLevel（app/agentcore/loop.go）白名单同源：
// 超集值会被内核静默丢弃，清单必须收敛。'' = 不发送。
export const EFFORT_OPTIONS = [
  { value: '', label: 'default' },
  { value: 'low', label: 'low' },
  { value: 'medium', label: 'medium' },
  { value: 'high', label: 'high' },
  { value: 'xhigh', label: 'xhigh' },
  { value: 'max', label: 'max' },
];

// 最近项目：取 sessions[].project_path 非空值，按输入顺序（调用方保证
// updatedAt 倒序）去重，截断 limit。任何形态的脏输入都安全返回数组。
export function recentProjectsFromSessions(sessions, { limit = 8 } = {}) {
  if (!Array.isArray(sessions)) return [];
  if (limit <= 0) return [];
  const seen = new Set();
  const out = [];
  for (const session of sessions) {
    const raw = session && typeof session === 'object' ? session.project_path : null;
    const path = typeof raw === 'string' ? raw.trim() : '';
    if (!path || seen.has(path)) continue;
    seen.add(path);
    out.push(path);
    if (out.length >= limit) break;
  }
  return out;
}

// 目录锁定：以服务端确认为准——ai.session.created 回显到达时由 ChatPage 置
// conv.dirLocked = true。create 被拒的会话（目录不存在等）保持可改，用户纠正
// 路径后下一条消息的 create 即落正确目录。legacy 会话（无该字段）不锁定。
export function isDirectoryLocked(conv) {
  return !!(conv && typeof conv === 'object' && conv.dirLocked);
}

// ai.session.create 载荷统一出口：ChatPage 首发与重发共用，保证字段一致。
export function sessionCreatePayload(conv) {
  return {
    type: 'ai.session.create',
    session_id: (conv && conv.aiSessionId) || '',
    project_path: (conv && conv.projectPath) || '',
    provider: (conv && conv.provider) || 'claudecode',
    mode: 'vibe',
    model: (conv && conv.model) || '',
    effort: (conv && conv.effort) || '',
  };
}

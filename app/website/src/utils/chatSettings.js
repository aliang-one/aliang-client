// 会话设置条（工作目录/模型/effort）的纯逻辑：选项常量、最近项目整理、
// create 载荷组装、目录锁定判定、来源角标与设备会话清单映射（阶段 2 跨端
// 统一）。UI 装配在 ChatPage.vue；本文件保持零依赖纯函数，vitest 直接覆盖
// （chatSettings.test.js）。

// 自定义模型的 select 哨兵值。真实模型名直接用自身作 value。
export const MODEL_CUSTOM_VALUE = '__custom__';

// 模型清单：前端常量维护（后端无清单接口，也不打算加——用户可能接第三方
// LLM，白名单会挡人）。这些只是「建议值」，自定义值原样透传，合法性由上游
// 裁决。
//
// ⚠️ 空串不再表示「跟随本机配置」：内核已删除模型兜底链
// （app/http/services/agent_ai_agentcore.go 的 agentCoreModel 只剩手机下发值），
// 空模型会被直接拒绝开跑并报「未指定模型」。空串在此仅表示「用户还没填」，
// 由 ChatPage 在发送前拦截，并用上一次用过的模型（last-used）预填。
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

// 终端原生会话的 id 前缀约定（claude_<nativeId> / codex_<id>）。带前缀的
// 条目本端没有可直连的 journal，续聊必须走「新建托管会话 + resume」。全仓
// 唯一定义（阶段 2 评审收敛）：useDeviceSessions / ChatPage 均从这里引用。
export function isTerminalPrefixedSessionId(id) {
  return typeof id === 'string' && (id.startsWith('claude_') || id.startsWith('codex_'));
}

// agentcore 上下文喂入（阶段 2 Task 4）：裸 id 设备会话首条续聊的 create 要带
// journal 转录——否则 create 先注册空 history 会话，message() 的 journal 读回
// 被跳过，续聊静默变成新会话。后端 remoteAgentAIHistory（agent_ai.go）按
// row.role/row.content 解析 transcript 数组。判定收口：仅 agentcore 裸 id
// 设备壳（claude_/codex_ 前缀壳走 resume，由 CLI 自己装回原生历史，再喂转录
// 会重复）、转录非空、且尚未 ack 消费（deviceTranscriptSent 由 ChatPage 在
// created ack 后置位——resendCreate/确认门重发在 ack 前同样携带）。脏输入
// 安全返回 null。
export function deviceTranscriptForCreate(conv) {
  if (!conv || typeof conv !== 'object') return null;
  const entry = conv.deviceSession;
  if (!entry || typeof entry !== 'object') return null;
  if (conv.provider !== 'agentcore') return null;
  if (isTerminalPrefixedSessionId(entry.id) || !entry.id) return null;
  if (conv.deviceTranscriptSent) return null;
  const rows = Array.isArray(conv.deviceTranscript) ? conv.deviceTranscript : [];
  if (!rows.length) return null;
  return rows;
}

// ai.session.create 载荷统一出口：ChatPage 首发与重发共用，保证字段一致。
// 续聊扩展（阶段 2）：resumeSessionId → resume_session_id、resumeProjectPath
// → 覆盖 project_path、deviceTranscriptForCreate → transcript。可选字段——
// 未设置时键不得出现在载荷（e2e 断言 create 帧字段集，多出的空键会被判为
// 协议漂移）。
export function sessionCreatePayload(conv) {
  const payload = {
    type: 'ai.session.create',
    session_id: (conv && conv.aiSessionId) || '',
    project_path: (conv && conv.projectPath) || '',
    provider: (conv && conv.provider) || 'claudecode',
    mode: 'vibe',
    model: (conv && conv.model) || '',
    effort: (conv && conv.effort) || '',
  };
  if (conv && conv.resumeSessionId) {
    payload.resume_session_id = String(conv.resumeSessionId);
  }
  if (conv && conv.resumeProjectPath) {
    payload.project_path = String(conv.resumeProjectPath);
  }
  // 输出预算（可选）：只有用户显式设了正数才下发，否则整个键不出现——
  // 后端把「没这个键」当作「未设置」（内核用 64000 默认值），与显式 0 区分。
  const maxTokens = Number(conv && conv.maxTokens);
  if (Number.isFinite(maxTokens) && maxTokens > 0) {
    payload.max_tokens = Math.trunc(maxTokens);
  }
  const contextWindow = Number(conv && conv.contextWindow);
  if (Number.isFinite(contextWindow) && contextWindow > 0) {
    payload.context_window = Math.trunc(contextWindow);
  }
  // 会话级 MCP 开关：数组存在即整体下发（空数组 = 显式全部启用）。
  // 缺键 = 不动既有设置，两者语义不同，不能合并。
  if (conv && Array.isArray(conv.disabledMcpServers)) {
    payload.disabled_mcp_servers = conv.disabledMcpServers
      .map((n) => (typeof n === 'string' ? n.trim() : ''))
      .filter(Boolean);
  }
  const transcript = deviceTranscriptForCreate(conv);
  if (transcript) {
    payload.transcript = transcript;
  }
  return payload;
}

// 会话来源角标：跨端统一清单里标注该条目由哪一端/哪个入口发起。优先级
// running > origin > id 前缀；都不命中返回 ''（UI 不渲染角标）。脏输入安全
// 返回 ''。terminal 判定沿用终端会话 ID 的 claude_/codex_ 前缀约定。
export function sessionSourceBadge(session) {
  if (!session || typeof session !== 'object') return '';
  if (session.status === 'running') return 'active';
  if (session.origin === 'phone') return 'phone';
  if (session.origin === 'dashboard') return 'dashboard';
  const id = String(session.id ?? '');
  if (id.startsWith('claude_') || id.startsWith('codex_')) return 'terminal';
  return '';
}

// 设备会话 provider 归一：服务端清单对同一内核可能下发 claude/claudecode
// 两种拼写（历史通道差异），统一到前端会话的 provider 词表；未知/空回落
// claudecode（与 sessionCreatePayload 的缺省一致）。
// agent-core/agent_core 是 AliangCode 内核的别名拼写（与 chatProvider.js
// 的判定同源）——漏认会被归成 claudecode，让 AliangCode 会话被误过滤掉。
export function normalizeDeviceProvider(provider) {
  const p = typeof provider === 'string' ? provider.trim().toLowerCase() : '';
  if (p === 'claude' || p === 'claudecode') return 'claudecode';
  if (p === 'codex') return 'codex';
  if (p === 'agentcore' || p === 'agent-core' || p === 'agent_core') return 'agentcore';
  return 'claudecode';
}

// isAliangCodeSession 是「这个清单条目属不属于 AliangCode」的唯一判据
// （2026-10-30 用户拍板：仪表盘只展示 AliangCode 对话）。聊天窗侧栏与设置页
// 「AI 会话活动」共用本函数，避免两处词表漂移。脏输入一律判否。
export function isAliangCodeSession(entry) {
  if (!entry || typeof entry !== 'object') return false;
  return normalizeDeviceProvider(entry.provider) === 'agentcore';
}

// 设备会话清单条目 → 前端统一会话对象（跨端清单与本地会话同池渲染的适配
// 层）。id 加 dev- 前缀避免与本地会话 ID 撞车；title 缺省回落设备会话 ID；
// 原始条目挂在 deviceSession 供续聊分流读 origin/created_at。脏输入（null/
// undefined/非对象）安全返回 null，调用方过滤即可。
export function deviceSessionToConversation(entry) {
  if (!entry || typeof entry !== 'object') return null;
  const id = typeof entry.id === 'string' ? entry.id : '';
  return {
    id: 'dev-' + id,
    title: entry.title || id,
    provider: normalizeDeviceProvider(entry.provider),
    projectPath: entry.project_path || '',
    // model/effort 必须回填：服务端清单/详情已带 model（agent_inventory_*），
    // effort 由 journal 头行持久化。不回填会让续聊的设备会话设置条恒显示
    // 「默认」，且下一条消息重发 create 时带空值——旧后端会因此把服务端记住
    // 的模型抹掉（现已在 create 侧改为「空值不覆盖」双保险）。
    model: entry.model || '',
    effort: entry.effort || '',
    deviceSession: entry,
    createdAt: entry.created_at || '',
    updatedAt: entry.updated_at || '',
  };
}

// --- last-used 记忆 -------------------------------------------------------
//
// 用户拍板（2026-10-31）：默认模型留空；新建会话沿用「上一次用过的模型」。
// 模型清单不做后端接口（用户可能接第三方 LLM），因此这个记忆只能落在本端。
// 键名带前缀，避免与其它 localStorage 数据混。
export const LAST_USED_MODEL_KEY = 'aliang-last-used-model';
export const LAST_USED_EFFORT_KEY = 'aliang-last-used-effort';

function safeStorage() {
  try {
    return typeof localStorage !== 'undefined' ? localStorage : null;
  } catch {
    return null;
  }
}

// readLastUsedModel 返回上一次成功发送时用的模型；无记录/脏值返回 ''。
export function readLastUsedModel() {
  const s = safeStorage();
  if (!s) return '';
  try {
    const v = s.getItem(LAST_USED_MODEL_KEY);
    return typeof v === 'string' ? v.trim() : '';
  } catch {
    return '';
  }
}

export function rememberLastUsedModel(model) {
  const s = safeStorage();
  const v = typeof model === 'string' ? model.trim() : '';
  if (!s || !v) return;
  try {
    s.setItem(LAST_USED_MODEL_KEY, v);
  } catch {
    /* 配额满/隐私模式：记忆是锦上添花，失败不影响发送 */
  }
}

export function readLastUsedEffort() {
  const s = safeStorage();
  if (!s) return '';
  try {
    const v = s.getItem(LAST_USED_EFFORT_KEY);
    return typeof v === 'string' ? v.trim() : '';
  } catch {
    return '';
  }
}

export function rememberLastUsedEffort(effort) {
  const s = safeStorage();
  const v = typeof effort === 'string' ? effort.trim() : '';
  if (!s) return;
  try {
    if (v) s.setItem(LAST_USED_EFFORT_KEY, v);
    else s.removeItem(LAST_USED_EFFORT_KEY);
  } catch {
    /* 同上 */
  }
}

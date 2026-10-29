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

// —— attach 语义（服务端契约见 ai.session.create 分支）——
// 对已存在的 session_id 发 create = attach：不新建、回执 ai.session.created
// 全字段 + attached:true。六个属主字段（provider/model/effort/resume_session_id/
// binding_version/initial_context）服务端在 attach 分支静默忽略——前端干脆不发，
// 换 provider/model 只能新建会话。attach 载荷 = 仅 type + session_id。
export function attachSessionPayload(sessionId) {
  return { type: 'ai.session.create', session_id: sessionId || '' };
}

// watch/unwatch：成功无回执（无 ai.error 即成功）；中途/重复 watch 幂等。
export function watchSessionPayload(sessionId) {
  return { type: 'ai.session.watch', session_id: sessionId || '' };
}

export function unwatchSessionPayload(sessionId) {
  return { type: 'ai.session.unwatch', session_id: sessionId || '' };
}

// watch 生命周期：同一时刻最多直播跟踪一个会话。从 current 切到 next 的操作计划
// （先 unwatch 旧的，再 watch 新的；相同 id → 无操作）。纯函数，ChatPage 依此发帧。
export function planWatchSwitch(currentSessionId, nextSessionId) {
  const cur = currentSessionId || null;
  const next = nextSessionId || null;
  if (cur === next) return [];
  const ops = [];
  if (cur) ops.push({ op: 'unwatch', sessionId: cur });
  if (next) ops.push({ op: 'watch', sessionId: next });
  return ops;
}

// watch 目标：仅 attached 会话可被直播跟踪。本地会话必须返回 null、绝不自 watch：
// 服务端 emit 对同一连接先扇出 watcher 再写属主，同 connID 两路都写且不去重，
// 本地会话自 watch 会让 delta 文本翻倍。
export function watchTargetOf(conv) {
  return conv && conv.attached && conv.aiSessionId ? conv.aiSessionId : null;
}

// 流式中的助手气泡（status=running）。零依赖纯函数，ChatPage 与转录守卫共用。
export function findRunningAssistant(conv) {
  const msgs = (conv && conv.messages) || [];
  for (let i = msgs.length - 1; i >= 0; i--) {
    if (msgs[i].role === 'assistant' && msgs[i].status === 'running') return msgs[i];
  }
  return null;
}

// 转录加载门槛：仅 attached 会话、未在加载、未加载过、且**不在运行中**——
// 转录整体替换 messages，运行中加载会把逐字直播的气泡冲掉，后续 delta 又因
// 末尾已是 done 气泡被丢弃（回合剩余输出静默丢失）。
export function shouldLoadTranscript(conv) {
  if (!conv || !conv.attached || !conv.aiSessionId) return false;
  if (conv.transcriptLoading || conv.transcriptLoaded) return false;
  return !findRunningAssistant(conv);
}

// 帧归位：优先按 session_id 精确命中（matchedById 纪律）；未命中才走 fallback——
// 且 fallback 仅允许本地（非 attached）会话承接：attached 会话是别人的直播流，
// 绝不能被他页会话/未知 session 的帧污染。
export function findConversationForEvent(conversations, sessionId, activeConversation) {
  if (sessionId) {
    const matched = (Array.isArray(conversations) ? conversations : [])
      .find(c => c && c.aiSessionId === sessionId);
    if (matched) return matched;
  }
  if (activeConversation && !activeConversation.attached) return activeConversation;
  return null;
}

// 抽屉条目组装：会话全量列表 + runtime 快照（运行中徽章）+ 本页绑定集合
// （bound = 该 session 已被本页某个对话接入；手机/桌面来源的区分标注需服务端
// 在 sessions 端点补 origin 注解，当前数据不含——文案只用「已接入/未接入」），
// 按 updated_at 倒序，客户端分页（服务端无分页）。任何脏输入安全。
export function agentSessionDrawerEntries(sessions, { runningIds = [], localSessionIds = [], limit = 20, offset = 0 } = {}) {
  if (!Array.isArray(sessions)) sessions = [];
  const running = runningIds instanceof Set ? runningIds : new Set(runningIds || []);
  const bound = localSessionIds instanceof Set ? localSessionIds : new Set(localSessionIds || []);
  const entries = sessions
    .filter(s => s && typeof s === 'object' && s.id)
    .map(s => ({
      id: s.id,
      title: typeof s.title === 'string' ? s.title.trim() : '',
      provider: s.provider || s.tool || '',
      projectPath: s.project_path || '',
      updatedAtMs: Date.parse(s.updated_at || '') || 0,
      running: running.has(s.id),
      bound: bound.has(s.id),
    }))
    .sort((a, b) => b.updatedAtMs - a.updatedAtMs);
  const size = Math.max(limit, 0);
  const page = entries.slice(offset, offset + size);
  return { items: page, total: entries.length, hasMore: offset + page.length < entries.length };
}

// 服务端转录行（/api/agent/session → transcript[]）→ 本页消息气泡。
// 空 content 行丢弃；role 非 user 一律按 assistant 渲染。
export function transcriptToMessages(rows) {
  if (!Array.isArray(rows)) return [];
  return rows
    .filter(r => r && typeof r === 'object' && typeof r.content === 'string' && r.content.trim() !== '')
    .map(r => ({
      role: r.role === 'user' ? 'user' : 'assistant',
      content: r.content,
      status: 'done',
      ts: Date.parse(r.timestamp || '') || 0,
    }));
}

// 刷新恢复候选：attach 接管过、带真实设备会话 id 的会话。session id 形态由
// 服务端定（codex_*/claude_*/agentcore 内部 id 等），前端只排除自己造的
// local- 伪 id——按 ai_ 前缀过滤会把几乎全部真实会话排除掉。
export function restorableAttachedSessionIds(conversations) {
  if (!Array.isArray(conversations)) return [];
  return conversations
    .filter(c => c && c.attached && typeof c.aiSessionId === 'string' && c.aiSessionId !== '' && !c.aiSessionId.startsWith('local-'))
    .map(c => c.aiSessionId);
}

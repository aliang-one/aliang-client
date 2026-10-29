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

// watch 目标：仅 attached 会话可被直播跟踪。本地会话必须返回 null、绝不自 watch。
// 注：服务端 Task 4.5 已根治自 watch 双投递（扇出跳过属主连接），此前本地自
// watch 会让同一条 delta 经属主+watcher 两路写、文本翻倍——本门保留为纵深防御，
// 前端不依赖服务端版本。
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
// （bound = 该 session 已被本页某个对话接入）+ 服务端 origin 注解（Task 4.5
// 起 sessions 端点与手机 sync 快照同源：managed=手机绑定 / external=设备
// 原生，活跃条目恒 managed），按 updated_at 倒序，客户端分页（服务端无分页）。
// 任何脏输入安全。
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
      origin: s.origin || '',
      updatedAtMs: Date.parse(s.updated_at || '') || 0,
      running: running.has(s.id),
      bound: bound.has(s.id),
    }))
    .sort((a, b) => b.updatedAtMs - a.updatedAtMs);
  const size = Math.max(limit, 0);
  const page = entries.slice(offset, offset + size);
  return { items: page, total: entries.length, hasMore: offset + page.length < entries.length };
}

// 来源标记三态：本页自建/接入 → 本机（bound 优先，attached 样式另表）；
// 否则 managed（手机绑定）→ 手机；external/未知 → 桌面（设备原生）。
export function originBadgeOf(entry) {
  if (entry && entry.bound) return 'local';
  if (entry && entry.origin === 'managed') return 'phone';
  return 'desktop';
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

// —— Task 5：直播帧渲染 + steer/stop 操作（载荷与流式气泡纯逻辑）——

// 插话：注入正在运行的回合（Required=type,session_id,message_id,content）。
// 回执 ai.steer.ack 按 message_id 对账；result!=ok（not_running/turn_ended）
// 表示没送进回合。
export function steerPayload(sessionId, content, messageId) {
  return { type: 'ai.steer', session_id: sessionId || '', message_id: messageId || '', content: content || '' };
}

// 停止：Required=type,session_id（run_id/source_session_id 可选，网页无 run_id 不带）。
export function stopPayload(sessionId) {
  return { type: 'ai.stop', session_id: sessionId || '' };
}

// 审批对账拉取：client→agent 请求 still-pending 全集（回执 ai.approval.sync
// 带 pending，且向该会话 watcher 扇出）。用于自愈「属主兼 watcher 时旁路帧
// 可能缺一份」的不对称——sync 是可主动拉取的权威源。
export function approvalSyncRequestPayload() {
  return { type: 'ai.approval.sync' };
}

// ai.run.progress 归一：只留可渲染的事实（重试/文件/Git 计数），缺省/非法
// 数值归 NaN 由调用方过滤。
export function runProgressFacts(e) {
  const src = e && typeof e === 'object' ? e : {};
  const num = (v) => (typeof v === 'number' && Number.isFinite(v) ? v : NaN);
  return {
    retrying: src.retry_active === true || src.retry_active === 'true',
    attempt: num(src.retry_attempt),
    max: num(src.retry_max),
    files: num(src.files_touched_count),
    git: num(src.git_changed_count),
  };
}

// 流式气泡定位：kind='prose'（正文）/'thinking'（思考）。规则：
// ①尾部同类 running 气泡 → 续写；②末条助手气泡是 prose 且已收尾 → 回合已
// 终结，迟到帧丢弃（绝不滞留孤泡）；③其余（思考尾 done、用户尾、空）→ 开
// 新气泡——思考结束转正文时 prose 必须另起新泡，不能塞进思考泡。
export function ensureStreamingBubble(conv, kind = 'prose') {
  const msgs = (conv && conv.messages) || [];
  const last = msgs[msgs.length - 1];
  if (last && last.role === 'assistant' && (last.kind || 'prose') === kind && last.status === 'running') return last;
  if (last && last.role === 'assistant' && (last.kind || 'prose') === 'prose' && (last.status === 'done' || last.status === 'error')) return null;
  const fresh = { role: 'assistant', kind, content: '', status: 'running', ts: Date.now() };
  if (conv) conv.messages.push(fresh);
  return fresh;
}

// 回合开始（ai.run.started）：权威新回合。已有 running 占位（本地 sendMessage
// 先推的占位）→ 复用；否则强制开新泡——attached 直播多回合时上一回合 prose
// 已 done，必须开新泡而不是丢弃（旧 ensureAssistantBubble 在此丢帧）。
export function startRunBubble(conv) {
  const msgs = (conv && conv.messages) || [];
  const last = msgs[msgs.length - 1];
  if (last && last.role === 'assistant' && last.status === 'running') return last;
  const fresh = { role: 'assistant', content: '', status: 'running', ts: Date.now() };
  if (conv) conv.messages.push(fresh);
  return fresh;
}

// 回合终结：把所有 running 助手气泡（正文+思考）翻到终态。
export function finishRunBubbles(conv, status) {
  let touched = false;
  for (const m of (conv && conv.messages) || []) {
    if (m.role === 'assistant' && m.status === 'running') {
      m.status = status;
      touched = true;
    }
  }
  return touched;
}

// ai.approval.sync.pending 里的审批 id 全集（authoritative-replace 的 keep 集合）。
export function syncApprovalIdSet(pending) {
  const ids = new Set();
  for (const item of Array.isArray(pending) ? pending : []) {
    const id = item && typeof item === 'object' ? String(item.approval_id || item.id || '') : '';
    if (id) ids.add(id);
  }
  return ids;
}

// 对账裁剪：该 session 下不在 keep 集合里的待批卡移除（服务端已不认）；
// responding 的保留（本地请求在途，结果由 ack/终态帧收口）。
export function dropStaleApprovals(approvals, sessionId, keepIds) {
  if (!Array.isArray(approvals)) return [];
  const keep = keepIds instanceof Set ? keepIds : new Set(keepIds || []);
  return approvals.filter(a => !a || a.sessionId !== sessionId || !a.id || keep.has(a.id) || a.status === 'responding');
}

// 转录×直播对账（衔接点）：转录是冷路径快照，journal 落盘滞后会尾部缺段——
// 抓取窗口内直播落进来的消息不能被盲替换丢掉。合并=转录全量 + 窗口内直播
// 消息中未被转录覆盖的（role+content 精确去重，防两端都落时重复气泡）。
export function mergeTranscriptWithLive(rows, liveSnapshot, fetchStartedTs) {
  const transcript = transcriptToMessages(rows);
  const live = (Array.isArray(liveSnapshot) ? liveSnapshot : [])
    .filter(m => m && typeof m === 'object' && (m.ts || 0) >= (fetchStartedTs || 0));
  if (!live.length) return transcript;
  const seen = new Set(transcript.map(m => `${m.role}\u0000${m.content}`));
  const tail = live.filter(m => !seen.has(`${m.role}\u0000${m.content}`));
  return transcript.concat(tail);
}

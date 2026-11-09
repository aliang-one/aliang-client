import { ref, reactive, computed, nextTick, onMounted, onUnmounted, watch } from 'vue';
import { useI18n } from '../i18n';
import { useNavigation } from './useNavigation';
import { slashProviderFor } from '../utils/chatProvider';
import { normalizeConversationTitle } from '../utils/chatTitle';
import { useAuthStore } from '../stores/auth';
import { uploadChatAttachment } from '../services/agentApi';

// 附件缩略图/原图 URL（spec 2026-11-05-chat-image-pipeline §3.6）：ref 来自
// journal/wire 行，一律走本机 agent 回环端点取图（COS/预签名 URL 永不出现在
// 前端）。组件直接 import 本模块级纯函数。
// --- 设备会话详情装载参数（journal 分页拉全）---
// create 时的 transcript 喂入是续聊上下文的全部来源（载荷自带历史时后端
// 跳过 journal 读回），默认 limit=40 的截断尾巴会让续聊静默丢早期上下文——
// 必须拉全。单页 100 行、最多 5 页（500 行封顶）：防异常服务端拖死打开流程；
// 超出封顶接受截断，仍远好于 40 行尾巴。
const DEVICE_DETAIL_PAGE_LIMIT = 100;
const DEVICE_DETAIL_MAX_PAGES = 5;

// 详情分页拉全（导出纯函数）：openDeviceSession 与草稿↔journal 对账
// （reconcileDraftWithJournal）同源实现——翻页方向「向更早」（before = 上一页
// 最旧行的 message_id），每页新行前插保持时间正序；中途页失败降级为已取到
// 的部分转录（pageError 带回，调用方留痕）；空页即停防异常服务端死循环；
// maxPages 封顶接受截断。fetchDetail 注入（生产=getAgentSessionDetail，测试=mock）。
export async function fetchFullJournalTranscript(id, fetchDetail, { limit = DEVICE_DETAIL_PAGE_LIMIT, maxPages = DEVICE_DETAIL_MAX_PAGES } = {}) {
  const pageRows = [];
  let before = '';
  let pageError = null;
  let lastDetail = null;
  for (let page = 0; page < maxPages; page++) {
    try {
      const detail = await fetchDetail(id, { limit, before: before || undefined });
      const rows = Array.isArray(detail?.transcript) ? detail.transcript : [];
      pageRows.unshift(rows);
      lastDetail = detail;
      const pg = detail?.transcript_page;
      if (!pg?.has_more || !pg?.next_before_message_id || !rows.length) break;
      before = String(pg.next_before_message_id);
    } catch (err) {
      pageError = err;
      break;
    }
  }
  return { transcript: pageRows.flat(), lastDetail, pageError };
}

// 草稿↔journal 对账判定（导出纯函数）：本地会话对象只有在「发过消息
// （aiSessionId 存在）+ journal 清单有同 id 会话 + 非 running（流式气泡
// 保护）+ TTL 外」时才需要以 journal 重建历史。running 中跳过——重建会把
// 流式中的气泡换成 journal 块级快照；run 终态由既有快照对账接管。
export function draftNeedsJournalReconcile(conv, journalSessions) {
  if (!conv || conv.running) return false;
  if (!conv.aiSessionId) return false;
  if (!Array.isArray(journalSessions) || !journalSessions.some(s => s && s.id === conv.aiSessionId)) return false;
  if (Date.now() - (conv.deviceLoadedAt || 0) <= DEVICE_SESSION_DETAIL_TTL_MS) return false;
  return true;
}

// 对账 TTL 复用 openDeviceSession 语义，re-export 供调用方/测试单一来源。
export { DEVICE_SESSION_DETAIL_TTL_MS };

export function chatAttachmentUrl(refId) {
  if (!refId) return '';
  return '/api/agent/ai/attachment?ref=' + encodeURIComponent(refId);
}
import { getAgentIntegrations, getAgentSessionDetail } from '../services/agentApi';
import { useDeviceSessions, DEVICE_SESSION_DETAIL_TTL_MS, isTerminalPrefixedSessionId } from './useDeviceSessions';
import {
  normalizeDeviceProvider,
  readLastUsedEffort,
  readLastUsedModel,
  rememberLastUsedEffort,
  rememberLastUsedModel,
  sessionCreatePayload,
  dropShadowedDeviceShells,
} from '../utils/chatSettings';
import {
  canRecall,
  recallCreatePayload,
  sliceMessagesBefore,
} from '../utils/chatRecall';
import { applySubagentEvent, hasActiveSubagents } from '../utils/subagentPanel';

const STORAGE_KEY = 'aliang-chat-data';

// 重连预算的归零帧型（模块级常量）：只认「run 数据面」帧——纯订阅握手快照
// 不算（防「连上→回一帧快照→立刻断」的代理假活环境把预算洗白成无限重连）。
const AGENT_AI_RECONNECT_RESET_TYPES = new Set([
  'ai.session.created', 'ai.run.started', 'ai.delta', 'ai.status', 'ai.run.progress',
  'ai.run.notice', 'ai.done', 'ai.error', 'ai.approval.request', 'ai.approval.cancelled',
  'ai.option.request', 'ai.option.cancelled', 'ai.steer.ack',
  // tool_ask 交互（mcp__aliang__ask_user）同属审批族广播面（fanOut 白名单同
  // 表），数据面帧型一并归零重连预算（2026-11-08 dashboard 渲染接入）。
  'tool_ask.request', 'tool_ask.cancel',
  // 子agent活动也是 run 数据面（spec 2026-11-07-subagent-panel §三）：回合主体静默、
  // 子agent干活期间帧流不断——收到即证明管道活，重连预算随之归零（有利副作用）。
  'ai.subagent',
]);

// 无活动看门狗阈值：模块顶层读取（E2E 经 window.__CHAT_IDLE_TIMEOUT_MS__
// 注入短值，注入脚本先于应用模块求值，与原 ChatPage.vue 内联声明等价）。
const CHAT_IDLE_TIMEOUT_MS = Number(globalThis.__CHAT_IDLE_TIMEOUT_MS__) || 90000;

// 撤回链的会话终态等待阈值（Task 13）：同 __CHAT_IDLE_TIMEOUT_MS__ 手法，
// E2E 经 window.__CHAT_STOP_TIMEOUT_MS__ 注入短值；0 会被 || 回退到默认。
const CHAT_STOP_TIMEOUT_MS = Number(globalThis.__CHAT_STOP_TIMEOUT_MS__) || 10000;

// IME 组合期（中文/日文输入法）的 Enter/Esc 属于「确认候选词 / 取消组合」，
// Firefox/Safari 会如实上报 key，模板修饰符拦不住，必须自己判。
// 也不能靠模板 `.prevent`：withModifiers 会先 preventDefault 再进 handler，
// 那样会把输入法自己的按键处理一起吃掉。
// 纯函数：重命名输入（侧栏/顶栏）与会话设置条的自定义输入框共用，导出供
// chat/ 子组件 import（拆分前是 ChatPage.vue 内的单一实现）。
export function isComposingEvent(e) {
  return e.isComposing || e.keyCode === 229;
}

// 审批决定归一（纯函数）：审批卡渲染（availableDecisions 按钮集）与
// respondApproval 发帧共用，导出供 ChatApprovalCard import。
export function normalizeApprovalDecision(value) {
  const lower = String(value || '').replace(/[-\s]/g, '_').toLowerCase();
  if (['accept', 'accepted', 'approve', 'approved', 'allow', 'allowed', 'yes'].includes(lower)) return 'accept';
  if (['accept_for_session', 'acceptforsession', 'approved_for_session', 'approve_for_session', 'allow_for_session'].includes(lower)) return 'accept_for_session';
  if (['decline', 'declined', 'deny', 'denied', 'reject', 'rejected', 'no'].includes(lower)) return 'decline';
  if (['cancel', 'abort', 'timed_out', 'timeout'].includes(lower)) return 'cancel';
  return '';
}

export function normalizeApprovalDecisions(values) {
  const input = Array.isArray(values) ? values : [];
  const out = [];
  for (const item of input) {
    const normalized = normalizeApprovalDecision(item);
    if (normalized && !out.includes(normalized)) out.push(normalized);
  }
  return out.length ? out : ['accept', 'decline', 'cancel'];
}

// 设备转录系统条气泡（G5 错误终态；纯函数，导出供单测，与上方审批归一纯函数
// 同族）：server 读侧（agent_inventory_agentcore.go / agent_ai_journal_history.go）
// 把 journal end(error) 行折成 role=system 的人话终态文案下发，前端镜像仓内
// 系统条先例（审批取消/运行提示条：role:'assistant'+kind:'system'+note+
// status:'done'）渲染为居中弱化胶囊。note='turn_failed' 供 testid 分流；文案
// server 已人话化，原样展示。
export function deviceSystemNoteBubble(content, ts) {
  return { role: 'assistant', kind: 'system', note: 'turn_failed', content, status: 'done', ts };
}

// 详情转录行 → 壳气泡 + 喂入流（纯函数，导出供单测）。user/assistant 原样
// 落气泡并进喂入；role=system 行（G5 错误终态系统条）**只落气泡、绝不进喂入**
// ——deviceTranscript 是续聊的模型上下文，事实说明喂回去会污染；其余角色
// （tool 轨迹等）跳过。
export function deviceDetailTranscriptToMessages(rows, provider, fallbackTs) {
  const list = Array.isArray(rows) ? rows : [];
  const renderable = list.filter(m => m && (m.role === 'user' || m.role === 'assistant' || m.role === 'system')
    && typeof m.content === 'string' && m.content);
  return {
    messages: renderable.map(m => m.role === 'system'
      ? deviceSystemNoteBubble(m.content, Date.parse(m.timestamp) || fallbackTs)
      : {
        role: m.role,
        content: m.content,
        status: 'done',
        // 撤回锚点接线（Task 13）：详情 wire 行的 id 即 journal message_id，落
        // 气泡后可作 truncate_before_message_id；provider 落上（canRecall 依赖）。
        messageId: m.id || '',
        provider: provider || '',
        ts: Date.parse(m.timestamp) || fallbackTs,
      }),
    feed: renderable.filter(m => m.role !== 'system').map(m => ({ role: m.role, content: m.content })),
  };
}

// 快照转录行 → 壳气泡（纯函数，导出供单测；**仅展示流**）。字段名与详情不同
// （快照用 text，详情用 content）。system 行同款落系统条（快照侧本就不写
// deviceTranscript，无需喂入分离）。
export function deviceSnapshotTranscriptToMessages(rows, provider, fallbackTs) {
  const list = Array.isArray(rows) ? rows : [];
  const renderable = list.filter(m => m && (m.role === 'user' || m.role === 'assistant' || m.role === 'system')
    && typeof m.text === 'string' && m.text);
  return {
    messages: renderable.map(m => m.role === 'system'
      ? deviceSystemNoteBubble(m.text, Date.parse(m.ts) || fallbackTs)
      : {
        role: m.role,
        content: m.text,
        status: 'done',
        // 快照 id 系 journal 同源（spec §A 已修订），可作撤回锚点；内容仍绝不
        // 喂入（deviceTranscript 专属流不变）。provider 落上（canRecall 依赖）。
        messageId: m.message_id || '',
        provider: provider || '',
        ts: Date.parse(m.ts) || fallbackTs,
      }),
  };
}

// --- 本地会话跨端回合渲染（2026-11-09 vm22 事故族，导出供单测） ---
//
// 手机端向网页正在使用的同一 journal 会话发问后，旧对账把锚点之后**所有**
// assistant 行拼进最后一个气泡（多答连体）、外来 user 行永不回建（提问隐形）、
// 活体 delta 续写滞留旧泡或整段丢弃。核心语义：journal 是会话结构的唯一权威，
// 本地视图是缓存+装饰——快照对账按「回合分段 + 权威修复 + 外来回合补建」
// 收敛，run 生命周期帧按窗口归属流。纯函数内核 + composable 薄壳装配（同
// deviceSnapshotTranscriptToMessages 先例）。

// 倒序找首个 running 的 assistant 气泡（原 composable 内联实现上提：纯函数，
// 供帧处理器/纯内核/测试共用）。
export function findRunningAssistant(conv) {
  const msgs = (conv && Array.isArray(conv.messages)) ? conv.messages : [];
  for (let i = msgs.length - 1; i >= 0; i--) {
    if (msgs[i] && msgs[i].role === 'assistant' && msgs[i].status === 'running') return msgs[i];
  }
  return null;
}

// 返回活体 assistant 气泡，必要时产新占位（原内联实现上提 + run 窗口感知）。
// run 窗口内（conv.runActive，run.started..终态）无 running 泡必产新——外来
// 回合/选项作答回合的 delta/thinking 不再被「末泡已 done」守卫整段吞掉
// （2026-11-09 vm22 缺陷③）。窗口外维持旧守卫：终态后的迟到 delta 丢弃，
// 防空泡垃圾（原注释语义不变）。
export function ensureAssistantBubble(conv, ts) {
  if (!conv) return null;
  const asst = findRunningAssistant(conv);
  if (asst) return asst;
  const msgs = Array.isArray(conv.messages) ? conv.messages : [];
  const last = msgs[msgs.length - 1];
  if (!conv.runActive
    && last && last.role === 'assistant' && (last.status === 'done' || last.status === 'error')) {
    return null;
  }
  const fresh = { role: 'assistant', content: '', status: 'running', ts };
  conv.messages.push(fresh);
  return fresh;
}

// 回合启动权威收敛（纯函数）。服务端串行回合语义：新回合 started 即上一回合
// 已终——滞留 running 的旧泡在此关账（终态帧丢失类的根治口），绝不与新回合
// 增量合流（挤一泡根因）。ownPending=true 表示本端 send 在途（占位泡即本回合
// 泡，可能已含先于 run.started 到达的 delta 内容），绝不关账、按复用认领；
// false（外来回合/后台会话/设备壳）则关账全部 running 后产本回合新泡。
// 置 runActive/running，返回本回合 ensured 气泡。
export function beginRunWindow(conv, ts, { ownPending = false } = {}) {
  if (!conv) return null;
  conv.runActive = true;
  conv.running = true;
  if (!ownPending) {
    const msgs = Array.isArray(conv.messages) ? conv.messages : [];
    for (const m of msgs) {
      if (m && m.role === 'assistant' && m.status === 'running') m.status = 'done';
    }
  }
  return ensureAssistantBubble(conv, ts);
}

// 回合终态收窗（纯函数）：runActive/running 复位 + 残留 running 泡关账；
// 返回是否曾有 running 泡（调用方据此决定终态定谳复订阅）。error 气泡由
// 调用方先置 error，此处只收剩余 running。
export function endRunWindow(conv) {
  if (!conv) return false;
  const had = !!findRunningAssistant(conv);
  const msgs = Array.isArray(conv.messages) ? conv.messages : [];
  for (const m of msgs) {
    if (m && m.role === 'assistant' && m.status === 'running') m.status = 'done';
  }
  conv.runActive = false;
  conv.running = false;
  return had;
}

// 会话 running 判定（原内联实现上提 + run 窗口并入）：快照态 OR run 窗口
// OR 本地气泡推断，三源并集。另一端跑着的会话（壳 running/快照 running）
// 与本端正在流的会话都算 running——running 期间本端新消息走 steer 分流
// （跨端统一阶段 2 Task 4 语义），窗口并入后判定不再被滞留旧泡独家把持。
export function isConversationRunning(conv) {
  if (!conv) return false;
  return !!conv.running || !!conv.runActive || !!findRunningAssistant(conv);
}

// 外来回合启动是否补订阅（纯函数）：run.started 不携带提问行，订阅握手回包
// （快照）是网页拿到外来 user 行的唯一载体。仅本端命中（matchedById）且非
// 设备壳的本地会话补订阅——壳走自身快照通道，未命中帧（fallback 兜底）绝不
// 触发订阅（会话已删/他会话的帧不得放大订阅面）。
export function runStartedNeedsSnapshot(conv, matchedById) {
  return !!matchedById && !!conv && !conv.deviceSession
    && typeof conv.aiSessionId === 'string' && !!conv.aiSessionId;
}

// 回合终态是否定谳复订阅（纯函数）：journal turn 行终态才齐——恢复会话
// （recoveredRun）与本回合曾有 running 泡（含外来回合活体补建）的答案全文
// 由终态后的快照收敛。
export function runDoneNeedsSnapshot(conv, hadRunning) {
  return !!conv && !conv.deviceSession
    && typeof conv.aiSessionId === 'string' && !!conv.aiSessionId
    && (conv.recoveredRun || hadRunning);
}

// 快照转录行 + 本地消息 → 对账后的本地消息（纯函数，导出供单测）。
//
// 算法（回合分段）：
//   ① rows（快照读侧已折叠为 user/assistant/system 行）按 user 行切回合；
//      帽截断孤儿头行（首条 user 行被截走）跳过——回合语义外。
//   ② 锚 = 最后一个 userRow.message_id 命中本地 user 气泡的回合；无锚整体
//      no-op（安全语义：本地气泡与 journal 无交集时不越权重排）。
//   ③ 锚点前区域不动（同引用透传）；锚回合文本按修复律收敛：
//      - 已定谳（存在后续回合，或快照非 running）：journal 更长即替换（增长
//        对齐 + 恢复尾巴片段——delta 尾巴与 journal 全文互不为前缀，2026-11-05
//        「只看到 59 60」形态）；local ⊇ journal 前缀 = 旧对账连体垃圾特征，
//        替换；双向皆非前缀且 journal 不更长（rune 截断标记）→ 保守不动。
//      - 流中（末回合 + 快照 running）：仅 journal⊇local 增长式对齐，绝不
//        收缩（半回合 delta 不许丢）。
//      - 锚回合无气泡且 journal 有文本 → 补建（吸收旧 noBubble 分支）。
//   ④ 外来回合逐个补建：user 气泡（journal 同源 messageId/provider/附件；
//      `.option` 作答行不新建——翻卡即作答，补泡与卡重复）、回合答案气泡
//      （末回合且 running → running，余 → done）、end(error) 系统条照序。
//   ⑤ 尾随 running 泡归属：内容非锚回合文本前缀且存在外来回合 → 归末个外来
//      回合的活体（delta 先于快照落泡的竞态），user 气泡插其前、答案不重复。
//   ⑥ 无 journal 对应物的本地装饰（失败发送/系统条）按原序保留在所属区域。
//
// 幂等：外来气泡二次对账按 messageId/区域复用原对象，零改动即 changed=false
// （调用方不重赋值不落盘，零渲染抖动）。
//
// 返回 { messages, changed, appended, closedRunning, alignedLive, createdRunning }：
// changed=有改动（调用方重赋值+落盘）；appended=新增泡数（贴底）；
// closedRunning=关账 running 数（调用方做审批清卡/finishRun 收敛）；
// alignedLive=流中泡内容被 journal 对齐（调用方打 snapshotCatchupAt 去重窗）；
// createdRunning=补建了 running 态泡（调用方置 recoveredRun，终态回取全文）。
export function reconcileLocalSnapshotMessages(localMessages, rows, opts = {}) {
  const { provider = '', snapshotRunning = false, fallbackTs = 0 } = opts;
  const local = Array.isArray(localMessages) ? localMessages : [];
  const list = Array.isArray(rows) ? rows : [];
  const result = { messages: local, changed: false, appended: 0, closedRunning: 0, alignedLive: false, createdRunning: false };
  if (!list.length) return result;

  // ① rows → 回合
  const rounds = [];
  const tsOf = (r) => (r && typeof r.ts === 'string' && Date.parse(r.ts)) || fallbackTs;
  for (const row of list) {
    if (!row) continue;
    if (row.role === 'user') {
      rounds.push({ userRow: row, texts: [], notes: [], lastTs: tsOf(row) });
    } else if (row.role === 'assistant' || row.role === 'system') {
      if (!rounds.length) continue; // 帽截断孤儿头行
      const r = rounds[rounds.length - 1];
      if (row.role === 'assistant' && typeof row.text === 'string' && row.text) r.texts.push(row.text);
      if (row.role === 'system' && typeof row.text === 'string' && row.text) {
        r.notes.push({ text: row.text, ts: tsOf(row) });
      }
      r.lastTs = tsOf(row) || r.lastTs;
    }
  }
  if (!rounds.length) return result;

  // ② 锚
  const midIndex = new Map();
  local.forEach((m, i) => {
    if (m && m.role === 'user' && typeof m.messageId === 'string' && m.messageId
      && !midIndex.has(m.messageId)) {
      midIndex.set(m.messageId, i);
    }
  });
  let anchorIdx = -1;
  let anchorLocalIdx = -1;
  for (let i = rounds.length - 1; i >= 0; i--) {
    const rawMid = rounds[i].userRow && rounds[i].userRow.message_id;
    const mid = rawMid == null ? '' : String(rawMid);
    if (mid && midIndex.has(mid)) {
      anchorIdx = i;
      anchorLocalIdx = midIndex.get(mid);
      break;
    }
  }
  if (anchorIdx < 0) return result;

  const anchorRound = rounds[anchorIdx];
  const foreign = rounds.slice(anchorIdx + 1);
  const block = local.slice(anchorLocalIdx + 1);
  const anchorTexts = anchorRound.texts.join('\n\n');
  const settled = foreign.length > 0 || !snapshotRunning;
  const isPlainAsst = (m) => !!(m && m.role === 'assistant' && !m.kind && !m.note);

  // 外来回合 mid → 回合序号（复用判定用）
  const foreignByMid = new Map();
  foreign.forEach((r, idx) => {
    const rawMid = r.userRow && r.userRow.message_id;
    const mid = rawMid == null ? '' : String(rawMid);
    if (mid) foreignByMid.set(mid, anchorIdx + 1 + idx);
  });

  // 单回合补建（user 气泡 + 答案泡 + 系统条）。reuseUser/reuseAnswer 传本地
  // 既有对象则原位复用（幂等），否则新建。liveBubble=该回合活泡已在本地区
  // （只补 user 行，答案泡由活泡承担）。
  const emitRound = (k, reuseUser, reuseAnswer, liveBubble) => {
    const r = rounds[k];
    const rawMid = r.userRow && r.userRow.message_id;
    const mid = rawMid == null ? '' : String(rawMid);
    // `.option` 作答行：已翻卡承载决策，绝不新建 user 泡（既有泡保留）
    const isOptionRow = /^assistant_.+\.option$/.test(mid);
    if (reuseUser) {
      if (!reuseUser.provider) {
        reuseUser.provider = provider;
        result.changed = true;
      }
      out.push(reuseUser);
    } else if (!isOptionRow) {
      const atts = Array.isArray(r.userRow.attachments)
        ? r.userRow.attachments
          .filter(a => a && a.ref)
          .map(a => ({ name: (a && a.name) || '', url: chatAttachmentUrl(a.ref) }))
        : null;
      out.push({
        role: 'user',
        content: typeof r.userRow.text === 'string' ? r.userRow.text : '',
        messageId: mid,
        provider,
        ts: tsOf(r.userRow),
        ...(atts && atts.length ? { attachments: atts } : {}),
      });
      result.appended++;
      result.changed = true;
    }
    const isLast = k === rounds.length - 1;
    const liveHere = liveBubble && isLast && snapshotRunning;
    const text = r.texts.join('\n\n');
    const answer = liveHere ? liveBubble : reuseAnswer;
    if (answer) {
      // 修复律与锚回合同源：流中末回合仅 journal 前缀增长（canAlign 语义）；
      // 定谳回合 journal 更长即替换（覆盖恢复尾巴片段形态）+ 连体前缀替换。
      const streaming = isLast && snapshotRunning;
      if (text && text !== answer.content
        && (streaming
          ? text.startsWith(answer.content || '')
          : (text.length > (answer.content || '').length
            || (typeof answer.content === 'string' && answer.content.startsWith(text))))) {
        answer.content = text;
        result.changed = true;
      }
      answer.status = streaming ? 'running' : 'done';
      out.push(answer);
    } else if (text || (isLast && snapshotRunning)) {
      const fresh = {
        role: 'assistant',
        content: text,
        status: (isLast && snapshotRunning) ? 'running' : 'done',
        ts: r.lastTs || fallbackTs,
      };
      out.push(fresh);
      result.appended++;
      result.changed = true;
      if (fresh.status === 'running') result.createdRunning = true;
    }
    for (const n of r.notes) {
      out.push(deviceSystemNoteBubble(n.text, n.ts));
      result.appended++;
      result.changed = true;
    }
  };

  // ⑤⑥ 区扫描：块内条目按序归区。区界 = 命中外来回合的 user 泡（复用）。
  // 每区持有 {kind,k,userEntry,items,answerReuse,live};anchor 区候选=最后一个
  // plain 泡（更早的 running 关账）；尾随 running 且内容非锚文本前缀且存在
  // 外来回合 → 外来活泡（live），不参与 anchor 修复。
  const zones = [];
  let cur = { kind: 'anchor', k: -1, userEntry: null, items: [], answerReuse: null, live: null };
  for (const entry of block) {
    if (isPlainAsst(entry)) {
      if (cur.kind === 'anchor') {
        cur.items.push({ entry, cls: 'plain' });
      } else if (!cur.answerReuse && !cur.live) {
        const c = typeof entry.content === 'string' ? entry.content : '';
        if (snapshotRunning && entry.status === 'running' && cur.k === rounds.length - 1
          && !(anchorTexts && c && anchorTexts.startsWith(c))) {
          cur.live = entry; // 外来回合活体：delta 先于快照落泡
        } else {
          cur.answerReuse = entry;
        }
      } else {
        if (entry.status === 'running') {
          entry.status = 'done';
          result.closedRunning++;
          result.changed = true;
        }
        cur.items.push({ entry, cls: 'plain' });
      }
    } else if (entry && entry.role === 'user') {
      const mid = typeof entry.messageId === 'string' ? entry.messageId : '';
      if (mid && foreignByMid.has(mid)) {
        zones.push(cur);
        cur = { kind: 'foreign', k: foreignByMid.get(mid), userEntry: entry, items: [], answerReuse: null, live: null };
      } else {
        cur.items.push({ entry, cls: 'keep' }); // 失败发送等本地独有物
      }
    } else {
      cur.items.push({ entry, cls: 'keep' }); // 系统条/其他装饰
    }
  }
  zones.push(cur);

  // anchor 区尾随活泡上提（zone 判定只看 foreign 区;anchor 区的活泡单独检出,
  // 供末回合补建时挂靠——delta 先落泡、快照后到且外来提问无本地泡的竞态形态）
  let anchorLive = null;
  for (const z of zones) {
    if (z.kind !== 'anchor') continue;
    const plains = z.items.filter(it => it.cls === 'plain').map(it => it.entry);
    if (!plains.length || !foreign.length || !snapshotRunning) continue;
    const lastPlain = plains[plains.length - 1];
    if (lastPlain.status !== 'running') continue;
    const c = typeof lastPlain.content === 'string' ? lastPlain.content : '';
    if (anchorTexts && c && anchorTexts.startsWith(c)) continue; // 锚回合自身流泡
    anchorLive = lastPlain;
    z.items = z.items.filter(it => it.entry !== lastPlain);
  }

  // ③④ 分区发射
  const out = local.slice(0, anchorLocalIdx + 1);
  let emittedK = anchorIdx;
  for (const z of zones) {
    if (z.kind === 'anchor') {
      const plains = z.items.filter(it => it.cls === 'plain').map(it => it.entry);
      let candidate = null;
      for (const e of plains) {
        if (candidate && candidate.status === 'running') {
          candidate.status = 'done';
          result.closedRunning++;
          result.changed = true;
        }
        candidate = e;
      }
      if (candidate) {
        if (settled) {
          if (candidate.status === 'running') {
            candidate.status = 'done';
            result.closedRunning++;
            result.changed = true;
          }
          // 定谳修复律（三形态全覆盖）：journal 更长即替换（增长对齐 + 恢复
          // 尾巴片段——delta 尾巴与 journal 全文互不为前缀，2026-11-05「只看
          // 到 59 60」）；local ⊇ journal 前缀 = 旧对账连体垃圾特征，替换；
          // 双向皆非前缀且 journal 不更长（rune 截断标记等退化）保守不动。
          const c = candidate.content;
          if (anchorTexts && anchorTexts !== c
            && (anchorTexts.length > (c || '').length
              || (typeof c === 'string' && c.startsWith(anchorTexts)))) {
            candidate.content = anchorTexts;
            result.changed = true;
          }
        } else if (anchorTexts && anchorTexts !== candidate.content
          && anchorTexts.startsWith(candidate.content || '')) {
          // 流中：journal 增长式对齐（canAlign 原语义），绝不收缩
          candidate.content = anchorTexts;
          result.alignedLive = true;
          result.changed = true;
        }
      } else if (anchorTexts) {
        // 锚回合无泡 + journal 有文本 → 补建（旧 noBubble 分支吸收）
        const fresh = {
          role: 'assistant',
          content: anchorTexts,
          status: settled ? 'done' : 'running',
          ts: anchorRound.lastTs || fallbackTs,
        };
        out.push(fresh);
        result.appended++;
        result.changed = true;
        if (fresh.status === 'running') result.createdRunning = true;
      }
      for (const it of z.items) out.push(it.entry);
    } else {
      // 区界前的无本地泡外来回合全量补建
      for (let k = emittedK + 1; k < z.k; k++) emitRound(k, null, null, null);
      emitRound(z.k, z.userEntry, z.answerReuse, z.live);
      for (const it of z.items) {
        if (it.cls === 'plain' && it.entry.status === 'running') {
          it.entry.status = 'done';
          result.closedRunning++;
          result.changed = true;
        }
        out.push(it.entry);
      }
      emittedK = z.k;
    }
  }
  // 尾冲刷：无区的剩余外来回合（含 anchorLive 挂靠末回合——活泡承担答案，
  // 只补 user 行，emitRound 对 liveHere 不再产答案泡）
  for (let k = emittedK + 1; k < rounds.length; k++) {
    emitRound(k, null, null, (k === rounds.length - 1) ? anchorLive : null);
  }
  if (result.appended > 0 || result.closedRunning > 0) result.changed = true;
  result.messages = out;
  return result;
}

// --- 子agent面板 WS 消费链（spec 2026-11-07-subagent-panel §三，导出供单测） ---
//
// ai.subagent 帧的消费拆成模块级纯函数（归属守卫+归一+聚合+M1 重拉触发），
// composable 内 switch case 只做薄壳装配——composable 需 Vue app 上下文难以
// 整体实例化，纯链单独可测（同 deviceDetailTranscriptToMessages 先例）。

// WS 蛇形帧 → reducer 驼峰消息（后端 agentAISubagentPayload 键名同源）。
// 字段缺省落 null/''：reducer 侧 blankEntry/maxOr/pickDefined 再兜底一层。
export function subagentFrameToReducerMsg(e) {
  if (!e || typeof e !== 'object') return null;
  return {
    taskId: typeof e.task_id === 'string' ? e.task_id : '',
    phase: e.phase,
    subagentType: typeof e.subagent_type === 'string' ? e.subagent_type : '',
    description: e.description ?? null,
    currentStep: e.current_step ?? null,
    lastTool: e.last_tool ?? null,
    toolUses: e.tool_uses ?? null,
    totalTokens: e.total_tokens ?? null,
    durationMs: e.duration_ms ?? null,
    summary: e.summary ?? null,
  };
}

// ai.subagent 帧消费主链（handleAIEvent case 的全部逻辑）。返回是否被消费，
// 供调试与测试断言；store 由调用方传入（composable 的 subagentsBySession 或
// 测试的普通对象），key=sessionId、value=该会话 subagents 数组。
// hooks.refetch：M1 详情静默重拉入口（composable 传真实现，测试传 spy）。
export function consumeSubagentFrame(store, e, activeSessionId, hooks = {}) {
  if (!e || typeof e !== 'object' || e.type !== 'ai.subagent') return false;
  // 会话归属守卫（spec M2，评审钉死）：仅帧 session_id 等于当前打开会话的
  // aiSessionId 才消费；不匹配（他会话帧——含经 fallback 挂活跃会话的兜底链
  // 路由进来的）一律丢弃，防他会话子agent帧污染当前面板。注意不能复用
  // handleAIEvent 上方的 conv 路由结果判归属：fallback 恰是把未命中帧挂上
  // 活跃会话的污染路径，守卫必须以「当前打开会话的 aiSessionId」为准。
  const sid = typeof e.session_id === 'string' ? e.session_id : '';
  const ownSession = typeof activeSessionId === 'string' && !!activeSessionId && sid === activeSessionId;
  if (!ownSession) return false;
  const msg = subagentFrameToReducerMsg(e);
  store[sid] = applySubagentEvent(store[sid], msg);
  // M1 报告重拉（spec §三）：completed|failed 到达且「用户正在看该子agent详情」
  // 时触发一次静默重拉（只消费返回的 subagents[]，转录一律不落壳——详情装载
  // 是整体替换 messages，全量重拉会顶掉正在流式输出的直播气泡）；不在看则不拉
  // （由面板打开逻辑兜底）。started/progress 不触发。
  if ((msg.phase === 'completed' || msg.phase === 'failed')
    && typeof hooks.isWatching === 'function' && hooks.isWatching(msg.taskId)
    && typeof hooks.refetch === 'function') {
    hooks.refetch(sid);
  }
  return true;
}

// 详情 API（AgentVibeSubagent，models/agent.go 蛇形键 + status 词表）→ reducer
// 条目形状（驼峰键 + phase 词表）。评审钉死的契约分歧收口：详情发 status
// 键（started|completed|failed）、WS 帧发 phase 键（词表含 progress/
// interrupted），原样入库会两形状混存致入口条/面板计数全 0。词表对齐后
// status→phase 同名直通，无需 interrupted 特判。progress 专有字段（WS 聚合
// 才有）补默认值，输出与 blankEntry 形状一致；result/tools/startedAt/endedAt
// 是详情专供字段（WS 条目不含），面板详情页消费，渲染层判空兜底。
export function subagentDetailToReducerEntry(detail) {
  if (!detail || typeof detail !== 'object') return null;
  const taskId = typeof detail.task_id === 'string' ? detail.task_id : '';
  if (!taskId) return null;
  const tools = Array.isArray(detail.tools)
    ? detail.tools
      .filter((tool) => tool && typeof tool === 'object')
      .map((tool) => ({
        tool: typeof tool.tool === 'string' ? tool.tool : '',
        summary: typeof tool.summary === 'string' ? tool.summary : '',
        isError: tool.is_error === true,
        ts: typeof tool.ts === 'string' ? tool.ts : '',
      }))
    : [];
  return {
    taskId,
    name: (typeof detail.name === 'string' && detail.name) ? detail.name : taskId,
    description: detail.description || null,
    // 缺 finish 行的条目 Go 读侧保持 started；空缺/未知按 started 兜底。
    phase: (typeof detail.status === 'string' && detail.status) ? detail.status : 'started',
    currentStep: null,
    lastTool: null,
    toolUses: 0,
    totalTokens: Number.isFinite(detail.total_tokens) ? detail.total_tokens : null,
    durationMs: Number.isFinite(detail.duration_ms) ? detail.duration_ms : null,
    summary: detail.summary || null,
    result: detail.result || null,
    startedAt: detail.started_at || null,
    endedAt: detail.ended_at || null,
    tools,
  };
}

// 详情响应 → 面板状态装配内核（spec 2026-11-07-subagent-panel H2/H3 共用）：
// 只消费 detail.subagents[] 整体替换该会话面板状态（入库前逐条归一为 reducer
// 条目形状——status→phase，坏条目过滤）；转录一律不落壳——详情转录装载走
// applyDeviceTranscript，subagents 绝不能混进 messages。详情未带 subagents 键
// （后端降级/旧版）时保留既有聚合态不覆写。消费方：refreshSubagentsFromDetail
// （M1 重拉/断线自愈，自带拉取）与 openDeviceSession（历史装配，直接吃详情
// 分页响应——subagents 是会话级字段，避免为装配再发一次请求）。
export function ingestDetailSubagents(store, sessionId, detail) {
  const sid = String(sessionId || '');
  if (!sid) return;
  const list = Array.isArray(detail?.subagents) ? detail.subagents : null;
  if (list) {
    store[sid] = list.map(subagentDetailToReducerEntry).filter(Boolean);
  }
}

// M1 详情静默重拉（真实实现，经 vi.mock 的 getAgentSessionDetail 可测）：
// 拉取后经 ingestDetailSubagents 只消费 subagents[]；失败静默——面板打开
// 逻辑兜底。同会话 in-flight 去重：连续 completed 同窗到达只发一次请求。
const subagentDetailRefreshInflight = new Set();
export async function refreshSubagentsFromDetail(store, sessionId) {
  const sid = String(sessionId || '');
  if (!sid || subagentDetailRefreshInflight.has(sid)) return;
  subagentDetailRefreshInflight.add(sid);
  try {
    const detail = await getAgentSessionDetail(sid);
    ingestDetailSubagents(store, sid, detail);
  } catch (_) {
    // 静默：详情缺席不阻断（journal 仍是持久真值，面板打开时再拉）
  } finally {
    subagentDetailRefreshInflight.delete(sid);
  }
}

// 决策跨端同步（2026-11-08）：ai.option.request 的 resolved 同型帧翻卡——
// 镜像审批族 status:"resolved" 约定（agent 侧 optionResponse 决策落地后重发
// 同型帧广播全端）。本端未作答时收到别端（手机/dashboard）作答，按 key 找卡
// 置 answered；已作答键位 handledOptionIds 幂等吸收；无对应卡（迟到/未知 id）
// 忽略。resolved 帧不带完整 options 载荷，绝不建新卡。顶层纯函数导出
// （deviceSystemNoteBubble 同模式，测试直接驱动）。
export function resolveOptionCard(conv, event, handledOptionIds, resolvedAt) {
  const optionId = String(event?.option_id || '').trim();
  if (!optionId) return false;
  const sessionId = String(event?.session_id || conv?.aiSessionId || '');
  const key = `${sessionId}:${optionId}`;
  if (handledOptionIds.has(key)) return false;
  const cards = Array.isArray(conv?.optionCards) ? conv.optionCards : [];
  const card = cards.find(c => c.key === key);
  if (!card) return false;
  const picked = Array.isArray(event.selected) ? event.selected.map(v => String(v)) : [];
  card.status = 'answered';
  card.selected = picked;
  card.customText = typeof event.custom_text === 'string' ? event.custom_text : '';
  card.error = '';
  card.updatedAt = resolvedAt;
  handledOptionIds.add(key);
  return true;
}

// 决策跨端同步（2026-11-08）：tool_ask.request 的 resolved 同型帧翻卡——
// agent 端 handleAskUser 作答落地后广播（同 option 约定），本端未作答时收到
// 别端（手机/dashboard）作答，按 key 找卡置 answered。answer 提取与
// respondToolAsk 同构：selected_option_id→单选、selected_option_ids→多选、
// message→customText。已答键位幂等吸收；无对应卡忽略，绝不建新卡。
export function resolveToolAskCard(conv, event, handledToolAskIds, resolvedAt) {
  const interactionId = String(event?.interaction_id || '').trim();
  if (!interactionId) return false;
  const sessionId = String(event?.session_id || conv?.aiSessionId || '');
  const key = `${sessionId}:${interactionId}`;
  if (handledToolAskIds.has(key)) return false;
  const asks = Array.isArray(conv?.toolAsks) ? conv.toolAsks : [];
  const card = asks.find(c => c.key === key);
  if (!card) return false;
  const answer = event.answer && typeof event.answer === 'object' ? event.answer : {};
  const picked = Array.isArray(answer.selected_option_ids)
    ? answer.selected_option_ids.map(v => String(v))
    : answer.selected_option_id
      ? [String(answer.selected_option_id)]
      : [];
  card.status = 'answered';
  card.selected = picked;
  card.customText = typeof answer.message === 'string' ? answer.message : '';
  card.error = '';
  card.updatedAt = resolvedAt;
  handledToolAskIds.add(key);
  return true;
}

// reconcileSnapshotApprovals 快照权威清卡（2026-11-09 桌面卡滞留家族③+④）：
// 旧实现对账只 upsert 不清卡——running 态下别端已作答的卡在桌面永 pending。
// 新契约：快照带 pending_approvals 字段（新 agent 恒带，空集=权威「无在挂」）
// 时，快照会话（snapshotSessionId）名下不在列表内的卡一律收走——按 sessionId
// **跨全部会话对象**扫（与 sweepApprovalKey 同口径的两半：resolved 帧全局扫卡
// 救「已作答」侧，快照清卡救「断线窗口错过 resolved」侧；卡经③活跃兜底/
// ①草稿壳双对象漂到别处，只清路由命中的那个对象会漏）。字段缺席（旧 agent）
// 不越权；外来会话卡（键为别的 sessionId）不在本快照管辖内，不得收走（评审
// P1 越权守卫）。
// **刻意不封键位**（与 resolved/cancelled 终态帧的封键不同）：快照（订阅握手
// 直写 WS）与审批 request 帧（fanOut 经 pump 队列）两条写路径不保序，先读
// 后写的陈旧快照可能晚于新 request 帧到达——封键会把竞态窗口里仍 pending 的
// 真卡永久锁死（审批卡从此隐形、回合挂死到超时）；不封键最坏退化为「迟到
// 重复 request 帧复活一张陈卡」，下一张快照或终态帧自愈，而终态帧封键
// （sweepApprovalKey→handledApprovalIds）已兜住复活面。返回是否发生变更。
export function reconcileSnapshotApprovals(convList, pendingApprovals, snapshotSessionId) {
  const ownSession = String(snapshotSessionId || '');
  if (!ownSession || !Array.isArray(pendingApprovals) || !Array.isArray(convList)) return false;
  const keep = new Set();
  for (const item of pendingApprovals) {
    if (!item || typeof item !== 'object') continue;
    const id = item.approval_id || item.id;
    if (!id) continue;
    keep.add(`${String(item.session_id || ownSession)}:${String(id)}`);
  }
  let changed = false;
  for (const conv of convList) {
    const approvals = Array.isArray(conv?.approvals) ? conv.approvals : [];
    const stale = approvals.filter(item => item?.key
      && String(item.sessionId || '') === ownSession
      && !keep.has(item.key));
    if (!stale.length) continue;
    conv.approvals = approvals.filter(item => !stale.includes(item));
    changed = true;
  }
  return changed;
}

// chatRecoverySubscribeSessionIds 恢复路径的设备壳补订清单（2026-11-09 评审
// P1）：服务端订阅挂 socket 键、连接结束整体摘除——断线恢复只订本地会话会让
// 已打开的设备壳失聪（壳卡快照对账永不再达）。isSubscribable 收**会话对象**
// （镜像 deviceSessionSubscribable 首查 conv?.deviceSession 的形状——评审
// verify 阶段 node 仿真抓过一版把裸清单条目喂给谓词、恒 false 零壳被订的惰性
// 接线）。返回可订阅壳的**会话对象**：订阅 id 取 deviceSession.id，子agent
// 刷新取 aiSessionId 优先（镜像 onclose 自愈口径）。
export function chatRecoverySubscribeSessionIds(convList, isSubscribable) {
  const targets = [];
  for (const conv of Array.isArray(convList) ? convList : []) {
    if (!conv?.deviceSession) continue;
    if (typeof isSubscribable === 'function' && !isSubscribable(conv)) continue;
    if (!String(conv.deviceSession.id || '').trim()) continue;
    targets.push(conv);
  }
  return targets;
}

// sweepApprovalKey resolved/cancelled 同型帧的全局扫卡（2026-11-09 归属漂移）：
// request 与 resolved 键恒一致但可落不同会话对象（③活跃兜底漂移/②壳三键抢道/
// ①草稿壳双对象），只清路由命中的那个会话会漏掉原卡；收卡幂等，对全部会话
// 扫同键一律收走。返回是否扫到（键位照封——迟到重复帧不复活）。
export function sweepApprovalKey(convList, sessionId, approvalId, handledApprovalIds) {
  if (!approvalId || !Array.isArray(convList)) return false;
  const key = `${String(sessionId || '')}:${String(approvalId)}`;
  let swept = false;
  for (const conv of convList) {
    const approvals = Array.isArray(conv?.approvals) ? conv.approvals : [];
    if (!approvals.some(item => item?.key === key)) continue;
    handledApprovalIds.add(key);
    conv.approvals = approvals.filter(item => item.key !== key);
    swept = true;
  }
  return swept;
}

// wireChatSocketRecovery 断线自愈的第二生命线（2026-11-09 家族⑤）：重连预算
// （2 次）耗尽后页面死寂——无定时重连/无轮询，断线窗口内错过的 resolved 帧
// 永不补达，DOM 卡片永不更新。visibilitychange（回到标签页）与 online（网络
// 恢复）是用户回归的两个确定性时机，各给一次恢复回调（预算重置与重订阅在
// recover 闭包内）。doc/win 注入便于测试；返回拆卸函数防 composable 卸载泄漏。
export function wireChatSocketRecovery(doc, win, recover) {
  if (!doc || !win || typeof recover !== 'function') return () => {};
  const onVisible = () => {
    if (doc.visibilityState === 'visible') recover();
  };
  const onOnline = () => recover();
  doc.addEventListener('visibilitychange', onVisible);
  win.addEventListener('online', onOnline);
  return () => {
    doc.removeEventListener('visibilitychange', onVisible);
    win.removeEventListener('online', onOnline);
  };
}

export function useAliangChat() {
  const { t } = useI18n();
  const { showDashboard, chatProviderPreset } = useNavigation();
  const { isAuthenticated } = useAuthStore();

  // 设备会话清单（跨端统一阶段 2）：后端三源会话的拉取/节流/壳缓存都收在
  // composable，ChatPage 只消费壳对象与 openDeviceSession 装载流程。
  const {
    deviceSessions,
    available: deviceAvailable,
    loadDeviceSessions,
    refreshOnEvent,
    shellForEntry,
    findDeviceShellBySessionId,
  } = useDeviceSessions();

  const conversations = ref([]);
  const activeId = ref(null);
  const inputText = ref('');
  const sending = ref(false);
  // 图片附件（spec 2026-11-05-chat-image-pipeline §3.6）：pending=已上传待随
  // ai.message 发出的引用（{ref,path,name,mime,bytes}）；uploading 期间禁发
  // （防载荷半截）。瞬态：不进 conversationForStorage。
  const pendingAttachments = ref([]);
  const attachmentUploading = ref(false);
  const messagesContainer = ref(null);
  // 滚动跟随守卫（Task 8）：用户贴底时流式增量照常拽底；一旦上翻看历史
  // （距底 ≥120px），delta/审批卡/系统条等自动滚动全部静默，改由「回到底部」
  // 浮钮承载。初始 true——发消息前用户必然在底部（e2e「发消息后内容可见」
  // 依赖该行为保真）。ChatMessageList 的 @scroll 经函数 prop 反向同步。
  const messagesNearBottom = ref(true);
  const inputRef = ref(null);
  const showMobileSidebar = ref(false);

  // --- 会话设置条（工作目录/模型/effort）状态 ---
  // createError 由帧路由（ai.error）与 sendMessage/断线收尾写入，留在本
  // composable；目录面板/自定义输入等纯 UI 状态已随 ChatSettingsBar 下沉。
  const createError = ref('');

  let idleWatchdogTimer = null;

  function armIdleWatchdog(conv) {
    disarmIdleWatchdog();
    idleWatchdogTimer = setTimeout(() => {
      idleWatchdogTimer = null;
      const asst = findRunningAssistant(conv);
      if (!asst) return;
      asst.status = 'error';
      if (!asst.content) asst.content = t('chat_idleTimeout');
      clearApprovalsForSession(conv, conv.aiSessionId); // 与 ai.done/ai.error 同口径
      finishRun(conv);
      saveToStorage();
    }, CHAT_IDLE_TIMEOUT_MS);
  }

  function disarmIdleWatchdog() {
    if (idleWatchdogTimer) {
      clearTimeout(idleWatchdogTimer);
      idleWatchdogTimer = null;
    }
  }

  // Local agent AI WebSocket: streams the local Claude Code / Codex headless run
  // token-by-token and carries the running -> done status transitions.
  let aiSocket = null;
  let aiSocketPromise = null;
  // 关窗守卫（后台静默运行，2026-11-04）：onUnmounted 主动 close 是预期生命周期
  // 而非断线——run 在 agent 侧照跑（后端 fanOut 对死 socket 仅忽略写错误，
  // journal 持续落盘），onclose 不得把 running 气泡画成「连接已中断」并持久化
  // （旧实现关窗即污染气泡，重进页满屏假中断）。重进页由
  // resubscribeRestoredRuns 的订阅快照对齐真值。
  let closingIntentionally = false;
  // 断线自愈重连计数（onopen 归零）：意外 close 先重连+重订阅对账，超限才
  // 回落「中断」画法——真正的 run 不随 socket 断而停，立即画错误是假中断。
  let reconnectAttempts = 0;
  const handledApprovalIds = new Set();
  // 作废/已作答的方案卡键位（镜像 handledApprovalIds 防御）：迟到重投的
  // ai.option.request 命中即弃，防已答卡被复活成 pending 邀请重复作答
  // （后端对二次作答本就静默忽略，这里保 UX 一致）。
  const handledOptionIds = new Set();
  // tool_ask 提问卡键位（2026-11-08 dashboard 渲染接入）：`session:interaction`
  // 封死已答/已作废卡，镜像 handledOptionIds（迟到重投不复活、作答不重复）。
  const handledToolAskIds = new Set();

  const activeConversation = computed(() =>
    conversations.value.find(c => c.id === activeId.value) || null
  );

  const activeMessages = computed(() =>
    activeConversation.value?.messages || []
  );

  const activeApprovals = computed(() =>
    (activeConversation.value?.approvals || []).filter(a => a.status === 'pending' || a.status === 'responding')
  );

  // 方案交互卡（Task 14）：pending/sending 展开作答，answered 折叠「已选择」
  // 终态常驻（作废即被移除）——与审批族数据结构独立，全量透传给列表渲染。
  const activeOptionCards = computed(() => activeConversation.value?.optionCards || []);
  // tool_ask 提问卡（mcp__aliang__ask_user 交互，2026-11-08）：与 optionCards
  // 同构瞬态（conversationForStorage 剥离、活过 run 终态等作答）。
  const activeToolAsks = computed(() => activeConversation.value?.toolAsks || []);

  // 子agent面板状态存储（spec 2026-11-07-subagent-panel §三）：会话维度
  // key=aiSessionId、value=该会话 subagents 数组（reducer applySubagentEvent
  // 的不可变更新结果）。瞬态展示数据：不进 localStorage——重进页由断线自愈
  // 的详情重拉（spec H3）与面板打开逻辑（Task 8）对齐。入口条/面板（Task 7/8）
  // 经下方 activeSubagents getter 或直接读本对象消费。
  const subagentsBySession = reactive({});
  // getter：当前打开会话的 subagents（无打开会话/会话无 aiSessionId 时为空数组）。
  const activeSubagents = computed(() => {
    const sid = activeConversation.value?.aiSessionId || '';
    return (sid && Array.isArray(subagentsBySession[sid]) ? subagentsBySession[sid] : []);
  });
  // 入口条「有无活动子agent」谓词（Task 7 渲染开关），reducer 同源实现。
  const activeHasRunningSubagents = computed(() => hasActiveSubagents(activeSubagents.value));

  // 「正在看该子agent详情」判定（spec M1 的 hook）：面板打开且选中某条目时才
  // 允许终态帧触发详情静默重拉。Task 8 面板选中态的落点（先声明占位 ref，
  // 接线后由面板选中逻辑写入：选中置 taskId，关闭/取消选中置 null）。
  const subagentWatchingTaskId = ref(null);
  // TODO(Task 8)：面板组件接线后此判定即生效；本任务先落占位实现（ref 恒
  // null ⇒ 恒 false，M1 重拉暂不触发，由面板打开逻辑兜底）。
  function isWatchingSubagent(taskId) {
    return !!taskId && subagentWatchingTaskId.value === taskId;
  }

  // M1 重拉入口（composable 侧薄壳）：绑定本会话存储交给模块级真实实现。
  function refetchSubagentsForSession(sessionId) {
    return refreshSubagentsFromDetail(subagentsBySession, sessionId);
  }

  // True while the active conversation's latest assistant message is still streaming.
  const activeRunning = computed(() => {
    const msgs = activeMessages.value;
    for (let i = msgs.length - 1; i >= 0; i--) {
      if (msgs[i].role === 'assistant') return msgs[i].status === 'running';
    }
    return false;
  });

  // --- 续聊发送分流（跨端统一阶段 2 Task 4） ---

  // 会话「running」判定:模块级三源并集(快照态 conv.running / run 窗口
  // conv.runActive / 本地气泡推断)——注释与实现见模块级 isConversationRunning。
  const activeSessionRunning = computed(() => isConversationRunning(activeConversation.value));

  // steer 目标（阶段 2 计划钉死 + 评审折叠项）：壳带 managed_conversation_id
  // （清单 running 绑定态带回）→ steer 该 id（真正的在跑回合）；agentcore 裸
  // id 壳无 managed id → 回退 entry.id（==托管通道会话 id，订阅/续聊同源，严格
  // 安全——消除清单刷新带回 managed id 前 ~2s 节流窗的误阻断）；claude_/codex_
  // 前缀壳无 managed id → 空（绝不发帧，也绝不引导「重发为普通消息」——那会对
  // 同一原生转录并发第二个 CLI run）；本地自跑会话 → 自身 aiSessionId。
  function steerTargetId(conv) {
    if (!conv) return '';
    if (conv.deviceSession) {
      const entry = conv.deviceSession;
      const managed = typeof entry.managed_conversation_id === 'string'
        ? entry.managed_conversation_id.trim()
        : '';
      if (managed) return managed;
      if (conv.provider === 'agentcore'
        && !isTerminalPrefixedSessionId(entry.id)
        && typeof entry.id === 'string' && entry.id) {
        return entry.id;
      }
      return '';
    }
    return conv.aiSessionId || '';
  }

  const activeSteerTarget = computed(() => steerTargetId(activeConversation.value));

  // 壳 running 且无 steer 目标（agentcore 裸 id 已回退 entry.id，只剩
  // claude_/codex_ 前缀壳等情形）：插话不可能（不发帧）——输入区常驻提示。
  const steerBlocked = computed(() =>
    activeSessionRunning.value
    && !!activeConversation.value?.deviceSession
    && !activeSteerTarget.value
  );

  // sending 守卫改造：running 会话在 sending 中仍可输入提交（插话），非 running
  // 维持禁用（本地 run 在途时输入框照旧锁住——此时 activeRunning=true 会放行，
  // 提交自然分流成 steer，这正是预期交互）。
  // 审批硬拦谓词单源：sendDisabled 与 ChatPage 的 :approval-blocked 接线共同
  // 消费——提示条与输入/发送禁用必须同真同假，别再各写一遍 length > 0。
  const sendApprovalBlocked = computed(() => activeApprovals.value.length > 0);

  // 审批等待期硬拦（Task 10，对齐手机端 spec §C 防顶翻）：活跃会话挂着
  // pending/responding 审批卡时，新消息/插话发送都会顶掉待审批、把会话打翻成
  // failed——所有 provider 一律禁发，直至卡被处理（响应/ai.done/cancelled 清卡）。
  // 停止键不受此拦（running+空输入仍可停——审批等待期停止是合法自救，走
  // ChatComposer 独立出口不经 sendDisabled）。仅拦活跃会话自身的审批（与
  // activeApprovals 口径一致，壳路由到他端会话的卡不拦本端输入）。
  const sendDisabled = computed(() =>
    (sending.value && !activeSessionRunning.value) || sendApprovalBlocked.value
    || attachmentUploading.value
  );

  // --- 撤回重发编辑态（Task 13，转录级真撤回） ---
  // recallState 非空且 convId 命中活跃会话即「编辑态」：banner 常驻、发送键位
  // 变确认重发、slash 补全禁用。originalText 回填输入框；draftBefore 是进入
  // 编辑态前的输入框草稿（cancel 时恢复；中途切换目标不覆盖——「进入前」以
  // 第一次进入为准）。瞬态：不进 conversationForStorage。
  const recallState = ref(null);

  const recallActive = computed(() => {
    const st = recallState.value;
    return !!st && !!activeConversation.value && st.convId === activeConversation.value.id;
  });

  const sendButtonLabel = computed(() => {
    // 撤回编辑态（Task 13）：键位文案换「确认重发」，动作经 Composer 的
    // recall-confirm 事件走 sendRecall（禁用语义与发送键同源 sendDisabled）。
    if (recallActive.value) return t('chat_recallConfirm');
    if (sending.value && activeSessionRunning.value) {
      return t('chat_steerSending');
    }
    return sending.value ? t('chat_sending') : t('chat_send');
  });

  const inputPlaceholder = computed(() => {
    // 仅在插话真正可达时换占位符提示（阻断态保持普通占位，提示条已另行说明）
    if (activeSessionRunning.value && activeSteerTarget.value) {
      return t('chat_steerPlaceholder');
    }
    return t('chat_placeholder');
  });

  const activeSteerItems = computed(() => activeConversation.value?.steerItems || []);

  // --- i18n 渲染辅助（跨端统一阶段 2 Task 6） ---

  // 系统条文案组装：③活跃兜底时带「来自会话 X」前缀（评审折叠项），无来源
  // 单行直出。sourceId 只存 id 切片（翻译在渲染时做，随语言切换）。
  function systemNoteContent(sourceId, text) {
    return sourceId ? `${t('chat_approvalFromSession', { id: sourceId })}: ${text}` : text;
  }

  // --- 三源统一清单（本地草稿 + 设备会话壳） ---

  const activeIsDevice = computed(() => !!activeConversation.value?.deviceSession);

  // 统一会话列表：本地草稿（无 deviceSession）+ 设备会话壳。壳对象由
  // composable 的 shellCache 保证稳定（打开后消息写在同一对象上）；打开过的
  // 设备会话也会插入 conversations（承载 activeConversation/sendMessage 既有
  // 路径），在这里去重——草稿过滤掉带 deviceSession 的成员，壳部分再补上
  // 「清单暂缺但已打开」的壳（agent 离线时代理回 200 空表，不能让打开着的
  // 会话从侧栏消失）。
  const mergedConversations = computed(() => {
    const drafts = [];
    const openedShells = [];
    for (const c of conversations.value) {
      if (c.deviceSession) openedShells.push(c);
      else drafts.push(c);
    }
    const listedIds = new Set();
    const shells = [];
    for (const entry of deviceSessions.value) {
      if (entry && typeof entry === 'object' && typeof entry.id === 'string') listedIds.add(entry.id);
      const shell = shellForEntry(entry);
      if (shell) shells.push(shell);
    }
    for (const shell of openedShells) {
      if (!listedIds.has(shell.deviceSession.id)) shells.push(shell);
    }
    // 侧栏双源去重（2026-11-06 侧栏双记录修复）：dashboard 会话的
    // journal 壳 aiSessionId=local-<convId> 与本地草稿同值——本地优先
    // 丢遮蔽壳，防「一条消息两条记录」（键缺失/未发送草稿不遮蔽）。
    const visibleShells = dropShadowedDeviceShells(drafts, shells);
    return [...drafts, ...visibleShells];
  });

  const conversationGroups = computed(() => [
    { key: 'today', label: t('chat_today'), items: groupedConversations.value.today },
    { key: 'yesterday', label: t('chat_yesterday'), items: groupedConversations.value.yesterday },
    { key: 'older', label: t('chat_older'), items: groupedConversations.value.older },
  ]);

  // --- 会话设置条逻辑 ---

  // 统一写入口：落字段 → 持久化 → 清错误 → 服务端原位同步（掉线时下条消息兜底）。
  // 设置条子组件经 apply-settings 事件落到这里（conv 取当前活跃会话，与拆分前
  // 各调用点 `activeConversation.value` 同源）。
  function applySessionSettings(conv, patch) {
    if (activeRunning.value) return;
    Object.assign(conv, patch);
    // effort/model 是「下一回合生效」配置：落值即挂「下轮生效」角标，直到
    // ai.run.started 路由回本会话才清（running 守卫已过 = 改动确实落值）。
    // 空串 effort（跟随默认）也是合法改动，同样触发。ai.session.created 的
    // 回显不走本入口，不会误挂角标。瞬态：conversationForStorage 剥离不落盘。
    if ('effort' in patch || 'model' in patch || 'approvalMode' in patch) conv.pendingConfig = true;
    conv.updatedAt = now();
    saveToStorage();
    createError.value = '';
    resendCreate(conv);
  }

  // 已建会话的设置变更要立即让服务端知道；socket 不在线就放弃本轮——
  // sendMessage 现在每条消息前都重发 create，天然兜底，无需脏标记。
  function resendCreate(conv) {
    if (!conv.aiSessionCreated) return;
    if (!aiSocket || aiSocket.readyState !== WebSocket.OPEN) return;
    sendAI(sessionCreatePayload(conv));
  }

  // 设置条「最近项目」数据通道：面板首开时装载（与侧栏设备清单共享同一次
  // /api/agent/sessions 请求的 promise 去重），返回清单原值给子组件做
  // recentProjectsFromSessions 归并（拆分前该逻辑在 ChatPage 内联）。
  async function loadRecentProjects() {
    await loadDeviceSessions();
    return deviceSessions.value;
  }

  // --- 输出预算 + 会话级 MCP/技能开关（master 并行线 ChatPage 原位迁入：
  //     数据与动作归 composable，展开态 moreSettingsOpen 留在 ChatSettingsBar）---

  // 输出预算面板草稿：max_tokens / context_window 的编辑草稿。
  const maxTokensDraft = ref('');
  const contextWindowDraft = ref('');
  // 会话级 MCP 开关：设备侧清单（name 即配置原始键）+ 本会话关掉的集合。
  // 清单只在首次展开面板时拉一次；拉不到就整块不渲染（不制造假开关）。
  const mcpServers = ref([]);
  const skills = ref([]);
  const mcpLoaded = ref(false);
  const mcpLoadError = ref('');

  // 展开面板时按需拉一次 MCP/技能清单（失败静默降级：只留错误行，整块不再假渲染）。
  async function ensureMcpServersLoaded() {
    if (mcpLoaded.value) return;
    mcpLoaded.value = true;
    try {
      const data = await getAgentIntegrations();
      mcpServers.value = Array.isArray(data?.mcp_servers) ? data.mcp_servers : [];
      skills.value = Array.isArray(data?.skills) ? data.skills : [];
      mcpLoadError.value = '';
    } catch (_) {
      mcpServers.value = [];
      mcpLoadError.value = t('chat_mcpLoadFailed');
    }
  }

  // 勾选/取消某个 MCP server：把整份「已关闭清单」整体写回会话（服务端按
  // 整列表替换），保证「全部重新打开」是显式空数组而不是缺键。
  function toggleMcpServer(name) {
    const conv = activeConversation.value;
    if (!conv || activeRunning.value) return;
    const key = String(name || '').trim();
    if (!key) return;
    const current = Array.isArray(conv.disabledMcpServers) ? conv.disabledMcpServers.slice() : [];
    const idx = current.indexOf(key);
    if (idx >= 0) current.splice(idx, 1);
    else current.push(key);
    applySessionSettings(conv, { disabledMcpServers: current });
  }

  // 勾选/取消某个技能：整份「已关闭清单」整体写回（与 MCP 同语义）。
  function toggleSkill(name) {
    const conv = activeConversation.value;
    if (!conv || activeRunning.value) return;
    const key = String(name || '').trim();
    if (!key) return;
    const current = Array.isArray(conv.disabledSkills) ? conv.disabledSkills.slice() : [];
    const idx = current.indexOf(key);
    if (idx >= 0) current.splice(idx, 1);
    else current.push(key);
    applySessionSettings(conv, { disabledSkills: current });
  }

  // 输出预算：空输入 = 清除（不下发），非数字/<=0 同样视为清除。
  function commitMaxTokens() {
    const conv = activeConversation.value;
    if (!conv || activeRunning.value) return;
    const raw = String(maxTokensDraft.value || '').trim();
    const n = Number(raw);
    if (!raw || !Number.isFinite(n) || n <= 0) {
      maxTokensDraft.value = '';
      applySessionSettings(conv, { maxTokens: 0 });
      return;
    }
    // 与后端 clampAgentAIMaxTokens 同区间，避免下发后被静默改值造成困惑。
    const clamped = Math.min(128000, Math.max(1024, Math.trunc(n)));
    maxTokensDraft.value = String(clamped);
    applySessionSettings(conv, { maxTokens: clamped });
  }

  function commitContextWindow() {
    const conv = activeConversation.value;
    if (!conv || activeRunning.value) return;
    const raw = String(contextWindowDraft.value || '').trim();
    const n = Number(raw);
    if (!raw || !Number.isFinite(n) || n <= 0) {
      contextWindowDraft.value = '';
      applySessionSettings(conv, { contextWindow: 0 });
      return;
    }
    const clamped = Math.min(1000000, Math.max(100000, Math.trunc(n)));
    contextWindowDraft.value = String(clamped);
    applySessionSettings(conv, { contextWindow: clamped });
  }

  // 输出预算草稿回填：留空表示「不下发（用内核默认）」，所以未设置时给空串
  // 而不是 0——0 在 number input 里会显示成 "0"，误导用户以为设了 0。
  function syncBudgetDrafts() {
    const conv = activeConversation.value;
    const mt = Number(conv && conv.maxTokens);
    const cw = Number(conv && conv.contextWindow);
    maxTokensDraft.value = Number.isFinite(mt) && mt > 0 ? String(mt) : '';
    contextWindowDraft.value = Number.isFinite(cw) && cw > 0 ? String(cw) : '';
  }

  // 切会话收起面板、清一次性错误；同时给切走的 agentcore 设备会话退订快照流
  // （幂等，socket 不在线时 sendAI 静默失败，重开会话会重新订阅）。
  // 目录面板/自定义模型/「更多」面板的 UI 复位在 ChatSettingsBar（watch
  // conversation prop）；预算草稿在数据侧，随会话切换在这里重同步。
  watch(activeId, (nv, ov) => {
    // 撤回编辑态随会话切换取消（Task 13）：恢复进入前草稿，banner 不跨会话悬挂。
    if (ov && recallState.value?.convId === ov) cancelRecall();
    if (ov) {
      const prev = conversations.value.find(c => c.id === ov);
      if (prev) unsubscribeDeviceSession(prev);
    }
    createError.value = '';
    syncBudgetDrafts();
    // 切会话即视为重新贴底：转录装载后的「看到最新一条」语义不被上一个
    // 会话里残留的上翻状态劫持（随后 scrollToBottom 走常规贴底路径）。
    messagesNearBottom.value = true;
  });

  // create 确认门：sendMessage 发出 create 后等回执再发 ai.message——目录无效
  // 时服务端拒绝建会话，绝不能让消息经 ai.message 的 lazy-create 落到 home 跑。
  // 回执来源：ai.session.created → {status:'created'}；create 期 ai.error（无
  // message_id）→ {status:'error', errorCode}（错误文本已由该分支渲染，errorCode
  // 透传帧上 e.error_code——后端 agentAIErrorPayload 对带码拒绝才落该字段）；
  // 超时/socket 死 → {status:'timeout'}（无 errorCode）。
  // payloadFn（Task 13，可选）：发出什么载荷由调用方决定——常规路径缺省
  // sessionCreatePayload(conv)，撤回路径传 recallCreatePayload 工厂（铁律：
  // 载荷必须真的经 sendAI 上帧，故工厂在发送点求值而非提前构造）。
  const pendingCreates = new Map(); // session_id -> settle(status, errorCode)

  function waitForCreateAck(conv, timeoutMs = 5000, payloadFn = null) {
    return new Promise((resolve) => {
      const sid = conv.aiSessionId;
      let done = false;
      const settle = (status, errorCode) => {
        if (done) return;
        done = true;
        pendingCreates.delete(sid);
        clearTimeout(timer);
        resolve({ status, errorCode });
      };
      const timer = setTimeout(() => settle('timeout'), timeoutMs);
      pendingCreates.set(sid, settle);
      conv.aiSessionCreated = true;
      // ensureAISocket 之后 socket 仍可能已死：sendAI 返回 false 就立刻按超时
      // 结算，不白等 5 秒死钟。
      if (!sendAI(payloadFn ? payloadFn() : sessionCreatePayload(conv))) settle('timeout');
    });
  }

  // 会话终态等待（Task 13 撤回链）：ai.stop 不是终态——回合的 ai.done/ai.error
  // 才算停稳，宿主在 run 未收敛时处理 truncate create 会以 truncate_run_active
  // 拒绝。镜像 pendingCreates 的一次性 settle 模式；settle 挂在帧路由上
  //（handleAIEvent 的终态早结算段，归属未命中也不让等待方空等），超时 settle
  // 'timeout'（调用方据此中止，保留编辑态）。
  const pendingTerminals = new Map(); // session_id -> settle(status)

  function waitForSessionTerminal(sessionId, timeoutMs = CHAT_STOP_TIMEOUT_MS) {
    return new Promise((resolve) => {
      const sid = String(sessionId || '');
      if (!sid) {
        resolve('timeout');
        return;
      }
      let done = false;
      const settle = (status) => {
        if (done) return;
        done = true;
        pendingTerminals.delete(sid);
        clearTimeout(timer);
        resolve(status);
      };
      const timer = setTimeout(() => settle('timeout'), timeoutMs);
      // 同 sid 二次 set 会把路由表项顶掉、首等待方从此无人结算只能空等死钟
      //（与 pendingCreates 同款隐患）。当前唯一调用方（撤回链）由 recalling
      // 单守不会并发；这里兜底撞键——立刻按 timeout 结算被顶掉的首等待方。
      pendingTerminals.get(sid)?.('timeout');
      pendingTerminals.set(sid, settle);
    });
  }

  // --- Slash command autocomplete (provider-aware) ---
  // The `/` popup is backed by slash.commands.list over the same AI stream socket
  // the chat already uses. Commands are cached per provider (claude vs codex) so a
  // claude conversation never surfaces codex's builtins and vice-versa.
  const slashCommandsByProvider = ref({});
  const slashOpen = ref(false);
  const slashIndex = ref(0);
  const slashPending = {};

  function slashProviderOf(conv) {
    return slashProviderFor(conv?.provider);
  }

  const slashActiveProvider = computed(() => slashProviderOf(activeConversation.value));

  // The in-progress command token (text after the leading `/`, before any space),
  // or null when the input is not a slash invocation. An empty string means the
  // user typed exactly `/` — show the full list.
  function slashToken(text) {
    if (!text.startsWith('/')) return null;
    const rest = text.slice(1);
    if (/\s/.test(rest)) return null; // args started -> stop suggesting
    return rest.toLowerCase();
  }

  const slashMatches = computed(() => {
    if (!slashOpen.value) return [];
    const q = slashToken(inputText.value);
    if (q === null) return [];
    const list = slashCommandsByProvider.value[slashActiveProvider.value] || [];
    const filtered = q === '' ? list : list.filter(c => String(c.name || '').toLowerCase().startsWith(q));
    return filtered.slice(0, 20);
  });

  function generateId() {
    return Date.now().toString(36) + Math.random().toString(36).slice(2, 8);
  }

  function now() {
    return Date.now();
  }

  const groupedConversations = computed(() => {
    const todayStart = new Date();
    todayStart.setHours(0, 0, 0, 0);
    const todayTs = todayStart.getTime();

    const yesterdayStart = new Date(todayStart);
    yesterdayStart.setDate(yesterdayStart.getDate() - 1);
    const yesterdayTs = yesterdayStart.getTime();

    const today = [];
    const yesterday = [];
    const older = [];

    // 三源统一清单排序：本地会话 updatedAt 与设备壳 updatedAt（composable 里
    // 已把 updated_at 归一成 epoch ms）同一口径。
    const sorted = [...mergedConversations.value].sort((a, b) => b.updatedAt - a.updatedAt);
    for (const c of sorted) {
      if (c.updatedAt >= todayTs) {
        today.push(c);
      } else if (c.updatedAt >= yesterdayTs) {
        yesterday.push(c);
      } else {
        older.push(c);
      }
    }
    return { today, yesterday, older };
  });

  function loadFromStorage() {
    try {
      const raw = localStorage.getItem(STORAGE_KEY);
      if (!raw) return;
      const data = JSON.parse(raw);
      if (data && Array.isArray(data.conversations)) {
        // 一次性清理历史残留：①设备会话壳（权威在后端，重进页重新派生）；
        // ②provider 非 AliangCode 的旧草稿（2026-10-30 前默认 claudecode 落过盘）——
        // 否则它们会以「本地草稿」身份绕过侧栏的清单过滤，继续在窗口里露脸。
        conversations.value = data.conversations
          .map(normalizeConversation)
          .filter((c) => c && !c.deviceSession && normalizeDeviceProvider(c.provider) === 'agentcore');
        activeId.value = data.activeConversationId || null;
        if (activeId.value && !conversations.value.some((c) => c.id === activeId.value)) {
          activeId.value = null;
        }
      }
    } catch (_) {
      // ignore corrupt data
    }
  }

  function saveToStorage() {
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify({
        // 设备会话壳不落 localStorage（权威在后端，重进页重新拉取派生）；
        // 活跃会话是设备壳时活跃指针一并置空——否则重进页会先落到一个
        // 本地不存在的会话 id 上（主区空壳、侧栏高亮），体验割裂。
        conversations: conversations.value.filter(c => !c.deviceSession).map(conversationForStorage),
        activeConversationId: conversations.value.some(c => c.id === activeId.value && !c.deviceSession)
          ? activeId.value
          : null
      }));
    } catch (_) {
      // storage full or unavailable
    }
  }

  function conversationForStorage(conv) {
    const copy = { ...conv };
    copy.approvals = [];
    // 方案交互卡同为瞬态（Task 14）：后端 run 结束才重发请求，跨页保留的
    // 「已选择」终态/待答卡只会误导（作答通道已随页面关闭而失联）。
    copy.optionCards = [];
    // tool_ask 提问卡同为瞬态（2026-11-08）：SLA 25min 内后端重发请求帧，
    // 跨页保留的待答卡只会误导（作答通道已随页面关闭而失联）。
    copy.toolAsks = [];
    // steer 轻量条是瞬态 UI：ack 状态不跨页持久（陈旧的「已受理」比没有更糟）
    copy.steerItems = [];
    // pendingConfig（「下轮生效」角标）同为瞬态：重载后「是否已开新回合」
    // 无从判定，跨页保留只会展示陈旧角标——与 approvals/steerItems 同剥。
    delete copy.pendingConfig;
    // snapshotCatchupAt（快照对账去重窗口起点）与 recoveredRun（恢复会话
    // 待终态回取快照标记）同为瞬态：跨页无意义，重进页 resubscribe 重建。
    delete copy.snapshotCatchupAt;
    delete copy.recoveredRun;
    // deviceLoadedAt（journal 详情装载时刻）：瞬态——跨页重开必须重新对账
    // （草稿↔journal reconcile 与设备壳装载共用此 TTL 键），持久化它会让
    // 重进页 5 分钟内跳过重建、继续展示过时的本地拼接稿。
    delete copy.deviceLoadedAt;
    // deviceTranscript 不落盘（评审折叠项）：重开设备会话有 5 分钟 TTL+详情
    // 分页重拉兜底，防大转录长期占 localStorage。
    delete copy.deviceTranscript;
    return copy;
  }

  function createConversation() {
    const conv = {
      id: generateId(),
      title: '',
      messages: [],
      approvals: [],
      optionCards: [],
      toolAsks: [],
      // 本窗口是 AliangCode 对话窗口（2026-10-30 用户拍板）：新建会话默认
      // agentcore；聊天窗只展示自家 agent 的对话，不该默认落成 claudecode。
      provider: chatProviderPreset.value || 'agentcore',
      // 默认模型留空（用户拍板 2026-10-31），但新建会话沿用「上一次用过的
      // 模型/档位」——否则每次新建都要重选，且空模型会被内核直接拒跑。
      model: readLastUsedModel(),
      effort: readLastUsedEffort(),
      // 输出预算默认不设（留空 = 内核默认 64000 / 200k），避免新建会话继承
      // 上一次的激进值而用户没意识到。
      maxTokens: 0,
      contextWindow: 0,
      disabledMcpServers: [],
      disabledSkills: [],
      createdAt: now(),
      updatedAt: now()
    };
    conversations.value.unshift(conv);
    activeId.value = conv.id;
    showMobileSidebar.value = false;
    saveToStorage();
    nextTick(() => inputRef.value?.focus());
  }

  // 侧栏点击分流：设备会话壳 → openDeviceSession（转录装载+订阅）；本地草稿
  // → journal 对账（若后端有同 id 会话）+ 原 selectConversation。
  function onSidebarItemClick(c) {
    if (c?.deviceSession) {
      openDeviceSession(c);
      return;
    }
    reconcileDraftWithJournal(c);
    selectConversation(c.id);
  }

  // 草稿↔journal 对账（2026-11-08 vm459 事故）：本地会话对象的 messages 是
  // 本端视图——手机端续聊的提问、附件、分回合回答都不会回流本端，旧拼接稿
  // 会永远遮蔽权威数据（该会话又因侧栏去重「草稿优先」拿不到 openDeviceSession
  // 的 journal 装载路径）。打开时命中同 id journal 会话即以 journal 全量重建
  // 历史（分页拉全同 openDeviceSession；身份字段/provider/审批配置不动，
  // running 中跳过——流式气泡保护，run 终态由既有快照对账接管）。TTL 语义
  // 与设备壳一致：deviceLoadedAt 是瞬态（conversationForStorage 剥离），
  // 跨页重开必重建。
  async function reconcileDraftWithJournal(conv) {
    if (!draftNeedsJournalReconcile(conv, deviceSessions.value)) return;
    const { transcript } = await fetchFullJournalTranscript(conv.aiSessionId, getAgentSessionDetail);
    if (!transcript.length) return;
    applyDeviceTranscript(conv, transcript);
    conv.deviceLoadedAt = now();
    saveToStorage();
  }

  function selectConversation(id) {
    activeId.value = id;
    showMobileSidebar.value = false;
    saveToStorage();
    nextTick(() => scrollToBottom());
  }

  // --- 会话重命名（仅本地标题，不同步 native / 服务端） ---
  const renamingId = ref('');
  // 'list' | 'header'：同一会话在列表与顶栏各有一个可编辑位点，
  // 必须靠它保证**同一时刻只渲染一个输入框**，否则键盘焦点会落到 DOM 里靠后的那个。
  const renameSurface = ref('');
  const renameDraft = ref('');
  // v-for 内的 ref 会收集成数组，故用函数 ref 只记住当前那一个输入框。
  // 拆分后输入框在 chat/ 子组件里，经 setRenameInput 函数 prop 把元素交回本
  // composable（保留原 `if (el)` 语义：卸载时的 null 不落值）。
  const renameInputEl = ref(null);

  function setRenameInput(el) {
    if (el) renameInputEl.value = el;
  }

  function startRename(conv, surface = 'list') {
    if (!conv?.id) return;
    // iOS Safari 点 <button> 不移动焦点，输入框收不到 blur，「失焦提交」不会触发；
    // 进新编辑位点前先主动提交上一个，避免共享的 renameDraft 冲掉在编文字。
    if (renamingId.value) commitRename();
    renamingId.value = conv.id;
    renameSurface.value = surface;
    renameDraft.value = conv.title || '';
    nextTick(() => {
      renameInputEl.value?.focus();
      renameInputEl.value?.select();
    });
  }

  function commitRename() {
    if (!renamingId.value) return;
    const conv = conversations.value.find(c => c.id === renamingId.value);
    if (conv) {
      conv.title = normalizeConversationTitle(renameDraft.value);
      saveToStorage(); // 刻意不动 updatedAt：改名不是内容更新，不该把会话顶到分组首位
    }
    cancelRename();
  }

  function cancelRename() {
    renamingId.value = '';
    renameSurface.value = '';
    renameDraft.value = '';
    renameInputEl.value = null;
  }

  function deleteConversation() {
    commitRename();
    if (!activeId.value) return;
    if (activeConversation.value?.deviceSession) return; // 设备会话不可在本端删（后端无端点）
    if (!confirm(t('chat_deleteConfirm'))) return;
    // 被删会话挂着撤回编辑态时一并清（recallActive 已随 conv 消失失活，但
    // 悬挂的 targetMessageId 会让下一条同 id 消息误配——直接清态）。
    if (recallState.value?.convId === activeId.value) recallState.value = null;
    conversations.value = conversations.value.filter(c => c.id !== activeId.value);
    if (conversations.value.length > 0) {
      activeId.value = conversations.value[0].id;
    } else {
      activeId.value = null;
    }
    saveToStorage();
  }

  // --- 打开设备会话（跨端统一阶段 2，Task 3） ---

  // 详情/快照转录 → 壳 messages（复用既有气泡渲染路径）；deviceTranscript
  // （[{role,content}] 原始形态，Task 4 的 agentcore create transcript 喂入
  // 消费——后端 remoteAgentAIHistory 按 row.role/row.content 解析）由
  // deviceDetailTranscriptToMessages 的 feed 供给。
  function applyDeviceTranscript(shell, transcript) {
    const rows = Array.isArray(transcript) ? transcript : [];
    const bubbles = [];
    const feed = [];
    for (const m of rows) {
      if (!m) continue;
      // 错误终态系统条（G5 问题悬空修复）：role=system 的行渲染居中弱化胶囊
      // （note=turn_failed），绝不进气泡对话流、绝不进续聊喂入流。
      if (m.role === 'system') {
        bubbles.push({
          role: 'assistant',
          kind: 'system',
          note: 'turn_failed',
          content: m.content || '',
          status: 'done',
          ts: Date.parse(m.timestamp) || now(),
        });
        continue;
      }
      if (m.role !== 'user' && m.role !== 'assistant') continue;
      const hasText = typeof m.content === 'string' && m.content;
      const hasAttachments = Array.isArray(m.attachments) && m.attachments.length > 0;
      if (!hasText && !hasAttachments) continue;
      bubbles.push({
        role: m.role,
        content: m.content || '',
        status: 'done',
        // 撤回锚点接线（Task 13）：详情 wire 行的 id 即 journal message_id，落
        // 气泡后可作 truncate_before_message_id；provider 落上（canRecall 依赖）。
        messageId: m.id || '',
        provider: shell.provider || '',
        ts: Date.parse(m.timestamp) || now(),
        // 图片附件（spec §3.6）：ref → 本机端点 URL，气泡缩略图分支渲染。
        ...(hasAttachments
          ? { attachments: m.attachments.map(a => ({ name: a.name || '', url: chatAttachmentUrl(a.ref) })) }
          : {}),
      });
      // 续聊喂入保持纯文本语义：空文本纯图行不进 deviceTranscript（本批次图片
      // 仅显示，不回灌内核历史）。
      if (hasText) feed.push({ role: m.role, content: m.content });
    }
    shell.messages = bubbles;
    shell.deviceTranscript = feed;
    scrollToBottom();
  }

  // 快照转录行（{role,text,message_id?,ts?}）→ 壳 messages（**仅展示流**）。
  // 职责分离（阶段 2 评审钉死）：快照是有损切片（200 行/4000 rune）且晚到会
  // 覆盖屏幕，但续聊喂入的 deviceTranscript 必须保持详情分页拉全的版本——快照
  // 绝不写 deviceTranscript，否则截断尾巴会在 create 时顶掉全量上下文（喂入即
  // 全部上下文）。
  function applyDeviceSnapshotTranscript(shell, rows) {
    const list = Array.isArray(rows) ? rows : [];
    const bubbles = [];
    for (const m of list) {
      if (!m) continue;
      // 错误终态系统条（G5）：同详情口径——胶囊展示，绝不进喂入流
      // （快照本就不写 deviceTranscript，双保险）。
      if (m.role === 'system') {
        bubbles.push({
          role: 'assistant',
          kind: 'system',
          note: 'turn_failed',
          content: m.text || '',
          status: 'done',
          ts: Date.parse(m.ts) || now(),
        });
        continue;
      }
      if (m.role !== 'user' && m.role !== 'assistant') continue;
      const hasText = typeof m.text === 'string' && m.text;
      const hasAttachments = Array.isArray(m.attachments) && m.attachments.length > 0;
      if (!hasText && !hasAttachments) continue;
      bubbles.push({
        role: m.role,
        content: m.text || '',
        status: 'done',
        // 快照 id 系 journal 同源（spec §A 已修订），可作撤回锚点；内容仍绝不
        // 喂入（deviceTranscript 专属流不变）。provider 落上（canRecall 依赖）。
        messageId: m.message_id || '',
        provider: shell.provider || '',
        ts: Date.parse(m.ts) || now(),
        ...(hasAttachments
          ? { attachments: m.attachments.map(a => ({ name: a.name || '', url: chatAttachmentUrl(a.ref) })) }
          : {}),
      });
    }
    shell.messages = bubbles;
  }

  // 订阅判定（钉死）：仅 agentcore 且裸 conversation id（journal 源才有转录
  // 快照）。claude_/codex_ 前缀不订阅（无 journal，详情已够）。
  function deviceSessionSubscribable(shell) {
    return !!shell?.deviceSession
      && shell.provider === 'agentcore'
      && !isTerminalPrefixedSessionId(shell.deviceSession.id);
  }

  // 打开设备会话：壳复用（composable 缓存的稳定对象）→ 分页拉全详情转录 →
  // agentcore 裸 id 发订阅等快照。404/网络错 → 空转录+错误条，不阻断续聊输入。

  // 详情转录分页参数（阶段 2 评审）：create 时的 transcript 喂入是续聊上下文
  // 的全部来源（载荷自带历史时后端跳过 journal 读回），默认 limit=40 的截断
  // 尾巴会让续聊静默丢早期上下文——必须拉全。单页 100 行、最多 5 页（500 行
  // 封顶）：防异常服务端拖死打开流程；超出封顶接受截断，仍远好于 40 行尾巴。
  const DEVICE_DETAIL_PAGE_LIMIT = 100;
  const DEVICE_DETAIL_MAX_PAGES = 5;

  async function openDeviceSession(shell) {
    const entry = shell?.deviceSession;
    if (!entry || typeof entry.id !== 'string' || !entry.id) return;
    if (!conversations.value.includes(shell)) {
      conversations.value.unshift(shell);
    }
    // 关键：壳对象来自 composable 的非响应式缓存，直接 unshift 进会话池后
    // 续写 raw 对象不会触发渲染——必须取会话池里的响应式代理来写后续字段。
    const live = conversations.value.find(c => c.id === shell.id) || shell;
    activeId.value = live.id;
    showMobileSidebar.value = false;
    saveToStorage();
    nextTick(() => scrollToBottom());

    // 详情装载：5 分钟内重复打开不重拉（之后的更新由快照覆盖**展示流**，
    // deviceTranscript 喂入流仍以这里的分页全量为准——见 applyDeviceSnapshotTranscript）
    if (Date.now() - (live.deviceLoadedAt || 0) > DEVICE_SESSION_DETAIL_TTL_MS) {
      const { transcript, lastDetail, pageError } = await fetchFullJournalTranscript(entry.id, getAgentSessionDetail);
      if (transcript.length) {
        live.deviceLoadedAt = now();
        live.deviceLoadError = '';
        if (pageError) console.warn('[aliang-chat] device transcript partial pages', pageError);
        applyDeviceTranscript(live, transcript);
      } else if (pageError) {
        // 空转录+错误条：详情缺席不阻断（本地会话池里壳已在，输入照常）。
        // 只存布尔标记——文案在模板渲染时走 i18n，不冻结赋值时的语言。
        live.deviceLoadError = true;
      }
      // 历史装配（spec 2026-11-07-subagent-panel H2）：详情响应顶层的
      // subagents[] 是会话级字段（每页同值），以最后一页响应装配一次——经
      // ingestDetailSubagents 与 M1 重拉同源归一入库，只进面板状态；转录装载
      // 走上面的 applyDeviceTranscript，subagents 绝不混进 messages。键取
      // aiSessionId（面板 getter 与 ai.subagent 帧守卫的同一把钥匙）。
      if (lastDetail) {
        ingestDetailSubagents(subagentsBySession, live.aiSessionId || entry.id, lastDetail);
      }
    }

    if (!deviceSessionSubscribable(shell)) return;
    try {
      await ensureAISocket();
      sendAI({ type: 'ai.session.subscribe', session_id: entry.id });
    } catch (_) {
      // socket 不可用：快照缺席不阻断（详情已渲染）
    }
  }

  // ai.session.snapshot（订阅握手回包）：按 session_id 找设备壳——先 aiSessionId
  // 匹配（打开过的壳），后三键兜底（entry.id/source_session_id/
  // managed_conversation_id）。未订阅/无壳的快照静默丢。
  // 2026-11-04 增补本地会话对账（reconcileLocalSnapshot）：窗口关闭期间 run
  // 照跑（journal 权威），重进页/断线重订阅的快照把恢复的 running 气泡收敛
  // 到真值——idle → done 收敛；running → 前缀延伸续流。
  function handleDeviceSnapshot(e) {
    const sessionId = String(e.session_id || '');
    if (!sessionId) return;
    // 本地草稿优先于壳查找：本地会话同样出现在设备清单（journal 三源扫描），
    // 壳查找若先命中同名 entry，快照会被壳分支吃掉、本地对账永远走不到
    // （2026-11-04 评审 P1：本地对账被设备壳清单劫持）。
    const local = conversations.value.find(c => !c.deviceSession && c.aiSessionId === sessionId);
    if (local) {
      reconcileLocalSnapshot(e, sessionId);
      return;
    }
    let shell = conversations.value.find(c => c.deviceSession && c.aiSessionId === sessionId)
      || findDeviceShellBySessionId(sessionId);
    if (!shell) return;
    // 兜底路径可能拿到 composable 缓存的 raw 壳：已在会话池里就换代理写值
    // （raw 写不触发渲染），不在池里（未打开）则保持原对象——下次打开时可见。
    shell = conversations.value.find(c => c.id === shell.id && c.deviceSession) || shell;

    // transcript 非空 → 覆盖壳 messages（快照是 journal 权威流的最新切片）
    const rows = Array.isArray(e.transcript) ? e.transcript : [];
    if (rows.length) {
      applyDeviceSnapshotTranscript(shell, rows);
      if (activeId.value === shell.id) scrollToBottom();
    }

    // pending_approvals 权威清卡（2026-11-09 家族③+④）+ 逐条 upsert：字段在
    // 场（新 agent 恒带，空集=权威无在挂）先按 sessionId 跨会话对象收走残留
    // 卡（漂到③活跃兜底/①双对象上的同会话卡一并裁决）；行缺 status 补
    // pending 默认（展开顺序：item 在前——行自带 status 时不得被默认值反客为主，
    // upsertApproval 只认 pending/responding，其余 status 走收卡）。
    reconcileSnapshotApprovals(conversations.value, e.pending_approvals, sessionId);
    for (const item of Array.isArray(e.pending_approvals) ? e.pending_approvals : []) {
      if (!item || typeof item !== 'object') continue;
      upsertApproval(shell, { ...item, status: item.status || 'pending' });
    }

    // status → 壳 running 标记（侧栏状态点消费）
    shell.running = e.status === 'running';
  }

  // 本地会话快照对账（薄壳）。核心语义已上提纯函数 reconcileLocalSnapshot-
  // Messages（回合分段 + 权威修复 + 外来回合补建；旧「归属锚定/前缀延伸/
  // 无气泡补建/恢复尾巴补全」四分支全部由其吸收，2026-11-09 vm22 事故族，
  // 修复律与竞态形态注释见该函数）。本壳只管 composable 侧副作用：在途守卫、
  // 看门狗武装、收敛结算、审批与权威态同步、落盘与贴底。
  function reconcileLocalSnapshot(e, sessionId) {
    const local = conversations.value.find(c => !c.deviceSession && c.aiSessionId === sessionId);
    if (!local) return;
    const rows = Array.isArray(e.transcript) ? e.transcript : [];
    const snapshotRunning = e.status === 'running';
    // 本端发送在途（create/ai.message 未落定）：run 可能刚要起跑，此刻的
    // idle 快照不是终态证据，绝不动消息列表（误收敛=假 done+delta 全丢）。
    const inFlight = local.id === activeId.value && sending.value;
    if (!inFlight && rows.length) {
      const res = reconcileLocalSnapshotMessages(local.messages, rows, {
        provider: local.provider || '',
        snapshotRunning,
        fallbackTs: now(),
      });
      if (res.changed) {
        local.messages = res.messages;
        if (res.alignedLive) local.snapshotCatchupAt = now(); // 扇出副本去重窗口起点（见 ai.delta）
        if (res.createdRunning) local.recoveredRun = true; // 补建运行态:后续 delta 只是增量,终态回取全文
        if (res.appended > 0 && local.id === activeId.value) scrollToBottom();
        saveToStorage();
      }
      if (!snapshotRunning) {
        // 定谳收敛：journal turn 行终态才齐，权威 idle 快照应用后恢复旗使命
        // 已尽（终态帧丢失的滞留 running 泡也已由纯内核关账）。
        local.recoveredRun = false;
        // 收敛结算镜像旧 done 分支：审批清卡（快照 pending_approvals 为空即
        // 权威「无在挂」）+ 看门狗解除 + finishRun——不落 finishRun 会把输入
        // 框永久锁死在 sendDisabled。
        if (!Array.isArray(e.pending_approvals) || e.pending_approvals.length === 0) {
          clearApprovalsForSession(local, sessionId);
        }
        if (local.id === activeId.value) disarmIdleWatchdog();
        finishRun(local);
      }
    }
    // running 快照证实 run 活着 → 看门狗再武装（onclose 的无条件 disarm 清了
    // 计时器）。只在「本地是活跃会话或当前无看门狗」时武装——单槽看门狗不能
    // 被后台对账从活跃会话手里抢走。
    if (snapshotRunning && !inFlight && (local.id === activeId.value || !idleWatchdogTimer)) {
      armIdleWatchdog(local);
    }
    // pending_approvals 恢复与状态分支解耦（审批等待中关窗重进：running
    // 快照同样带在挂审批，任何早退分支都不得丢）。字段在场时先权威清卡
    // （2026-11-09 家族③：running 态旧实现只 upsert 不清卡，别端已作答的卡
    // 永 pending；按 sessionId 跨对象扫，漂移卡同款裁决）；缺席（旧 agent）
    // 不越权。
    reconcileSnapshotApprovals(conversations.value, e.pending_approvals, sessionId);
    for (const item of Array.isArray(e.pending_approvals) ? e.pending_approvals : []) {
      if (!item || typeof item !== 'object') continue;
      upsertApproval(local, { ...item, status: item.status || 'pending' });
    }
  }

  // 切走设备会话时退订（幂等，多发无害）：agentcore 裸 id 才订过。
  function unsubscribeDeviceSession(shell) {
    if (!deviceSessionSubscribable(shell)) return;
    sendAI({ type: 'ai.session.unsubscribe', session_id: shell.deviceSession.id });
  }

  // 自动滚动改为贴底守卫版（Task 8）：默认仅在 messagesNearBottom 时执行；
  // 用户主动作（发消息/插话/点「回到底部」）传 force=true 无条件拽底——用户
  // 动作意味着「我要看新内容」，不回首翻滚前的阅读位置。
  function scrollToBottom(force = false) {
    if (!force && !messagesNearBottom.value) return;
    nextTick(() => {
      if (messagesContainer.value) {
        messagesContainer.value.scrollTop = messagesContainer.value.scrollHeight;
      }
    });
  }

  // 消息滚动容器在 ChatMessageList 里，经函数 prop 把元素交回（字符串 ref 语义：
  // 卸载时以 null 落值）。
  function setMessagesContainer(el) {
    messagesContainer.value = el;
  }

  // nearBottom 反向同步：滚动容器元素同样在 ChatMessageList，@scroll 几何判定
  // （距底 <120px 即贴底）后经本函数写回。与 setMessagesContainer 同一套
  // 函数-prop 交接模式。
  function setMessagesNearBottom(v) {
    messagesNearBottom.value = !!v;
  }

  // --- Local agent AI streaming over WebSocket ---

  function aiSocketUrl() {
    const proto = window.location.protocol === 'https:' ? 'wss' : 'ws';
    return `${proto}://${window.location.host}/api/agent/ai/stream`;
  }

  function ensureAISocket() {
    if (aiSocket && aiSocket.readyState === WebSocket.OPEN) return Promise.resolve();
    if (aiSocket && aiSocket.readyState === WebSocket.CONNECTING && aiSocketPromise) return aiSocketPromise;

    aiSocketPromise = new Promise((resolve, reject) => {
      let sock;
      try {
        sock = new WebSocket(aiSocketUrl());
      } catch (e) {
        aiSocketPromise = null;
        reject(e);
        return;
      }
      aiSocket = sock;
      sock.onopen = () => {
        console.log('[aliang-chat] ws open', aiSocketUrl());
        aiSocketPromise = null;
        // 刻意不在这里归零 reconnectAttempts：「连上就断」的离线环境（agent
        // 离线时浏览器侧照样先 Upgrade 再 close(1011)）每轮重连都会触发
        // open——若 open 即归零，重连预算永远清零、封顶永远到不了，退化成
        // 无限重连风暴（2026-11-04 e2e 实测）。归零点在 handleAIEvent：收到
        // 生产性入站帧才是「通道被证实健康」。
        resolve();
      };
      sock.onmessage = (ev) => {
        let parsed = null;
        try {
          parsed = JSON.parse(ev.data);
        } catch (_) {
          // ignore malformed frames
        }
        if (parsed && parsed.type) {
          console.log('[aliang-chat] frame', parsed.type, parsed.type === 'ai.delta' ? `len=${(parsed.delta || '').length}` : '');
        }
        handleAIEvent(parsed);
      };
      sock.onerror = (e) => {
        console.warn('[aliang-chat] ws error', e);
      };
      sock.onclose = (e) => {
        console.log('[aliang-chat] ws close code=', e.code, 'reason=', e.reason);
        if (aiSocket === sock) aiSocket = null;
        aiSocketPromise = null;
        // 陈旧 socket 的迟到 close（CLOSING→重连窗口）：aiSocket 已被新连接顶掉，
        // 别解新 run 的看门狗、别给新 run 画错误——让旧 close 静默退场。
        if (aiSocket && aiSocket !== sock) return;
        disarmIdleWatchdog();
        // 关窗卸载的主动 close 不是断线：run 在 agent 侧继续跑，重进页经订阅
        // 快照对齐终态（见 resubscribeRestoredRuns / handleDeviceSnapshot 的
        // 本地会话对账分支），这里绝不画「连接已中断」。
        if (closingIntentionally) return;
        // 受影响会话 = 有 running 气泡的（含后台会话）+ 活跃会话发送在途。
        const affected = conversations.value.filter(
          (c) => findRunningAssistant(c) || (c.id === activeId.value && sending.value)
        );
        if (!affected.length) return;
        // 断线自愈（后台静默运行）：先重连 + 重订阅拿快照对账——socket 断了
        // run 并没停，立即把 running 气泡画成 error 是假中断。重连失败（构造
        // 异常）或连上又立刻断（下轮 onclose 超限）才回落「中断」画法。
        // 800ms 退避：重连预算只防死线，不给学生式的瞬时抖动上强度。
        if (reconnectAttempts < 2) {
          reconnectAttempts += 1;
          setTimeout(() => {
            ensureAISocket()
              .then(() => {
                // 新 socket 的订阅集全灭（Go 侧订阅挂 socket 键，连接结束即
                // 整体摘除）——重订阅所有需要观察的会话：打开过的可订阅设备
                // 壳 + 有 running 气泡/发送在途的本地会话，不止 affected 子集
                // （已打开但无 running 气泡的壳不重订阅会静默失聪）。
                for (const c of conversations.value) {
                  if (c.deviceSession) {
                    if (deviceSessionSubscribable(c)) {
                      sendAI({ type: 'ai.session.subscribe', session_id: c.deviceSession.id });
                      // 断线自愈（spec 2026-11-07-subagent-panel H3）：重连成功
                      // 即对已订阅会话补一次子agent面板刷新——断连窗口内
                      // started/completed 的 ai.subagent 帧随 socket 全灭，TTL
                      // 过期前面板只能从详情恢复。无条件拉一次（重连是低频
                      // 事件）；in-flight 去重与静默失败都在
                      // refreshSubagentsFromDetail 内兜住。
                      refreshSubagentsFromDetail(subagentsBySession, c.aiSessionId || c.deviceSession.id);
                    }
                  } else if (c.aiSessionId
                    && (findRunningAssistant(c) || (c.id === activeId.value && sending.value))) {
                    sendAI({ type: 'ai.session.subscribe', session_id: c.aiSessionId });
                    refreshSubagentsFromDetail(subagentsBySession, c.aiSessionId);
                  }
                }
              })
              .catch(() => markRunsInterrupted(affected, e.code));
          }, 800);
          return;
        }
        markRunsInterrupted(affected, e.code);
      };
    });
    return aiSocketPromise;
  }

  function sendAI(payload) {
    if (aiSocket && aiSocket.readyState === WebSocket.OPEN) {
      aiSocket.send(JSON.stringify(payload));
      return true;
    }
    return false;
  }

  // 断线终态画法：仅重连自愈失败/超限后调用。只把「本端发送在途」的活跃
  // 会话画成中断——管道真断在我们自己的请求上，错误可操作（重发即重试）。
  // 其余 running 气泡（含后台会话/恢复态）保持原样：run 在 agent 侧的真值
  // 未知，running 是 last-known 状态，等下一次重连/重进页订阅快照对账——
  // 绝不自动落「连接已中断」假中断（本分支主诉求；旧实现关窗/断线即污染
  // 气泡并持久化）。
  function markRunsInterrupted(convList, closeCode) {
    for (const conv of convList) {
      const inFlight = conv.id === activeId.value && sending.value;
      const asst = findRunningAssistant(conv);
      if (asst && inFlight) {
        asst.status = 'error';
        if (!asst.content) {
          asst.content = closeCode === 1011 ? t('chat_agentOffline') : t('chat_connInterrupted');
        }
      }
      conv.aiSessionCreated = false; // 下条消息经确认门重发 create 重试
      // 离线语义下同步压住设置条的 serviceUnavailable（先设 createError，
      // 再结算确认门——早退分支的 `!createError.value` 兜底就不触发了）
      if (inFlight && !createError.value) {
        createError.value = closeCode === 1011 ? t('chat_agentOffline') : t('chat_connInterrupted');
      }
      const pending = conv.aiSessionId ? pendingCreates.get(conv.aiSessionId) : undefined;
      if (pending) pending('error'); // 别让确认门空等 5s 超时
      if (inFlight) finishRun(conv);
    }
    saveToStorage();
  }

  // Ask the agent for the active provider's `/`-command surface. Cached per
  // provider so we only round-trip once; a second call while pending is a no-op.
  async function ensureSlashCommands() {
    const provider = slashActiveProvider.value;
    // agentcore 内核没有 claude/codex 的插件命令面。绝不发空 provider 请求：
    // 后端 normalizeSlashProvider 把空值当「未指定」并返回全部 provider 的命令，
    // 正是要避免的误导。
    if (!provider) return;
    if (slashCommandsByProvider.value[provider] || slashPending[provider]) return;
    slashPending[provider] = true;
    try {
      await ensureAISocket();
      sendAI({
        type: 'slash.commands.list',
        request_id: 'slash-' + provider,
        project_path: activeConversation.value?.projectPath || '',
        provider,
      });
    } catch (_) {
      // socket unavailable: leave uncached so a later retry can fill it in
      slashPending[provider] = false;
    }
  }

  function onSlashInput() {
    // 撤回编辑态禁用 slash 补全（Task 13）：确认重发的输入语义是「改写后的
    // 消息正文」，`/` 开头按普通文本重发。
    if (recallActive.value) {
      slashOpen.value = false;
      return;
    }
    const token = slashToken(inputText.value);
    // 无命令面（agentcore）：`/` 按普通文本发送，不弹补全。
    if (token === null || !slashActiveProvider.value) {
      slashOpen.value = false;
      return;
    }
    ensureSlashCommands();
    slashOpen.value = true;
    slashIndex.value = 0;
  }

  function applySlash(cmd) {
    if (!cmd) return;
    const name = String(cmd.name || '').trim();
    if (!name) return;
    // Insert `/name ` and let the user append arguments. (For Codex the app-server
    // does not execute slash commands, so this is the most we can do — the token is
    // forwarded as text. For Claude the CLI likewise receives it as a prompt.)
    inputText.value = '/' + name + ' ';
    slashOpen.value = false;
    nextTick(() => inputRef.value?.focus());
  }

  // findRunningAssistant/ensureAssistantBubble 上提为模块级纯函数（2026-11-09
  // vm22 事故族：run 窗口感知/串行回合收敛需要独立单测）——此处不再有闭包版，
  // 引用解析到模块作用域。

  function normalizeConversation(conv) {
    if (!conv || typeof conv !== 'object') return null;
    if (!Array.isArray(conv.messages)) conv.messages = [];
    conv.approvals = [];
    // optionCards/toolAsks 从不落盘（storage 侧已剥离），此处兜底旧数据/异常形态。
    conv.optionCards = [];
    conv.toolAsks = [];
    return conv;
  }

  function approvalKeyFromValues(sessionId, approvalId) {
    return `${sessionId || ''}:${approvalId || ''}`;
  }

  function approvalKey(approval) {
    return approvalKeyFromValues(approval?.sessionId, approval?.id);
  }

  function ensureApprovalList(conv) {
    if (!Array.isArray(conv.approvals)) conv.approvals = [];
    return conv.approvals;
  }

  function removeApproval(conv, key) {
    if (!conv || !key || !Array.isArray(conv.approvals)) return;
    conv.approvals = conv.approvals.filter(item => item.key !== key);
  }

  function markApprovalHandled(conv, key) {
    if (!key) return;
    handledApprovalIds.add(key);
    removeApproval(conv, key);
  }

  function clearApprovalsForSession(conv, sessionId) {
    if (!conv || !Array.isArray(conv.approvals)) return;
    const keep = [];
    for (const item of conv.approvals) {
      if (!sessionId || item.sessionId === sessionId) {
        handledApprovalIds.add(item.key);
      } else {
        keep.push(item);
      }
    }
    conv.approvals = keep;
  }

  function upsertApproval(conv, event) {
    const id = event.approval_id || event.id;
    const sessionId = event.session_id || conv.aiSessionId || '';
    const key = approvalKeyFromValues(sessionId, id);
    if (!id || handledApprovalIds.has(key)) return;
    if (event.status && event.status !== 'pending') {
      markApprovalHandled(conv, key);
      return;
    }

    const approvals = ensureApprovalList(conv);
    const existing = approvals.find(item => item.key === key);
    const next = {
      ...(existing || {}),
      key,
      id,
      sessionId,
      messageId: event.message_id || existing?.messageId || '',
      provider: event.provider || existing?.provider || '',
      kind: event.kind || existing?.kind || 'tool',
      status: existing?.status === 'responding' ? 'responding' : 'pending',
      // ③兜底挂活跃会话时的来源 id 切片（「来自会话 X」的 X）：挂对壳/本端
      // 会话的卡无此字段。文案在渲染时经 chat_approvalFromSession 翻译。
      sourceId: event.sourceId || existing?.sourceId || '',
      title: event.title || existing?.title || '',
      reason: event.reason || existing?.reason || '',
      command: event.command || existing?.command || '',
      cwd: event.cwd || existing?.cwd || '',
      toolName: event.tool_name || event.toolName || existing?.toolName || '',
      toolInput: event.tool_input ?? event.toolInput ?? existing?.toolInput ?? null,
      fileChanges: event.file_changes ?? event.fileChanges ?? existing?.fileChanges ?? null,
      // 审批等待的绝对截止（RFC3339，pending 帧与快照行带、resolved 同型不带）：
      // 不存则模板倒计时永远渲染不出。已过/缺失由 approvalExpiresLabel 折叠为空。
      expiresAt: event.expires_at || existing?.expiresAt || '',
      availableDecisions: normalizeApprovalDecisions(event.available_decisions || event.availableDecisions || existing?.availableDecisions),
      error: '',
      updatedAt: now()
    };
    if (existing) {
      Object.assign(existing, next);
    } else {
      approvals.push(next);
    }
    conv.updatedAt = now();
    scrollToBottom();
  }

  // --- 审批卡 expires_at 倒计时（跨端统一阶段 2 Task 5） ---

  // 倒计时刷新时钟：30s 粒度足够（文案最小单位是分钟级「即将超时」）。
  const nowTick = ref(now());
  let nowTickTimer = null;
  // 断线自愈第二生命线的拆卸句柄（见 recoverChatSocketAfterInterruption）。
  let disposeChatSocketRecovery = null;

  async function respondApproval(conv, approval, decision) {
    if (!conv || !approval || approval.status !== 'pending') return;
    const normalized = normalizeApprovalDecision(decision);
    if (!normalized) return;

    const key = approvalKey(approval);
    if (handledApprovalIds.has(key)) {
      removeApproval(conv, key);
      return;
    }

    approval.status = 'responding';
    approval.error = '';
    approval.updatedAt = now();
    saveToStorage();

    try {
      await ensureAISocket();
      const ok = sendAI({
        type: 'ai.approval.response',
        session_id: approval.sessionId || conv.aiSessionId,
        message_id: approval.messageId || '',
        approval_id: approval.id,
        decision: normalized,
        scope: normalized === 'accept_for_session' ? 'session' : 'turn'
      });
      if (!ok) throw new Error('socket-closed');
      markApprovalHandled(conv, key);
      saveToStorage();
    } catch (_) {
      if (!handledApprovalIds.has(key)) {
        approval.status = 'pending';
        approval.error = t('chat_approvalSendFailed');
        approval.updatedAt = now();
        saveToStorage();
      }
    }
  }

  // --- 方案交互卡 ai.option（Task 14，与审批族同构但数据结构独立） ---
  // 协议：run 结束时后端从模型输出的 aliang-options 围栏提取发
  // ai.option.request（先于 ai.done 到达——ai.done 清审批但绝不清卡），作答
  // ai.option.response 续接新 run；ai.option.cancelled 作废（option_ids[]）。
  // optionCards 挂会话、瞬态（conversationForStorage 剥离，同 approvals）。

  function optionKeyFromValues(sessionId, optionId) {
    return `${sessionId || ''}:${optionId || ''}`;
  }

  // 选项行硬规范化（镜像后端 normalize：空 label 丢、id/label 字符串化）——
  // 帧来自模型产物清洗，逐项容错防坏行炸卡。
  function normalizeOptionChoices(raw) {
    const list = Array.isArray(raw) ? raw : [];
    const out = [];
    for (const item of list) {
      if (!item || typeof item !== 'object') continue;
      const id = String(item.id ?? '').trim();
      const label = String(item.label ?? '').trim();
      if (!id || !label) continue;
      out.push({
        id,
        label,
        description: typeof item.description === 'string' ? item.description : '',
        response: typeof item.response === 'string' ? item.response : '',
        previews: Array.isArray(item.previews) ? item.previews.map(p => String(p)) : [],
      });
    }
    return out;
  }

  function ensureOptionCards(conv) {
    if (!Array.isArray(conv.optionCards)) conv.optionCards = [];
    return conv.optionCards;
  }

  // --- tool_ask 提问卡（mcp__aliang__ask_user，2026-11-08 dashboard 渲染接入）---
  // 协议：agent 发 tool_ask.request{session_id, interaction_id, tool_name,
  // tool_input{title,options,allow_custom,multi}, expires_at(ms)}（审批族广播）；
  // 作答 tool_ask.response{session_id, interaction_id, status:'answered',
  // answer{selected_option_id?/selected_option_ids?/message?}}；作废
  // tool_ask.cancel{session_id, interaction_id(可空=批量), reason}。
  // 卡片数据结构与 optionCards 完全同形（渲染复用 ChatOptionCard），独立容器
  // 独立键位（interaction_id 名空间）；卡活过 run 终态等作答（同 option 卡）。

  function ensureToolAsks(conv) {
    if (!Array.isArray(conv.toolAsks)) conv.toolAsks = [];
    return conv.toolAsks;
  }

  function upsertToolAskCard(conv, e) {
    const interactionId = String(e.interaction_id || '').trim();
    if (!interactionId) return;
    const input = e.tool_input && typeof e.tool_input === 'object' ? e.tool_input : {};
    const sessionId = e.session_id || conv.aiSessionId || '';
    const key = optionKeyFromValues(sessionId, interactionId);
    // 镜像审批族防御：已作答/已作废键位的迟到重投帧直接丢弃（不复活卡）。
    if (handledToolAskIds.has(key)) return;
    const asks = ensureToolAsks(conv);
    const existing = asks.find(c => c.key === key);
    const choices = normalizeOptionChoices(input.options);
    const next = {
      key,
      interactionId,
      sessionId,
      messageId: e.message_id || existing?.messageId || '',
      provider: 'tool_ask',
      toolName: String(e.tool_name || existing?.toolName || ''),
      title: String(input.title || existing?.title || ''),
      header: '',
      summary: '',
      // 纯自由文本提问（无选项行）也合法：选项为空时卡片只剩 custom 输入。
      options: choices,
      allowCustom: input.allow_custom !== undefined ? !!input.allow_custom : true,
      multi: !!input.multi,
      expiresAt: Number(e.expires_at) || existing?.expiresAt || 0,
      // 同 key 重投（agent 投递重试）整体复位：旧选中/旧错误不作数。
      // 已作答键位已被 handledToolAskIds 封死，进不了这里。
      status: 'pending',
      selected: [],
      customText: '',
      error: '',
      // ③活跃兜底来源标（镜像审批族，渲染层经 chat_approvalFromSession 翻译）
      sourceId: e.sourceId || existing?.sourceId || '',
      updatedAt: now(),
    };
    if (existing) Object.assign(existing, next);
    else asks.push(next);
    scrollToBottom();
  }

  async function respondToolAsk(conv, card, selected, customText) {
    if (!conv || !card || card.status !== 'pending') return;
    const picked = (Array.isArray(selected) ? selected : []).map(v => String(v)).filter(Boolean);
    const custom = typeof customText === 'string' ? customText.trim() : '';
    // 选项卡至少选一项；纯自由文本卡（无选项行）允许只填 message。
    if (!picked.length && !custom) return;
    card.status = 'sending';
    card.error = '';
    card.updatedAt = now();
    try {
      await ensureAISocket();
      // 作答体契约（PhoneServer zod 同构）：单选 selected_option_id、multi
      // selected_option_ids、自由文本 message；并存合法（agent 端逐一消费）。
      const answer = {};
      if (card.multi && picked.length) answer.selected_option_ids = picked;
      else if (picked.length) answer.selected_option_id = picked[0];
      if (custom) answer.message = custom;
      const ok = sendAI({
        type: 'tool_ask.response',
        session_id: card.sessionId || conv.aiSessionId,
        interaction_id: card.interactionId,
        status: 'answered',
        answer,
      });
      if (!ok) throw new Error('socket-closed');
      card.status = 'answered';
      card.selected = picked;
      card.customText = custom;
      card.updatedAt = now();
      // 作答落定即封死键位（镜像 respondOption）：此后同 key 的迟到重投帧
      // 不再复活本卡（agent pop-on-delivery 幂等，二次帧静默忽略）。
      handledToolAskIds.add(card.key);
    } catch (_) {
      card.status = 'pending';
      card.error = t('chat_toolAskSendFailed');
      card.updatedAt = now();
    }
  }

  function upsertOptionCard(conv, e) {
    const optionId = String(e.option_id || '').trim();
    const choices = normalizeOptionChoices(e.options);
    if (!optionId || !choices.length) return;
    const sessionId = e.session_id || conv.aiSessionId || '';
    const key = optionKeyFromValues(sessionId, optionId);
    // 镜像审批族防御：已作答/已作废键位的迟到重投帧直接丢弃（不复活卡）。
    if (handledOptionIds.has(key)) return;
    const cards = ensureOptionCards(conv);
    const existing = cards.find(c => c.key === key);
    const next = {
      key,
      optionId,
      sessionId,
      messageId: e.message_id || existing?.messageId || '',
      provider: e.provider || existing?.provider || '',
      title: e.title || existing?.title || '',
      header: e.header || existing?.header || '',
      summary: e.summary || existing?.summary || '',
      options: choices,
      allowCustom: !!e.allow_custom,
      multi: !!e.multi,
      // 同 key 新请求（新 run 重发方案）整体复位：旧选中/旧错误不作数。
      // 已作答（answered）键位已被 handledOptionIds 封死，进不了这里。
      status: 'pending',
      selected: [],
      customText: '',
      error: '',
      // ③活跃兜底来源标（镜像审批族，渲染层经 chat_approvalFromSession 翻译）
      sourceId: e.sourceId || existing?.sourceId || '',
      updatedAt: now(),
    };
    if (existing) Object.assign(existing, next);
    else cards.push(next);
    conv.updatedAt = now();
    scrollToBottom();
  }

  // 作答：裸 sendAI（与 respondApproval 同构，不进 create 确认门——作答续接
  // 的是既有会话）。成功置 answered+记录选择（卡折叠终态），失败恢复 pending
  // +错误文案（保留已选可直接重试）。optionCards 不落盘，无需 saveToStorage。
  async function respondOption(conv, card, selected, customText) {
    if (!conv || !card || card.status !== 'pending') return;
    const picked = (Array.isArray(selected) ? selected : []).map(v => String(v)).filter(Boolean);
    if (!picked.length) return;
    const custom = typeof customText === 'string' ? customText.trim() : '';
    card.status = 'sending';
    card.error = '';
    card.updatedAt = now();
    try {
      await ensureAISocket();
      const ok = sendAI({
        type: 'ai.option.response',
        session_id: card.sessionId || conv.aiSessionId,
        option_id: card.optionId,
        selected: picked,
        custom_text: custom,
      });
      if (!ok) throw new Error('socket-closed');
      card.status = 'answered';
      card.selected = picked;
      card.customText = custom;
      card.updatedAt = now();
      // 作答落定即封死键位（镜像 markApprovalHandled）：此后同 key 的迟到
      // 重投 request 帧不再复活本卡（后端对二次作答静默忽略）。
      handledOptionIds.add(card.key);
    } catch (_) {
      card.status = 'pending';
      card.error = t('chat_optionSendFailed');
      card.updatedAt = now();
    }
  }

  function finishRun(conv) {
    conv.updatedAt = now();
    if (activeId.value === conv.id) sending.value = false;
    saveToStorage();
  }

  // ai.run.notice code → 用户文案（Task 5）；未知 code 返回空（调用方忽略）。
  function agentRunNoticeText(code) {
    switch (code) {
      case 'resume_missing':
        return t('chat_noticeResumeMissing');
      case 'history_restore_failed':
        return t('chat_noticeHistoryRestoreFailed');
      case 'tool_timeout':
        return t('chat_noticeToolTimeout');
      default:
        return '';
    }
  }

  // --- 图片附件（spec 2026-11-05-chat-image-pipeline §3.6） ---

  // 客户端预检与 agent 侧对齐：≤8 张/消息、单张 ≤10MiB、仅 jpeg/png/gif/webp。
  // 错误走 createError 面板（下次成功发送清除）；上传串行进行防乱序。
  const ATTACH_IMAGE_EXT = /\.(jpe?g|png|gif|webp)$/i;
  function addPendingAttachments(files) {
    const list = Array.from(files || []);
    if (list.length === 0) return;
    const conv = activeConversation.value;
    if (!conv) return;
    for (const file of list) {
      if (!ATTACH_IMAGE_EXT.test(file.name)) {
        createError.value = t('chat_attachUnsupported');
        return;
      }
      if (file.size > 10 * 1024 * 1024) {
        createError.value = t('chat_attachTooLarge');
        return;
      }
    }
    if (pendingAttachments.value.length + list.length > 8) {
      createError.value = t('chat_attachTooMany');
      return;
    }
    const sessionId = conv.aiSessionId || ('local-' + conv.id);
    attachmentUploading.value = true;
    (async () => {
      try {
        for (const file of list) {
          const attachment = await uploadChatAttachment(sessionId, file);
          pendingAttachments.value.push(attachment);
        }
        createError.value = '';
      } catch (err) {
        createError.value = (err && err.message) || t('chat_attachUploadFailed');
      } finally {
        attachmentUploading.value = false;
      }
    })();
  }

  function removePendingAttachment(refId) {
    pendingAttachments.value = pendingAttachments.value.filter(a => a.ref !== refId);
  }

  function clearPendingAttachments() {
    pendingAttachments.value = [];
  }

  // --- steer 分流（跨端统一阶段 2 Task 4） ---
  // 运行中回合的旁路注入：不插 user 气泡、不走 create 确认门、不武装看门狗、
  // 不动 sending——轻量条自己承载状态（sent → ack 落定）。目标为空（claude_/
  // codex_ 壳 running 且无 managed id）时静默返回：阻断提示条已常驻输入区，
  // 绝不做「重发为普通消息」引导（会对同一原生转录并发第二个 CLI run）。
  async function submitSteer(conv, text) {
    const target = steerTargetId(conv);
    if (!target) return;
    if (!Array.isArray(conv.steerItems)) conv.steerItems = [];
    const item = {
      messageId: 'steer-' + generateId(),
      content: text,
      status: 'sent',
      result: '',
      detail: '',
      ts: now(),
    };
    conv.steerItems.push(item);
    inputText.value = '';
    // 用户主动作：无条件贴底（上翻状态不劫持用户刚发出的插话）。
    scrollToBottom(true);
    try {
      await ensureAISocket();
      const ok = sendAI({ type: 'ai.steer', session_id: target, message_id: item.messageId, content: text });
      if (!ok) throw new Error('socket-closed');
    } catch (_) {
      item.status = 'failed';
      item.detail = t('chat_serviceUnavailable');
    }
  }

  function handleSteerAck(e) {
    const mid = String(e.message_id || '');
    if (!mid) return;
    for (const c of conversations.value) {
      const items = Array.isArray(c.steerItems) ? c.steerItems : [];
      const item = items.find(it => it.messageId === mid);
      if (!item) continue;
      applySteerAck(item, e);
      refreshOnEvent(); // 插话落定（not_running 等）可能伴随清单 running 态翻转，节流刷新侧栏
      return;
    }
    console.debug('[chat] steer ack has no matching item, dropped', mid);
  }

  // queued/applied/not_running 落各自状态；error/unsupported 是契约内失败值
  // （失败渲染 + detail）；其余未知 result（含未来新增值）中性渲染 `插话状态：x`，
  // 不误标失败（评审折叠项）。
  function applySteerAck(item, e) {
    const result = String(e.result || '');
    item.result = result;
    if (result === 'queued' || result === 'applied' || result === 'not_running') {
      item.status = result;
      item.detail = '';
      return;
    }
    if (result === 'error' || result === 'unsupported') {
      item.status = 'failed';
      item.detail = String(e.error || e.code || result);
      return;
    }
    item.status = 'unknown';
    item.detail = '';
  }

  // --- 撤回重发：编辑态进出与发送链（Task 13） ---

  // 进入/切换编辑态：气泡 hover 菜单经 ChatPage 落到这里。canRecall + 目标仍
  // 在会话消息里（与发送链同源的切片判定，目标不存在=禁用入口）双重把关。
  // 已在编辑态时再点（同条或另一条）→ 直接重置目标与回填。
  function startRecall(msg) {
    const conv = activeConversation.value;
    if (!conv || recalling.value) return;
    // 拦截 idle 发送窗：sending 中且会话非 running（占位符未落/已撤的窗口）。
    // 注意常规发送的 create 确认门窗口**不**在此列——占位符在等 ack 前已推
    // （running=true），该窗口本守卫不拦；running 态撤回必须可用（running 中
    // 发送/撤回并存是常态），故对 running 放行——并发由 recalling 单守。
    if (sending.value && !isConversationRunning(conv)) return;
    if (!canRecall(msg)) return;
    if (!sliceMessagesBefore(conv.messages, msg.messageId)) return;
    const prev = recallState.value;
    recallState.value = {
      convId: conv.id,
      targetMessageId: msg.messageId,
      originalText: msg.content || '',
      draftBefore: prev ? prev.draftBefore : inputText.value,
    };
    inputText.value = recallState.value.originalText;
    slashOpen.value = false;
    nextTick(() => inputRef.value?.focus());
  }

  // 取消编辑态：恢复进入前草稿、清态。仅在状态属于当前活跃会话或会话切换
  //（watch activeId）时被调用，恢复草稿总是安全的（状态不会跨会话悬挂）。
  function cancelRecall() {
    const st = recallState.value;
    if (!st) return;
    recallState.value = null;
    inputText.value = st.draftBefore || '';
  }

  // 撤回重发发送链（与 sendMessage 尾段同构）：
  //   running → ai.stop（目标与 steer 同构）→ 等会话终态（超时中止：保留编辑态
  //   + createError，绝不把 truncate create 发进仍在跑的回合）
  //   → create 确认门（recallCreatePayload：无 transcript/无 resume + truncate 键，
  //   宿主从 journal 全文重建；宿主侧按 beforeMessageID 位置幂等 → 超时可重试）
  //   → ack created → 本地时间线截到目标之前 + 新 user 气泡（新 messageId，
  //   provider 落上——canRecall 依赖）+ assistant 占位 → 看门狗 + ai.message。
  // create 超时/被拒：不发 ai.message，编辑态保留（createError 落可重试文案）。
  // 取消（cancelRecall 清 recallState）= 中止在途链：链内每个 await 后复验
  // 身份，失配即静默收尾——不发 ai.message、不动输入框（草稿由 cancelRecall
  // 恢复）；recalling 由 finally 链尾复位（C1：成功路径曾漏复位，首次撤回后
  // startRecall 守卫使入口永久哑火）。
  const RECALL_CREATE_ACK_MS = 5000;
  // 撤回 create 被拒的 error_code → 专属文案键：后端 ai.session.create truncate
  // 四成因（agent_ai.go truncateErrCode*，经 agentAIErrorPayload 的 ErrorCode()
  // 落帧 error_code），句柄是英文技术语，直译给用户没有行动指向——按成因映射
  // 成可操作的中文/英文文案；未知/缺 code（含 timeout，无帧）回落
  // chat_recallUnconfirmed 的「未确认可重试」。
  const RECALL_CREATE_ERROR_KEYS = {
    truncate_run_active: 'chat_recallErrActive',
    truncate_journal_unavailable: 'chat_recallErrNoJournal',
    truncate_failed: 'chat_recallErrFailed',
    truncate_rebuild_failed: 'chat_recallErrRebuild',
  };
  const recalling = ref(false);

  async function sendRecall(conv) {
    const st = recallState.value;
    if (!st || !conv || conv.id !== st.convId || recalling.value) return;
    // 设备壳撤回禁用（spec §6）：壳续聊 create 落新建的 local-* journal，而壳
    // 气泡锚点 id 来自原 entry.id journal——truncate 未命中是 no-op 非报错，
    // UI 假成功、上下文静默分叉。渲染层（ChatMessageList recallEnabled）已收掉
    // 入口，这里兜底防绕过；待服务端跨 journal 锚点解析后解锁。
    if (conv.deviceSession) return;
    const edited = inputText.value.trim();
    if (!edited) return; // 空文本不允许重发（与 sendMessage trim 守卫同口径）
    if (!sliceMessagesBefore(conv.messages, st.targetMessageId)) {
      cancelRecall();
      return;
    }

    recalling.value = true;
    sending.value = true;
    createError.value = '';
    // resent：step③ 的 ai.message 是否已发出——catch 据此分流兜底文案（未发=
    // 链前段失败、编辑态仍在可重试；已发=journal 已截/新气泡已落/编辑态已清，
    // 「未确认可重试」文案错位，改走 sendMessage catch 同款口径）。
    // aborted：链因取消在 await 点中止——finally 据此做编辑态兜底（见 finally）。
    let resent = false;
    let aborted = false;
    try {
      await ensureAISocket();
      // I2 复验（每个 await 后）：取消=中止在途链。cancelRecall（用户取消/切
      // 会话）清走 recallState——失配即静默收尾，后续动作一概不发、输入框不
      // 动（草稿已由 cancelRecall 恢复）；sending 就地复位（未发出回合，终态
      // 帧不会来），recalling 归 finally。
      if (recallState.value !== st) {
        aborted = true;
        sending.value = false;
        return;
      }

      // ① running 先停。stop 目标与 steer 同构（本地 aiSessionId / 壳 managed
      //    ?? entry.id）；agentcore 撤回链的壳必回退 entry.id，空目标理论不可达，
      //    真发生按可重试失败中止。
      if (isConversationRunning(conv)) {
        const target = steerTargetId(conv);
        if (!target || !sendAI({ type: 'ai.stop', session_id: target })) {
          throw new Error('socket-closed');
        }
        const terminal = await waitForSessionTerminal(target);
        if (recallState.value !== st) {
          aborted = true;
          sending.value = false;
          return; // 取消中止（同 ensureAISocket 后复验）
        }
        if (terminal === 'timeout') {
          // 中止：编辑态与回填保留，绝不发 truncate create（场景 E 钉死）。
          createError.value = t('chat_recallStopTimeout');
          sending.value = false; // recalling 复位归 finally；早退分支 sending 自理
          return;
        }
      }

      // ② create 确认门。载荷工厂在发送点求值——铁律：撤回 create 绝不带
      //    transcript（宿主从 journal 全文重建），也绝不带 resume_session_id。
      if (!conv.aiSessionId) conv.aiSessionId = 'local-' + conv.id;
      const ack = await waitForCreateAck(
        conv,
        RECALL_CREATE_ACK_MS,
        () => recallCreatePayload(conv, st.targetMessageId),
      );
      if (recallState.value !== st) {
        // 取消中止：truncate create 帧可能已发出（宿主侧按位置幂等），但本地
        // 截断与 ai.message 一概不发——「用取消前捕获的文本推气泡」在此掐断。
        aborted = true;
        sending.value = false;
        return;
      }
      if (ack.status !== 'created') {
        createError.value = t(RECALL_CREATE_ERROR_KEYS[ack.errorCode] || 'chat_recallUnconfirmed');
        sending.value = false; // recalling 复位归 finally
        return; // 编辑态保留；truncate 幂等，原样重试安全
      }

      // ③ ack created → 本地截断 + 追加（重切一次：stop 收敛期可能新落了系统
      //    条，截断点之后的内容一律弃置，与 journal 截断口径一致）。
      const before = sliceMessagesBefore(conv.messages, st.targetMessageId);
      if (!before) {
        cancelRecall();
        sending.value = false; // recalling 复位归 finally
        return;
      }
      const messageId = generateId(); // 重发必换新 id（journal 去重）
      conv.messages = [
        ...before,
        { role: 'user', content: edited, messageId, provider: conv.provider, ts: now() },
        { role: 'assistant', content: '', status: 'running', ts: now() },
      ];
      if (!conv.title) conv.title = edited.slice(0, 20);
      conv.updatedAt = now();
      recallState.value = null;
      inputText.value = '';
      scrollToBottom(true);
      saveToStorage();

      armIdleWatchdog(conv);
      resent = true; // ai.message 发出前置位：此后失败属「已撤回、重发未达」
      const ok = sendAI({
        type: 'ai.message',
        session_id: conv.aiSessionId,
        message_id: messageId,
        content: edited,
      });
      if (!ok) throw new Error('socket-closed');
    } catch (_) {
      disarmIdleWatchdog();
      const asst = findRunningAssistant(conv);
      if (asst) {
        asst.status = 'error';
        asst.content = t('chat_serviceUnavailable');
      }
      if (resent) {
        // step③ 已走完（journal 已截、新气泡已落、编辑态已清）——落「撤回未
        // 确认可重试」会指向不存在的重试入口；改与 sendMessage catch 同款：
        // 服务不可用兜底 + 同款守卫（仅当前会话且无既有错误时落）。
        if (conv.id === activeId.value && !createError.value) {
          createError.value = t('chat_serviceUnavailable');
        }
      } else {
        // 链前段失败（socket 建连/ai.stop 发不出）：编辑态保留，可原样重试。
        createError.value = t('chat_recallUnconfirmed');
      }
      sending.value = false;
      saveToStorage();
    } finally {
      // C1：recalling 是链域标志，链尾（成功/失败/取消中止）必须复位——原缺陷
      // 成功路径不复位，首次撤回后 startRecall 守卫（recalling=true）使撤回
      // 入口永久哑火。sending 不在此动：回合域标志由终态帧（finishRun）清理，
      // 未发出回合的分支已就地自理。
      recalling.value = false;
      // I2 联动兜底：取消中止时编辑态本已被 cancelRecall 清走；若链因取消中止
      // 而 recallState 竟仍挂本链 st（未来未经 cancelRecall 的取消路径），清掉
      // 防悬挂。可重试失败路径（停止超时/create 未确认）不在此列——编辑态
      // 保留是场景 E 钉死语义，finally 不得误伤。
      if (aborted && recallState.value === st) {
        recallState.value = null;
      }
    }
  }

  // --- 停止当前运行（Task 9，动作槽对齐手机端） ---
  // running 时可停：stop 帧不是终态——后端只回 ai.status "stopping" + 取消
  // 审批，终态由被取消 run 的 done/error 既有路径收敛（这里不新增等待/超时
  // 逻辑，视觉保持 running 直到终态帧到达；终态到达前停止键仍可点，重复
  // stop 帧幂等无害）。目标 id 与 steer 同构（steerTargetId）：本地会话
  // aiSessionId / 设备壳 managed_conversation_id ?? entry.id；空目标（claude_/
  // codex_ 壳 running 且无 managed id）静默返回——停止本就无的放矢。
  async function stopRun() {
    const conv = activeConversation.value;
    const target = steerTargetId(conv);
    if (!target) return;
    try {
      await ensureAISocket();
      const ok = sendAI({ type: 'ai.stop', session_id: target });
      if (!ok) throw new Error('socket-closed');
    } catch (_) {
      // 既有错误呈现口径：设置条内联错误（socket 不可用时不静默；下次发送自清）。
      // !createError.value 防覆盖：帧路由（ai.error 等）已落的错误不被兜底文案
      // 顶掉（对齐 sendMessage 确认门早退分支同款守卫）。
      if (conv && conv.id === activeId.value && !createError.value) {
        createError.value = t('chat_serviceUnavailable');
      }
    }
  }

  function handleAIEvent(e) {
    if (!e || !e.type) return;
    // run 数据面入站帧证明管道活着：重连预算归零（纯快照握手不算——防
    // 「连上→回一帧→断」假活环境无限洗白预算；长跑 run 的偶发断线在健康
    // 恢复后靠 delta/终态帧重新拿满预算，不会被累计封顶误伤）。
    if (AGENT_AI_RECONNECT_RESET_TYPES.has(e.type)) reconnectAttempts = 0;

    // Slash-command discovery results are not tied to a conversation session; cache
    // them by provider before the conversation-targeting logic below.
    if (e.type === 'slash.commands.list.result') {
      const provider = e.provider === 'codex' ? 'codex' : 'claude';
      slashPending[provider] = false;
      slashCommandsByProvider.value = {
        ...slashCommandsByProvider.value,
        [provider]: Array.isArray(e.commands) ? e.commands : [],
      };
      return;
    }
    if (e.type === 'slash.commands.list.error') {
      const provider = e.provider === 'codex' ? 'codex' : 'claude';
      slashPending[provider] = false;
      slashCommandsByProvider.value = { ...slashCommandsByProvider.value, [provider]: [] };
      return;
    }

    // 设备会话快照（订阅握手回包）：必须在通用会话归属判定**之前**接住——
    // 快照可能属于未打开的设备会话，绝不能落进「未命中挂活跃会话」的
    // fallback 链（会把他会转录灌进当前会话）。
    if (e.type === 'ai.session.snapshot') {
      handleDeviceSnapshot(e);
      return;
    }

    // steer ack：按 message_id 反查提交方会话的轻量条更新。必须在通用会话归属
    // 判定之前接住——ack 的 session_id 是 steer 目标（managed_conversation_id
    // 等），按 aiSessionId 匹配不上时会走「未命中挂活跃会话」fallback，把跨端/
    // 他会话的 ack 渲染进当前会话。匹配不上（跨端 steer 的 ack 不回本 socket、
    // 刷新丢条）静默丢。
    if (e.type === 'ai.steer.ack') {
      handleSteerAck(e);
      return;
    }

    // 清单跟随设备侧 run/审批生命周期：2s 节流刷新侧栏角标/状态点（对未打开
    // 的设备会话同样生效，故放在归属判定之前）。
    if (e.type === 'ai.run.started' || e.type === 'ai.done' || e.type === 'ai.error' || e.type === 'ai.approval.request') {
      refreshOnEvent();
    }

    // 撤回链的会话终态结算（Task 13）：done/error 都是回合终态，在通用会话
    // 归属判定之前结算——壳路由/活跃兜底的归属差异不让等待方空等超时。
    if ((e.type === 'ai.done' || e.type === 'ai.error') && e.session_id) {
      // 键归一与 set 侧对称（waitForSessionTerminal 的 String()）：WS 帧 JSON
      // 反序列化后 session_id 理应为 string，但历史帧/桩注入可能带数字——
      // get 侧不归一会静默 miss，等待方空等超时。
      const pendingTerminal = pendingTerminals.get(String(e.session_id));
      if (pendingTerminal) pendingTerminal('terminal');
    }

    let conv = e.session_id ? conversations.value.find(c => c.aiSessionId === e.session_id) : null;
    // session_id 是否命中决定事件归属；matchedById=false 的事件走 fallback 仅为
    // 让流仍能渲染，绝不能成为回显/dirLocked 的写入目标（会话已删或他页会话的
    // 迟到 create 回执会污染当前活跃会话）。
    const matchedById = !!conv;
    // 审批族帧（request——resolved 是同型 request 带 status——与 cancelled）未按
    // aiSessionId 命中时的分源兜底（阶段 2 计划钉死）：
    // ②设备壳三键匹配（entry.id/source_session_id/managed_conversation_id）→
    //   挂该壳，**不切 activeId**（卡在壳上，用户点开即见）；
    // ③仍无命中 → 挂活跃会话（现状 fallback），审批卡带「来自会话 <id 前 8>」
    //   来源标签。非审批帧维持现状 fallback。
    // Task 14：ai.option.request/cancelled 与审批族同走后端审批族广播
    // （fanOut 白名单同表），归属判定镜像同一条链（①直连→②壳三键→③兜底
    // 带来源标）——数据结构独立，路由链共享。
    const isApprovalFamily = e.type === 'ai.approval.request' || e.type === 'ai.approval.cancelled'
      || e.type === 'ai.option.request' || e.type === 'ai.option.cancelled'
      || e.type === 'tool_ask.request' || e.type === 'tool_ask.cancel';
    let routedToShell = false;
    if (!conv && isApprovalFamily && e.session_id) {
      const shell = findDeviceShellBySessionId(e.session_id);
      if (shell) {
        conv = shell;
        routedToShell = true;
      } else if (activeConversation.value) {
        // ③ 兜底：只记 id 切片，前缀文案由渲染层经 chat_approvalFromSession 翻译
        e.sourceId = String(e.session_id).slice(0, 8);
      }
    }
    // run.notice 走活跃兜底（③）时同样带来源标签（评审折叠项）：系统条是单行
    // 文案，前缀由各 case 拼进内容。notice 不做壳三键路由（session_id 即
    // manager 会话 id，① 直连已覆盖，壳路由会放大误挂面）。
    if (!conv && e.type === 'ai.run.notice' && e.session_id && activeConversation.value) {
      e.sourceId = String(e.session_id).slice(0, 8);
    }
    // Fallback: a run started from this page always targets the active
    // conversation's live assistant bubble. If the session id didn't match
    // (e.g. reloaded localStorage holding a stale id, or an id mismatch), route
    // to the active conversation so the stream still renders instead of being
    // silently dropped.
    if (!conv && activeConversation.value) {
      conv = activeConversation.value;
    }
    if (!conv) {
      console.debug('[chat] ai event has no target conversation', e);
      return;
    }

    // Lightweight trace so a streaming/render issue can be localized from the
    // browser console (event type + session match). Delta text is intentionally
    // omitted to keep the log readable.
    console.log('[aliang-chat] ai event', e.type, 'session=', e.session_id, 'conv=', conv.id);

    // 喂狗：任何到达会话归属判定之后的帧都算 AI 活动，重置无活动计时。审批族
    // 帧路由到设备壳时例外——看门狗目标是活跃 run 的会话，不能被无关帧换靶。
    // 断线自愈路径的再武装口：onclose 曾无条件 disarm（计时器为 null，旧门
    // `if (idleWatchdogTimer && ...)` 从此永远假——重连治愈的 run 僵死即永
    // running），活跃会话发送在途时允许从零重新武装。
    if ((idleWatchdogTimer || (conv.id === activeId.value && sending.value)) && !routedToShell) armIdleWatchdog(conv);

    let persist = false;
    switch (e.type) {
      case 'ai.session.created': {
        const pending = pendingCreates.get(e.session_id);
        if (!matchedById) {
          // 未按 session_id 命中（会话已删/他页会话的迟到回执）：只结算确认门，
          // 不做任何 conv 写入——fallback 命中的活跃会话不能被污染。
          if (pending) pending('created');
          return;
        }
        conv.aiReady = true;
        // 目录锁定以服务端确认为准：create 成功后不可再改（create 失败保持可改）。
        conv.dirLocked = true;
        // 回显是服务端清洗后的权威值：project_path 已是解析后的绝对路径。
        if (typeof e.project_path === 'string' && e.project_path) conv.projectPath = e.project_path;
        if (typeof e.model === 'string') conv.model = e.model;
        if (typeof e.effort === 'string') conv.effort = e.effort;
        // approval_mode 回显:后端归一后的权威值(非法值回落空)。
        if (typeof e.approval_mode === 'string') conv.approvalMode = e.approval_mode;
        // resume 字段仅首次 create（阶段 2 计划钉死）：ack 即消费——此后每条
        // 消息前的 create 重发与设置条同步都不再携带，防陈旧 resume 压过用户
        // 纠正后的路径（服务端 binding 守卫目前掩盖，但契约在本端收口）。
        // resumeConsumed 同时约束 composable 的清单刷新路径不得复活字段。
        conv.resumeConsumed = true;
        delete conv.resumeSessionId;
        delete conv.resumeProjectPath;
        // agentcore 上下文喂入（Task 4）：transcript 只随首条 create 发出，ack
        // 即消费——此后确认门/设置条重发的 create 不再携带（防大载荷反复传输）。
        conv.deviceTranscriptSent = true;
        if (pending) pending('created');
        persist = true;
        break;
      }
      case 'ai.run.started': {
        // 回合启动权威收敛（beginRunWindow）：服务端串行回合语义——新回合
        // started 即上一回合已终，滞留 running 旧泡在此关账，绝不与新回合
        // 增量合流（vm22 挤一泡根因口）。ownPending=sending 在途（本端占位
        // 即本回合泡，不关账）。外来回合/后台会话由此产泡接住 delta。
        // matchedById 门控：未命中帧（fallback 兜底挂活跃会话）绝不凭空造泡
        // /置 runActive——他会话的 run 不得污染活跃会话（旧实现只抬不造）。
        if (matchedById) {
          beginRunWindow(conv, now(), { ownPending: conv.id === activeId.value && sending.value });
          // 外来回合快照拉取：run.started 不携带提问行，订阅握手回包（快照）
          // 是网页拿到外来 user 行的唯一载体——本端命中即补订阅（inFlight 守卫
          // 下对本端在途流无副作用）。旧泡关账随 persist 落盘。
          persist = true;
          if (runStartedNeedsSnapshot(conv, matchedById)) {
            sendAI({ type: 'ai.session.subscribe', session_id: conv.aiSessionId });
          }
        } else {
          const asst = findRunningAssistant(conv);
          if (asst) asst.status = 'running';
        }
        // 新回合开始：上一手 effort/model 改动已随本回合的 create 生效，
        // 「下轮生效」角标退场（改动只会经 applySessionSettings 挂起）。
        conv.pendingConfig = false;
        break;
      }
      case 'ai.thinking': {
        // GLM 思考阶段输出(2026-11-05 真机复现实锤):agentcore 回答前的
        // 整段推理走 ai.thinking(不带进 journal text),此前落 default 被
        // 静默丢弃——思考阶段(经常数十秒)仪表盘全程隐形,用户看到的是
        // 「始终没有正在输出的对话」。挂进气泡的 thinking 字段随流渲染。
        const asst = ensureAssistantBubble(conv, now());
        if (!asst) break;
        const delta = e.delta || '';
        if (delta) asst.thinking = (asst.thinking || '') + delta;
        scrollToBottom();
        break;
      }
      case 'ai.delta': {
        const asst = ensureAssistantBubble(conv, now());
        if (!asst) break;
        const delta = e.delta || '';
        // 快照对账去重（窄窗口）：subscribe 注册→快照 journal 读之间落进的
        // delta 既入快照文本又被扇出副本，对账顶内容后紧随的第一枚 delta 若
        // 已是内容尾缀则丢弃，防重复段。窗口 1.5s 后一律放行。
        if (conv.snapshotCatchupAt && Date.now() - conv.snapshotCatchupAt < 1500
          && delta && asst.content.endsWith(delta)) {
          conv.snapshotCatchupAt = 0;
          break;
        }
        conv.snapshotCatchupAt = 0;
        asst.content += delta;
        console.log('[aliang-chat] rendered delta, contentLen=', asst.content.length, 'conv=', conv.id);
        scrollToBottom();
        break;
      }
      case 'ai.status': {
        // approval_not_found(2026-11-05 用户报「404」):审批已过期/已被处理/
        // waiter 已被取消后,残留卡上的点击回发拿不到 waiter——后端回此状态。
        // 它是协议状态不是回答正文:不得写进气泡(旧实现空气泡时会把英文
        // "approval_not_found" 原样写成回答)。处理=清该会话残留 pending 卡
        // (取消帧丢失的陈旧卡)+ 系统条说明,引导重发触发新审批。
        if (e.status === 'approval_not_found') {
          const stale = Array.isArray(conv.approvals)
            ? conv.approvals.filter(a => a.status === 'pending' || a.status === 'responding')
            : [];
          for (const a of stale) {
            handledApprovalIds.add(a.key);
          }
          if (stale.length) {
            conv.approvals = conv.approvals.filter(a => a.status !== 'pending' && a.status !== 'responding');
          }
          conv.messages.push({
            role: 'assistant',
            kind: 'system',
            note: 'approval_not_found',
            content: t('chat_approvalNotFound'),
            status: 'done',
            ts: now(),
          });
          scrollToBottom();
          persist = true;
          break;
        }
        const asst = findRunningAssistant(conv);
        if (asst && !asst.content && e.status) asst.content = e.status;
        persist = true;
        break;
      }
      case 'ai.approval.request': {
        // resolved 同型帧先全局扫卡（2026-11-09 归属漂移）：request 与
        // resolved 键恒一致但可落不同会话对象，只清路由命中的那个会漏掉原卡。
        // pending 帧不走扫卡（正常 upsert 语义）。
        if (e && e.status && e.status !== 'pending') {
          sweepApprovalKey(conversations.value, e.session_id, e.approval_id || e.id, handledApprovalIds);
        }
        upsertApproval(conv, e);
        persist = true;
        break;
      }
      case 'ai.approval.cancelled': {
        // 未结束回合的精确清卡（区别于 done/error 路径的 clearApprovalsForSession
        // 粗暴清空）：逐个作废 + 系统提示条挂对应会话（审批族链已路由到设备壳/
        // 活跃兜底）。③ 兜底时带「来自会话 X」前缀（评审折叠项，单行文案内联）。
        // expires_at 到期后端会以 ai.approval.cancelled(reason=approval_timeout)
        // 收尾，同样走清卡。作废同走全局扫卡（归属漂移同款，2026-11-09）。
        const ids = Array.isArray(e.approval_ids) ? e.approval_ids : [];
        for (const id of ids) {
          sweepApprovalKey(conversations.value, e.session_id, id, handledApprovalIds);
        }
        const cancelledText = e.reason
          ? t('chat_approvalCancelledReason', { reason: e.reason })
          : t('chat_approvalCancelled');
        conv.messages.push({
          role: 'assistant',
          kind: 'system',
          note: 'approval_cancelled',
          content: systemNoteContent(e.sourceId, cancelledText),
          status: 'done',
          ts: now(),
        });
        scrollToBottom();
        persist = true;
        break;
      }
      case 'ai.option.request': {
        // 决策跨端同步（2026-11-08）：resolved 同型帧先翻卡（别端作答，
        // 镜像审批族 status 约定），完整 pending 帧才 upsert——status 缺省
        // 即 v1 旧 agent 兼容（request 帧本无 status 字段）。
        if (e && e.status === 'resolved') {
          resolveOptionCard(conv, e, handledOptionIds, now());
          break;
        }
        upsertOptionCard(conv, e);
        break;
      }
      case 'tool_ask.request': {
        // 决策跨端同步（2026-11-08）：resolved 同型帧先翻卡（别端作答，镜像
        // ai.option 约定），完整 pending 帧才 upsert——status 缺省即 v1 旧
        // agent 兼容（request 帧本无 status 字段）。
        if (e && e.status === 'resolved') {
          resolveToolAskCard(conv, e, handledToolAskIds, now());
          break;
        }
        upsertToolAskCard(conv, e);
        break;
      }
      case 'tool_ask.cancel': {
        // 镜像 ai.option.cancelled：逐 interaction 清卡 + 系统提示条挂对应
        // 会话。interaction_id 可空（agent 批量 sweep——run 终结 unregisterRun
        // 的收口形态），空则作废该 session 全部待答提问卡。ai.done/ai.error
        // 清审批但绝不清提问卡——卡必须活过 run 终态等 SLA 内作答。
        const askId = String(e.interaction_id || '').trim();
        const asks = Array.isArray(conv.toolAsks) ? conv.toolAsks : [];
        const sid = String(e.session_id || '');
        const kept = [];
        let removedAsks = 0;
        for (const card of asks) {
          const hit = askId
            ? askId === card.interactionId && card.key === optionKeyFromValues(sid, card.interactionId)
            : card.sessionId === sid;
          if (hit) {
            // 确认在场被移除的卡才封键位——防作废后迟到重投帧复活。
            handledToolAskIds.add(card.key);
            removedAsks++;
          } else {
            kept.push(card);
          }
        }
        if (removedAsks) {
          conv.toolAsks = kept;
          const cancelledText = e.reason
            ? t('chat_toolAskCancelledReason', { reason: e.reason })
            : t('chat_toolAskCancelled');
          conv.messages.push({
            role: 'assistant',
            kind: 'system',
            note: 'tool_ask_cancelled',
            content: systemNoteContent(e.sourceId, cancelledText),
            status: 'done',
            ts: now(),
          });
          scrollToBottom();
        }
        persist = true; // 系统条进消息流要落盘（无卡可清时无副作用）
        break;
      }
      case 'ai.option.cancelled': {
        // 镜像 ai.approval.cancelled：逐 id 清卡 + 系统提示条挂对应会话。
        // 已作答（answered 终态）的卡同样按作废语义移除；未带 option_ids
        // （协议 Optional）时作废该 session 全部卡。③兜底时带「来自会话 X」
        // 前缀（与审批取消同构）。注意 ai.done/ai.error 清审批但绝不清卡——
        // option.request 先于 ai.done 到达，卡必须活过 run 终态等用户作答。
        const ids = (Array.isArray(e.option_ids) ? e.option_ids : []).map(v => String(v));
        const cards = Array.isArray(conv.optionCards) ? conv.optionCards : [];
        const sid = String(e.session_id || '');
        const kept = [];
        let removed = 0;
        for (const card of cards) {
          const hit = ids.length
            ? ids.includes(card.optionId) && card.key === optionKeyFromValues(sid, card.optionId)
            : card.sessionId === sid;
          if (hit) {
            // 确认在场被移除的卡才封键位（与精确匹配同域；空 option_ids 兜底
            // 分支同样经 hit 走到这里）——防作废后迟到重投帧复活。
            handledOptionIds.add(card.key);
            removed++;
          } else {
            kept.push(card);
          }
        }
        if (removed) {
          conv.optionCards = kept;
          const cancelledText = e.reason
            ? t('chat_optionCancelledReason', { reason: e.reason })
            : t('chat_optionCancelled');
          conv.messages.push({
            role: 'assistant',
            kind: 'system',
            note: 'option_cancelled',
            content: systemNoteContent(e.sourceId, cancelledText),
            status: 'done',
            ts: now(),
          });
          scrollToBottom();
        }
        persist = true; // 系统条进消息流要落盘（无卡可清时无副作用）
        break;
      }
      case 'ai.run.notice': {
        // 信息性帧：resume/历史降级的事实说明，不阻断输入、不进错误气泡。
        // 未知 code 忽略（协议向后兼容——服务端新增 code 不炸旧前端）。
        // ③ 兜底时带「来自会话 X」前缀（评审折叠项）。
        const noticeText = agentRunNoticeText(e.code);
        if (noticeText) {
          conv.messages.push({
            role: 'assistant',
            kind: 'system',
            note: 'run_notice',
            content: systemNoteContent(e.sourceId, noticeText),
            status: 'done',
            ts: now(),
          });
          scrollToBottom();
          persist = true;
        }
        break;
      }
      case 'ai.subagent': {
        // 子agent生命周期帧（spec 2026-11-07-subagent-panel §三）：归属守卫
        // +蛇形归一+reducer 聚合+M1 详情重拉都在 consumeSubagentFrame 纯链
        // （模块级导出供单测），本 case 只做薄壳装配。注意归属判定不能用
        // 上方 conv 路由结果：fallback 链已把未命中帧挂上活跃会话，恰是
        // M2 要防的「他会话帧污染当前面板」路径，守卫以当前打开会话的
        // aiSessionId 为准（含设备壳 aiSessionId=entry.id 的形态）。
        consumeSubagentFrame(
          subagentsBySession,
          e,
          activeConversation.value?.aiSessionId || '',
          { isWatching: isWatchingSubagent, refetch: refetchSubagentsForSession },
        );
        return; // 面板状态是瞬态展示数据：不落 localStorage（persist 不置位）
      }
      case 'ai.done': {
        disarmIdleWatchdog();
        // 收窗（endRunWindow）：runActive/running 复位 + running 泡关账；
        // hadRunning 供定谳复订阅判定（含外来回合活体补建的答案全文收敛）。
        // matchedById 门控同 run.started：未命中帧只关账既有泡，不动 flags。
        let hadRunning = false;
        if (matchedById) {
          hadRunning = endRunWindow(conv);
        } else {
          const fallbackAsst = findRunningAssistant(conv);
          if (fallbackAsst) fallbackAsst.status = 'done';
        }
        clearApprovalsForSession(conv, e.session_id);
        finishRun(conv);
        // 终态定谳复订阅（2026-11-05「只看到 59 60」形态 + vm22 外来回合）：
        // journal turn 行在回合结束才落盘——恢复会话/活体补建会话收到的
        // delta 只覆盖片段，全文此刻才第一次可得。再取一次快照，
        // reconcileLocalSnapshot 用 journal 全文对齐补齐。（仅本地会话；
        // 设备壳走清单/详情通道。）
        if (matchedById && runDoneNeedsSnapshot(conv, hadRunning)) {
          // 刻意不清 recoveredRun：终态快照回来时尾巴补全分支还靠它放行，
          // 补全成功处在 reconcile（journal 全文修复律）里落旗。
          sendAI({ type: 'ai.session.subscribe', session_id: conv.aiSessionId });
        }
        return;
      }
      case 'ai.error': {
        disarmIdleWatchdog();
        const asst = findRunningAssistant(conv);
        if (!e.message_id) {
          // create 阶段失败（典型：手输目录不存在）。亮在设置条上、结算确认门；
          // 不进消息流。目录未锁定（dirLocked 只在回显成功时置位），纠正路径后
          // 下一条消息重发 create 即成功。判定只用 message_id 单条件：运行期
          // 所有 ai.error 出口都带 message_id，create 期全不带；「无 running 气泡」
          // 不可作条件——sendMessage 在发 create 前已推入占位气泡。
          if (matchedById && conv.id === activeId.value) createError.value = e.error || t('chat_createFailed');
          const pending = pendingCreates.get(e.session_id);
          // error_code 随结算透传给确认门调用方（撤回链据此映射专属文案；
          // 帧是整体 parsed JSON，字段天然可达，无需另行解包）。
          if (pending) pending('error', e.error_code);
          saveToStorage();
          return;
        }
        if (asst) {
          asst.status = 'error';
          if (!asst.content) asst.content = e.error || t('chat_aiError');
        }
        // 收窗（endRunWindow）：asst 已先置 error，此处收剩余 running + flags。
        // matchedById 门控同 done/run.started：未命中帧不动 flags（他会话
        // 帧不得清活跃会话的 run 窗口）。
        if (matchedById) endRunWindow(conv);
        clearApprovalsForSession(conv, e.session_id);
        finishRun(conv);
        return;
      }
      default:
        return;
    }
    if (persist) saveToStorage();
  }

  async function sendMessage() {
    if (!isAuthenticated.value) {
      if (activeConversation.value) {
        activeConversation.value.messages.push({ role: 'assistant', content: t('chat_loginRequired'), status: 'done', ts: now() });
      }
      return;
    }

    const text = inputText.value.trim();
    if ((!text && pendingAttachments.value.length === 0) || !activeId.value) return;
    // sending 守卫（Task 4 分流）：非 running 维持禁用；running 会话在 sending
    // 中放行——提交分流成 steer（见下）。
    if (sending.value && !activeSessionRunning.value) return;

    const conv = activeConversation.value;
    if (!conv) return;

    // 图片走完整消息路径（create 门 + ai.message attachments）；running 插话
    // 本批次仅承载文本——带图插话提示等回合结束，绝不静默丢附件。
    if (pendingAttachments.value.length > 0 && isConversationRunning(conv)) {
      createError.value = t('chat_attachSteerBlocked');
      return;
    }
    const attachments = pendingAttachments.value.map(a => ({ type: 'image', name: a.name, path: a.path }));
    const bubbleAttachments = pendingAttachments.value.map(a => ({ name: a.name, url: chatAttachmentUrl(a.ref) }));

    // 分流规则（阶段 2 计划钉死）：running（本地气泡推断 OR 设备壳清单/快照
    // status）→ 提交走 ai.steer；非 running → 原链（create 确认门 + ai.message）。
    if (isConversationRunning(conv)) {
      await submitSteer(conv, text);
      return;
    }

    // 模型必填守卫（2026-10-31）：内核已删除模型兜底链，空模型会被直接拒跑并
    // 回「未指定模型」。与其让用户等一个必然失败的回合，不如在发送前就地拦住
    // 并指路——错误挂在会话设置条上（选中模型后 applySessionSettings 自动清）。
    // 拦截点在 steer 分流之后（running 会话插话不需要模型）、user 气泡 push
    // 之前；撤回链 sendRecall 不加此守卫（撤回重发的 create 自带 conv.model）。
    if (!String(conv.model || '').trim()) {
      createError.value = t('chat_modelRequired');
      return;
    }

    // messageId 提前生成、随气泡落值（Task 13）：撤回链以气泡 id 为 journal
    // 截断锚点，ai.message 帧复用同一 id；provider 落气泡——canRecall 依赖。
    const messageId = generateId();
    conv.messages.push({
      role: 'user',
      content: text,
      messageId,
      provider: conv.provider,
      ts: now(),
      ...(bubbleAttachments.length ? { attachments: bubbleAttachments } : {}),
    });
    if (!conv.title) {
      conv.title = text.slice(0, 20);
    }
    conv.updatedAt = now();
    inputText.value = '';
    saveToStorage();
    // 用户主动作：无条件贴底（e2e「发消息后内容可见」的保真路径——发消息前
    // 用户必然在底部，force 只是把该不变量钉死，不被任何上翻残留劫持）。
    scrollToBottom(true);

    // Live assistant placeholder: its content fills in token-by-token as
    // ai.delta frames arrive, and its status drives the running -> done UI.
    // 占位气泡必须即时落盘(2026-11-04 刷新丢回答事故):delta 只进内存不落
    // 盘,占位若不持久化,刷新后恢复的会话末尾只有 user 气泡——运行态消失、
    // 且 resubscribeRestoredRuns 以「存在 running 气泡」为触发,对账通道
    // 整体哑火,答案永远追不回。create 被拒的早退分支会移除空占位并重存。
    const assistantMsg = { role: 'assistant', content: '', status: 'running', ts: now() };
    conv.messages.push(assistantMsg);
    saveToStorage();
    scrollToBottom(true);

    sending.value = true;
    if (!conv.aiSessionId) conv.aiSessionId = 'local-' + conv.id;

    try {
      await ensureAISocket();
      createError.value = '';
      const ack = await waitForCreateAck(conv);
      if (ack.status !== 'created') {
        // create 被拒或无回执：内联错误已由 ai.error 分支渲染（timeout 无帧，
        // 这里兜底提示）；撤掉空占位气泡防空转；不发 ai.message。
        const asst = findRunningAssistant(conv);
        if (asst && !asst.content) conv.messages = conv.messages.filter(m => m !== asst);
        // 与 ai.error 分支同约定：只在发送会话仍是当前会话时落兜底提示。
        if (conv.id === activeId.value && !createError.value) createError.value = t('chat_serviceUnavailable');
        sending.value = false; // 本函数的 sending=false 在 catch 尾部，早退须自理
        disarmIdleWatchdog();
        saveToStorage();
        return;
      }
      // create 已被服务端接受：记下本次模型/档位，供新建会话沿用（last-used）。
      rememberLastUsedModel(conv.model);
      rememberLastUsedEffort(conv.effort);
      conv.recoveredRun = false; // 新回合自本端发起:delta 全量可达,无需终态回取
      armIdleWatchdog(conv);
      const ok = sendAI({
        type: 'ai.message',
        session_id: conv.aiSessionId,
        message_id: messageId,
        content: text,
        ...(attachments.length ? { attachments } : {}),
      });
      if (!ok) throw new Error('socket-closed');
      // 发送落地才清 pending（失败保留，用户重试不丢图）。
      clearPendingAttachments();
    } catch (_) {
      disarmIdleWatchdog();
      const asst = findRunningAssistant(conv);
      if (asst) {
        asst.status = 'error';
        asst.content = t('chat_serviceUnavailable');
      }
      sending.value = false;
      saveToStorage();
    }
  }

  function close() {
    // iOS Safari 上点关闭不会触发输入框 blur，先把在编的标题落地再卸载页面。
    commitRename();
    showDashboard();
  }

  // 重进页对账（后台静默运行，2026-11-04）：**会话状态的权威在 agent 侧**
  // （journal + 会话态），localStorage 只是视图缓存。因此对账绝不能以本地
  // 缓存形态（恰好有 running 气泡）为触发前提——占位气泡没落盘、或历史
  // 陈旧缓存缺答案时，按缓存触发的订阅会整体哑火，答案永远追不回（用户
  // 实测：刷新后回答与运行态双双消失）。正解：凡有 aiSessionId 的本地会话
  // 一律订阅，由 Go 侧快照决定前端形态——running 补建运行态、idle+journal
  // 补答案（见 reconcileLocalSnapshot）。设备壳不落盘（重进页不在会话池），
  // 打开时 openDeviceSession 自带订阅，无需此处处理。
  async function resubscribeRestoredRuns() {
    const targets = conversations.value
      .filter((c) => !c.deviceSession && !!c.aiSessionId)
      .sort((a, b) => (b.updatedAt || 0) - (a.updatedAt || 0))
      .slice(0, 32); // 爆发上限：一次性订阅全量清单,防超大历史刷屏
    if (!targets.length) return;
    try {
      await ensureAISocket();
    } catch (_) {
      return; // socket 不可用：下次进页再对账，恢复态不动
    }
    for (const c of targets) {
      c.recoveredRun = true; // 终态后回取快照补全 journal 头部（见 ai.done）
      sendAI({ type: 'ai.session.subscribe', session_id: c.aiSessionId });
    }
  }

  function setInputEl(el) {
    // 字符串 ref 语义：卸载时以 null 落值（与拆分前 ref="inputRef" 等价）。
    inputRef.value = el;
  }

  // 断线自愈第二生命线（2026-11-09 家族⑤）：重连预算（2 次）耗尽后页面死寂，
  // 无定时重连/无轮询——断线窗口内错过的 resolved 帧永不补达，DOM 卡片永不
  // 更新。visibilitychange（回到标签页）与 online（网络恢复）是用户回归的
  // 两个确定性时机，各给一次免费重连+重订阅（预算重置，与生产帧归零点同
  // 语义）。socket 已 OPEN 时不动作（防每次切标签页空转）。
  function recoverChatSocketAfterInterruption() {
    if (aiSocket && aiSocket.readyState === WebSocket.OPEN) return;
    reconnectAttempts = 0;
    ensureAISocket()
      .then(() => {
        // 新 socket 订阅集全灭（服务端订阅挂 socket 键，连接结束整体摘除）：
        // 已打开设备壳补订（评审 P1——resubscribeRestoredRuns 只滤本地会话，
        // 壳订阅唯二入口是 openDeviceSession 与 onclose 自愈，恢复路径必须
        // 同口径补上，壳卡快照对账才可达）+ 子agent面板补刷（断连窗口内
        // ai.subagent 帧随 socket 全灭，与 onclose 自愈同款）+ 本地会话
        // running 对账。
        for (const shell of chatRecoverySubscribeSessionIds(conversations.value, deviceSessionSubscribable)) {
          sendAI({ type: 'ai.session.subscribe', session_id: shell.deviceSession.id });
          refreshSubagentsFromDetail(subagentsBySession, shell.aiSessionId || shell.deviceSession.id);
        }
        return resubscribeRestoredRuns();
      })
      .catch(() => {
        // 重连失败：预算不回滚（死线保护仍在），等下次回归时机再试。
      });
  }

  onMounted(() => {
    console.log('[aliang-chat] ChatPage mounted build=stream-v4 ws=' + aiSocketUrl());
    loadFromStorage();
    loadDeviceSessions(); // 设备会话清单：每次进页拉取（权威在后端，不进 localStorage）
    resubscribeRestoredRuns(); // 后台静默运行对账：恢复的 running 气泡向真值收敛
    // 审批卡 expires_at 倒计时刷新（30s 粒度，见 ChatApprovalCard 的 approvalExpiresLabel）
    nowTickTimer = setInterval(() => { nowTick.value = now(); }, 30000);
    disposeChatSocketRecovery = wireChatSocketRecovery(document, window, recoverChatSocketAfterInterruption);
  });

  onUnmounted(() => {
    if (nowTickTimer) {
      clearInterval(nowTickTimer);
      nowTickTimer = null;
    }
    if (disposeChatSocketRecovery) {
      disposeChatSocketRecovery();
      disposeChatSocketRecovery = null;
    }
    if (aiSocket) {
      // 主动关窗 ≠ 断线：run 在 agent 侧继续跑。closingIntentionally 让
      // onclose 的断线画法（重连/「连接已中断」）整体退场，恢复态留给下次
      // 进页的 resubscribeRestoredRuns 对账。
      closingIntentionally = true;
      try { aiSocket.close(); } catch (_) {}
      aiSocket = null;
    }
  });

  return {
    // 状态
    activeId,
    activeConversation,
    activeIsDevice,
    activeMessages,
    activeApprovals,
    activeOptionCards,
    activeToolAsks,
    activeSteerItems,
    activeRunning,
    // 动作槽（发送/停止交替）的 running 门控源：会话级并集判定（本地气泡
    // OR 设备壳清单/快照 status），与 steer 分流/占位符同源（Task 9）。
    activeSessionRunning,
    conversationGroups,
    conversations,
    createError,
    contextWindowDraft,
    deviceAvailable,
    inputPlaceholder,
    inputText,
    maxTokensDraft,
    mcpLoadError,
    mcpLoaded,
    mcpServers,
    mergedConversations,
    skills,
    nowTick,
    renameDraft,
    renamingId,
    renameSurface,
    sendButtonLabel,
    sendApprovalBlocked,
    sendDisabled,
    showMobileSidebar,
    slashIndex,
    slashMatches,
    slashOpen,
    steerBlocked,
    // DOM 交接（拆分后元素在 chat/ 子组件里，经函数 prop 交回）
    messagesNearBottom,
    setInputEl,
    setMessagesContainer,
    setMessagesNearBottom,
    setRenameInput,
    scrollToBottom,
    // 方法（模板事件直接绑定的入口）
    applySessionSettings,
    openDeviceSession, // 侧栏分流之外的可 await 入口（测试/未来调用点）
    applySlash,
    cancelRecall,
    cancelRename,
    close,
    commitContextWindow,
    commitMaxTokens,
    commitRename,
    createConversation,
    deleteConversation,
    ensureMcpServersLoaded,
    loadRecentProjects,
    onSidebarItemClick,
    onSlashInput,
    recallActive,
    respondApproval,
    respondOption,
    respondToolAsk,
    sendMessage,
    sendRecall,
    startRecall,
    startRename,
    stopRun,
    toggleMcpServer,
    toggleSkill,
    // 图片附件（spec §3.6）：pending 态 + 上传/移除动作（ChatComposer 接线）。
    pendingAttachments,
    attachmentUploading,
    addPendingAttachments,
    removePendingAttachment,
    // 子agent面板（spec 2026-11-07-subagent-panel §三）：入口条/面板组件经
    // ChatPage 消费；subagentWatchingTaskId 供 Task 8 面板选中态写入；
    // refetchSubagentsForSession 供面板打开时的详情拉取（只消费 subagents[]）。
    subagentsBySession,
    activeSubagents,
    activeHasRunningSubagents,
    subagentWatchingTaskId,
    refetchSubagentsForSession,
  };
}

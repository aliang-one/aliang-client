import { ref, computed, nextTick, onMounted, onUnmounted, watch } from 'vue';
import { useI18n } from '../i18n';
import { useNavigation } from './useNavigation';
import { slashProviderFor } from '../utils/chatProvider';
import { normalizeConversationTitle } from '../utils/chatTitle';
import { useAuthStore } from '../stores/auth';
import { getAgentSessionDetail } from '../services/agentApi';
import { useDeviceSessions, DEVICE_SESSION_DETAIL_TTL_MS, isTerminalPrefixedSessionId } from './useDeviceSessions';
import {
  normalizeDeviceProvider,
  sessionCreatePayload,
} from '../utils/chatSettings';
import {
  canRecall,
  recallCreatePayload,
  sliceMessagesBefore,
} from '../utils/chatRecall';

const STORAGE_KEY = 'aliang-chat-data';

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
  const handledApprovalIds = new Set();

  const activeConversation = computed(() =>
    conversations.value.find(c => c.id === activeId.value) || null
  );

  const activeMessages = computed(() =>
    activeConversation.value?.messages || []
  );

  const activeApprovals = computed(() =>
    (activeConversation.value?.approvals || []).filter(a => a.status === 'pending' || a.status === 'responding')
  );

  // True while the active conversation's latest assistant message is still streaming.
  const activeRunning = computed(() => {
    const msgs = activeMessages.value;
    for (let i = msgs.length - 1; i >= 0; i--) {
      if (msgs[i].role === 'assistant') return msgs[i].status === 'running';
    }
    return false;
  });

  // --- 续聊发送分流（跨端统一阶段 2 Task 4） ---

  // 会话「running」判定 = 本地气泡推断（activeRunning）OR 设备壳清单/快照
  // status（conv.running，composable 原位刷新）。两者并集：另一端跑着的会话
  // （壳 running 但本地无流式气泡）与本端正在流的会话都算 running。
  function isConversationRunning(conv) {
    if (!conv) return false;
    return !!conv.running || !!findRunningAssistant(conv);
  }

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
    return [...drafts, ...shells];
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
    if ('effort' in patch || 'model' in patch) conv.pendingConfig = true;
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

  // 切会话收起面板、清一次性错误；同时给切走的 agentcore 设备会话退订快照流
  // （幂等，socket 不在线时 sendAI 静默失败，重开会话会重新订阅）。
  // 目录面板/自定义模型的 UI 复位已迁 ChatSettingsBar（watch conversation prop）。
  watch(activeId, (nv, ov) => {
    // 撤回编辑态随会话切换取消（Task 13）：恢复进入前草稿，banner 不跨会话悬挂。
    if (ov && recallState.value?.convId === ov) cancelRecall();
    if (ov) {
      const prev = conversations.value.find(c => c.id === ov);
      if (prev) unsubscribeDeviceSession(prev);
    }
    createError.value = '';
    // 切会话即视为重新贴底：转录装载后的「看到最新一条」语义不被上一个
    // 会话里残留的上翻状态劫持（随后 scrollToBottom 走常规贴底路径）。
    messagesNearBottom.value = true;
  });

  // create 确认门：sendMessage 发出 create 后等回执再发 ai.message——目录无效
  // 时服务端拒绝建会话，绝不能让消息经 ai.message 的 lazy-create 落到 home 跑。
  // 回执来源：ai.session.created → 'created'；create 期 ai.error（无 message_id）
  // → 'error'（错误文本已由该分支渲染）；超时 → 'timeout'（socket 死亡等）。
  // payloadFn（Task 13，可选）：发出什么载荷由调用方决定——常规路径缺省
  // sessionCreatePayload(conv)，撤回路径传 recallCreatePayload 工厂（铁律：
  // 载荷必须真的经 sendAI 上帧，故工厂在发送点求值而非提前构造）。
  const pendingCreates = new Map(); // session_id -> settle(status)

  function waitForCreateAck(conv, timeoutMs = 5000, payloadFn = null) {
    return new Promise((resolve) => {
      const sid = conv.aiSessionId;
      let done = false;
      const settle = (status) => {
        if (done) return;
        done = true;
        pendingCreates.delete(sid);
        clearTimeout(timer);
        resolve(status);
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
    // steer 轻量条是瞬态 UI：ack 状态不跨页持久（陈旧的「已受理」比没有更糟）
    copy.steerItems = [];
    // pendingConfig（「下轮生效」角标）同为瞬态：重载后「是否已开新回合」
    // 无从判定，跨页保留只会展示陈旧角标——与 approvals/steerItems 同剥。
    delete copy.pendingConfig;
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
      // 本窗口是 AliangCode 对话窗口（2026-10-30 用户拍板）：新建会话默认
      // agentcore；聊天窗只展示自家 agent 的对话，不该默认落成 claudecode。
      provider: chatProviderPreset.value || 'agentcore',
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
  // → 原 selectConversation。
  function onSidebarItemClick(c) {
    if (c?.deviceSession) {
      openDeviceSession(c);
      return;
    }
    selectConversation(c.id);
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

  // 详情/快照转录 → 壳 messages（复用既有气泡渲染路径）。role 只认
  // user/assistant，系统/工具行跳过；同时存壳 deviceTranscript（[{role,content}]
  // 原始形态，Task 4 的 agentcore create transcript 喂入消费——后端
  // remoteAgentAIHistory 按 row.role/row.content 解析）。
  function applyDeviceTranscript(shell, transcript) {
    const rows = Array.isArray(transcript) ? transcript : [];
    const renderable = rows.filter(m => m && (m.role === 'user' || m.role === 'assistant')
      && typeof m.content === 'string' && m.content);
    shell.messages = renderable.map(m => ({
      role: m.role,
      content: m.content,
      status: 'done',
      // 撤回锚点接线（Task 13）：详情 wire 行的 id 即 journal message_id，落
      // 气泡后可作 truncate_before_message_id；provider 落上（canRecall 依赖）。
      messageId: m.id || '',
      provider: shell.provider || '',
      ts: Date.parse(m.timestamp) || now(),
    }));
    shell.deviceTranscript = renderable.map(m => ({ role: m.role, content: m.content }));
    scrollToBottom();
  }

  // 快照转录行（{role,text,message_id?,ts?}）→ 壳 messages（**仅展示流**），
  // 字段名与详情不同（快照用 text，详情用 content）。职责分离（阶段 2 评审
  // 钉死）：快照是有损切片（200 行/4000 rune）且晚到会覆盖屏幕，但续聊喂入的
  // deviceTranscript 必须保持详情分页拉全的版本——快照绝不写 deviceTranscript，
  // 否则截断尾巴会在 create 时顶掉全量上下文（喂入即全部上下文）。
  function applyDeviceSnapshotTranscript(shell, rows) {
    const list = Array.isArray(rows) ? rows : [];
    const renderable = list.filter(m => m && (m.role === 'user' || m.role === 'assistant')
      && typeof m.text === 'string' && m.text);
    shell.messages = renderable.map(m => ({
      role: m.role,
      content: m.text,
      status: 'done',
      // 快照 id 系 journal 同源（spec §A 已修订），可作撤回锚点；内容仍绝不
      // 喂入（deviceTranscript 专属流不变）。provider 落上（canRecall 依赖）。
      messageId: m.message_id || '',
      provider: shell.provider || '',
      ts: Date.parse(m.ts) || now(),
    }));
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
      // 翻页方向是「向更早」（before = 上一页最旧行的 message_id）：每页新行
      // 前插，最终保持时间正序。中途页失败降级为已取到的部分转录（console
      // 留痕，不亮错误条）；首页失败维持既有「空转录+错误条」。
      const pageRows = [];
      let before = '';
      let pageError = null;
      for (let page = 0; page < DEVICE_DETAIL_MAX_PAGES; page++) {
        try {
          const detail = await getAgentSessionDetail(entry.id, {
            limit: DEVICE_DETAIL_PAGE_LIMIT,
            before: before || undefined,
          });
          const rows = Array.isArray(detail?.transcript) ? detail.transcript : [];
          pageRows.unshift(rows);
          live.deviceLoadedAt = now();
          live.deviceLoadError = '';
          const pg = detail?.transcript_page;
          if (!pg?.has_more || !pg?.next_before_message_id || !rows.length) break;
          before = String(pg.next_before_message_id);
        } catch (err) {
          pageError = err;
          break;
        }
      }
      const transcript = pageRows.flat();
      if (transcript.length) {
        if (pageError) console.warn('[aliang-chat] device transcript partial pages', pageError);
        applyDeviceTranscript(live, transcript);
      } else if (pageError) {
        // 空转录+错误条：详情缺席不阻断（本地会话池里壳已在，输入照常）。
        // 只存布尔标记——文案在模板渲染时走 i18n，不冻结赋值时的语言。
        live.deviceLoadError = true;
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
  function handleDeviceSnapshot(e) {
    const sessionId = String(e.session_id || '');
    if (!sessionId) return;
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

    // pending_approvals → 逐条走既有 upsertApproval；快照行缺 status 时补
    // pending 默认（展开顺序：item 在前——行自带 status 时不得被默认值反客为主，
    // upsertApproval 只认 pending/responding，其余 status 走收卡）。
    for (const item of Array.isArray(e.pending_approvals) ? e.pending_approvals : []) {
      if (!item || typeof item !== 'object') continue;
      upsertApproval(shell, { ...item, status: item.status || 'pending' });
    }

    // status → 壳 running 标记（侧栏状态点消费）
    shell.running = e.status === 'running';
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
        const conv = activeConversation.value;
        if (conv && (sending.value || findRunningAssistant(conv))) {
          const asst = findRunningAssistant(conv);
          if (asst) {
            asst.status = 'error';
            if (!asst.content) {
              asst.content = e.code === 1011 ? t('chat_agentOffline') : t('chat_connInterrupted');
            }
          }
          conv.aiSessionCreated = false; // 下条消息经确认门重发 create 重试
          // 离线语义下同步压住设置条的 serviceUnavailable（先设 createError，
          // 再结算确认门——早退分支的 `!createError.value` 兜底就不触发了）
          if (conv.id === activeId.value && !createError.value) {
            createError.value = e.code === 1011 ? t('chat_agentOffline') : t('chat_connInterrupted');
          }
          const pending = conv.aiSessionId ? pendingCreates.get(conv.aiSessionId) : undefined;
          if (pending) pending('error'); // 别让确认门空等 5s 超时
          finishRun(conv);
          saveToStorage();
        }
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

  function findRunningAssistant(conv) {
    const msgs = conv.messages || [];
    for (let i = msgs.length - 1; i >= 0; i--) {
      if (msgs[i].role === 'assistant' && msgs[i].status === 'running') return msgs[i];
    }
    return null;
  }

  // Returns the live assistant bubble for a streaming run, creating a fresh
  // placeholder if sendMessage's placeholder was lost (e.g. page reloaded mid-run
  // holding a stale session id, or a state desync). Never spawns a stray bubble
  // after the turn already finished (done/error) — late deltas are then ignored.
  function ensureAssistantBubble(conv) {
    const asst = findRunningAssistant(conv);
    if (asst) return asst;
    const msgs = conv.messages || [];
    const last = msgs[msgs.length - 1];
    if (last && last.role === 'assistant' && (last.status === 'done' || last.status === 'error')) {
      return null;
    }
    const fresh = { role: 'assistant', content: '', status: 'running', ts: now() };
    conv.messages.push(fresh);
    return fresh;
  }

  function normalizeConversation(conv) {
    if (!conv || typeof conv !== 'object') return null;
    if (!Array.isArray(conv.messages)) conv.messages = [];
    conv.approvals = [];
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
      default:
        return '';
    }
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
  const RECALL_CREATE_ACK_MS = 5000;
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
    try {
      await ensureAISocket();

      // ① running 先停。stop 目标与 steer 同构（本地 aiSessionId / 壳 managed
      //    ?? entry.id）；agentcore 撤回链的壳必回退 entry.id，空目标理论不可达，
      //    真发生按可重试失败中止。
      if (isConversationRunning(conv)) {
        const target = steerTargetId(conv);
        if (!target || !sendAI({ type: 'ai.stop', session_id: target })) {
          throw new Error('socket-closed');
        }
        const terminal = await waitForSessionTerminal(target);
        if (terminal === 'timeout') {
          // 中止：编辑态与回填保留，绝不发 truncate create（场景 E 钉死）。
          createError.value = t('chat_recallStopTimeout');
          sending.value = false; // 本函数的收尾在 recalling 尾部，早退须自理
          recalling.value = false;
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
      if (ack !== 'created') {
        createError.value = t('chat_recallUnconfirmed');
        sending.value = false;
        recalling.value = false;
        return; // 编辑态保留；truncate 幂等，原样重试安全
      }

      // ③ ack created → 本地截断 + 追加（重切一次：stop 收敛期可能新落了系统
      //    条，截断点之后的内容一律弃置，与 journal 截断口径一致）。
      const before = sliceMessagesBefore(conv.messages, st.targetMessageId);
      if (!before) {
        cancelRecall();
        sending.value = false;
        recalling.value = false;
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
      createError.value = t('chat_recallUnconfirmed');
      sending.value = false;
      recalling.value = false;
      saveToStorage();
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
    const isApprovalFamily = e.type === 'ai.approval.request' || e.type === 'ai.approval.cancelled';
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
    if (idleWatchdogTimer && !routedToShell) armIdleWatchdog(conv);

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
        const asst = ensureAssistantBubble(conv);
        if (asst) asst.status = 'running';
        // 新回合开始：上一手 effort/model 改动已随本回合的 create 生效，
        // 「下轮生效」角标退场（改动只会经 applySessionSettings 挂起）。
        conv.pendingConfig = false;
        break;
      }
      case 'ai.delta': {
        const asst = ensureAssistantBubble(conv);
        if (!asst) break;
        asst.content += e.delta || '';
        console.log('[aliang-chat] rendered delta, contentLen=', asst.content.length, 'conv=', conv.id);
        scrollToBottom();
        break;
      }
      case 'ai.status': {
        const asst = findRunningAssistant(conv);
        if (asst && !asst.content && e.status) asst.content = e.status;
        persist = true;
        break;
      }
      case 'ai.approval.request': {
        upsertApproval(conv, e);
        persist = true;
        break;
      }
      case 'ai.approval.cancelled': {
        // 未结束回合的精确清卡（区别于 done/error 路径的 clearApprovalsForSession
        // 粗暴清空）：逐个作废 + 系统提示条挂对应会话（审批族链已路由到设备壳/
        // 活跃兜底）。③ 兜底时带「来自会话 X」前缀（评审折叠项，单行文案内联）。
        // expires_at 到期后端会以 ai.approval.cancelled(reason=approval_timeout)
        // 收尾，同样走清卡。
        const ids = Array.isArray(e.approval_ids) ? e.approval_ids : [];
        for (const id of ids) {
          const key = approvalKeyFromValues(e.session_id, id);
          handledApprovalIds.add(key);
          removeApproval(conv, key);
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
      case 'ai.done': {
        disarmIdleWatchdog();
        const asst = findRunningAssistant(conv);
        if (asst) asst.status = 'done';
        clearApprovalsForSession(conv, e.session_id);
        finishRun(conv);
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
          if (pending) pending('error');
          saveToStorage();
          return;
        }
        if (asst) {
          asst.status = 'error';
          if (!asst.content) asst.content = e.error || t('chat_aiError');
        }
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
    if (!text || !activeId.value) return;
    // sending 守卫（Task 4 分流）：非 running 维持禁用；running 会话在 sending
    // 中放行——提交分流成 steer（见下）。
    if (sending.value && !activeSessionRunning.value) return;

    const conv = activeConversation.value;
    if (!conv) return;

    // 分流规则（阶段 2 计划钉死）：running（本地气泡推断 OR 设备壳清单/快照
    // status）→ 提交走 ai.steer；非 running → 原链（create 确认门 + ai.message）。
    if (isConversationRunning(conv)) {
      await submitSteer(conv, text);
      return;
    }

    // messageId 提前生成、随气泡落值（Task 13）：撤回链以气泡 id 为 journal
    // 截断锚点，ai.message 帧复用同一 id；provider 落气泡——canRecall 依赖。
    const messageId = generateId();
    conv.messages.push({ role: 'user', content: text, messageId, provider: conv.provider, ts: now() });
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
    const assistantMsg = { role: 'assistant', content: '', status: 'running', ts: now() };
    conv.messages.push(assistantMsg);
    scrollToBottom(true);

    sending.value = true;
    if (!conv.aiSessionId) conv.aiSessionId = 'local-' + conv.id;

    try {
      await ensureAISocket();
      createError.value = '';
      const ack = await waitForCreateAck(conv);
      if (ack !== 'created') {
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
      armIdleWatchdog(conv);
      const ok = sendAI({
        type: 'ai.message',
        session_id: conv.aiSessionId,
        message_id: messageId,
        content: text,
      });
      if (!ok) throw new Error('socket-closed');
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

  function setInputEl(el) {
    // 字符串 ref 语义：卸载时以 null 落值（与拆分前 ref="inputRef" 等价）。
    inputRef.value = el;
  }

  onMounted(() => {
    console.log('[aliang-chat] ChatPage mounted build=stream-v3 ws=' + aiSocketUrl());
    loadFromStorage();
    loadDeviceSessions(); // 设备会话清单：每次进页拉取（权威在后端，不进 localStorage）
    // 审批卡 expires_at 倒计时刷新（30s 粒度，见 ChatApprovalCard 的 approvalExpiresLabel）
    nowTickTimer = setInterval(() => { nowTick.value = now(); }, 30000);
  });

  onUnmounted(() => {
    if (nowTickTimer) {
      clearInterval(nowTickTimer);
      nowTickTimer = null;
    }
    if (aiSocket) {
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
    activeSteerItems,
    activeRunning,
    // 动作槽（发送/停止交替）的 running 门控源：会话级并集判定（本地气泡
    // OR 设备壳清单/快照 status），与 steer 分流/占位符同源（Task 9）。
    activeSessionRunning,
    conversationGroups,
    conversations,
    createError,
    deviceAvailable,
    inputPlaceholder,
    inputText,
    mergedConversations,
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
    applySlash,
    cancelRecall,
    cancelRename,
    close,
    commitRename,
    createConversation,
    deleteConversation,
    loadRecentProjects,
    onSidebarItemClick,
    onSlashInput,
    recallActive,
    respondApproval,
    sendMessage,
    sendRecall,
    startRecall,
    startRename,
    stopRun,
  };
}

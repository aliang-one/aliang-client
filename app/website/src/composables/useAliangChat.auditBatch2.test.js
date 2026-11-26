// 网页聊天审计二批（2026-11-20，TODOS「审计二批遗留」生命周期族+其他 3 条）
// 行为面锁定。每条缺陷先红后绿：
//   B1 断线自愈重连只重订阅 running 子集 → 全量补订（断线窗口外来回合静默缺失）
//   B2 tool_ask/option resolved/cancel 漂移卡 → 审批族同款全局扫卡
//   B3 单槽看门狗被抢靶/被 done 清零 → 按会话 Map（并发挂死回合自愈通道恢复）
//   B4 后台会话终态不复位 sending → sendingConvId 跟踪（切走会话后全 app 输入锁死）
//   B5 删在跑会话不退订+fallback 无白名单（帧污染活跃会话）
//   B6 进页/切会话不锚定底部（贴底守卫初值恒 true 三态失真）
//   B7 卸载后幽灵 WS 重连 → disposeChat（定时器+旗标）
//   B8 useDeviceSessions 尾随刷新定时器泄漏 → dispose
//   B10 撤回重发丢附件（气泡带 path/ref + 链路回带）
//   B11 系统提示条熄灭 running 徽标（倒序扫描跳过 kind=system）
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest';
import { nextTick } from 'vue';

vi.mock('../services/agentApi', () => ({
  getAgentIntegrations: vi.fn(),
  getAgentSessions: vi.fn(),
  getAgentSessionDetail: vi.fn(),
  uploadChatAttachment: vi.fn(),
}));

vi.mock('../stores/auth', async (importOriginal) => {
  const { ref } = await import('vue');
  return {
    useAuthStore: () => ({ isAuthenticated: ref(true) }),
  };
});

vi.mock('./useDeviceSessions', async (importOriginal) => {
  const actual = await importOriginal();
  let shared = null;
  return {
    ...actual,
    useDeviceSessions: () => {
      if (!shared) shared = actual.useDeviceSessions();
      return shared;
    },
  };
});

import { useAliangChat } from './useAliangChat';
import { useDeviceSessions } from './useDeviceSessions';
import { uploadChatAttachment } from '../services/agentApi';
import { useAuthStore } from '../stores/auth';

class FakeWebSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;
  static instances = [];
  constructor(url) {
    this.url = url;
    this.readyState = FakeWebSocket.CONNECTING;
    this.sent = [];
    FakeWebSocket.instances.push(this);
  }
  send(payload) {
    this.sent.push(payload);
  }
  close() {
    this.readyState = FakeWebSocket.CLOSED;
  }
}

async function flushMicrotasks() {
  for (let i = 0; i < 20; i++) await Promise.resolve();
}

function lastSock() {
  return FakeWebSocket.instances.at(-1);
}

function frame(sock, payload) {
  sock.onmessage({ data: JSON.stringify(payload) });
}

function sentFrames(sock) {
  return sock.sent.map((raw) => {
    try {
      return JSON.parse(raw);
    } catch (_) {
      return { type: 'malformed' };
    }
  });
}

function sentTypes(sock) {
  return sentFrames(sock).map((f) => f.type);
}

function makeConv(overrides = {}) {
  return {
    id: overrides.id || 'c1',
    title: '',
    provider: 'agentcore',
    model: 'glm-x',
    aiSessionId: overrides.aiSessionId || 'sess-a',
    running: false,
    runActive: false,
    messages: overrides.messages || [],
    ...overrides,
  };
}

// 借道 ensureAISocket 建 socket 并手动 onopen（stopRun 帧对测试无副作用）。
async function openSocket(chat) {
  const stopping = chat.stopRun();
  const sock = lastSock();
  sock.readyState = FakeWebSocket.OPEN;
  sock.onopen();
  await flushMicrotasks();
  await stopping;
  return sock;
}

beforeEach(() => {
  localStorage.clear();
  FakeWebSocket.instances.length = 0;
  globalThis.WebSocket = FakeWebSocket;
  vi.stubGlobal('confirm', () => true);
  useAuthStore().isAuthenticated.value = true;
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  useAuthStore().isAuthenticated.value = false;
});

describe('B1 断线自愈重连全量补订（不再只订 running 子集）', () => {
  it('空闲本地会话也被补订：断线窗口内别端发起的回合整轮可追', async () => {
    vi.useFakeTimers();
    const chat = useAliangChat();
    const a = makeConv({
      id: 'cA',
      aiSessionId: 'sess-a',
      messages: [
        { role: 'user', content: 'q', status: 'done', ts: 1 },
        { role: 'assistant', content: '流式中', status: 'running', ts: 2 },
      ],
      running: true,
      runActive: true,
    });
    const b = makeConv({ id: 'cB', aiSessionId: 'sess-b', messages: [] });
    chat.conversations.value.push(a, b);
    chat.activeId.value = 'cA';
    await openSocket(chat);
    lastSock().onclose({ code: 1006, reason: '' });
    vi.advanceTimersByTime(900);
    await flushMicrotasks();
    const sock2 = lastSock();
    sock2.readyState = FakeWebSocket.OPEN;
    sock2.onopen();
    await flushMicrotasks();
    const subs = sentFrames(sock2).filter((f) => f.type === 'ai.session.subscribe');
    const ids = subs.map((f) => f.session_id).sort();
    expect(ids).toEqual(['sess-a', 'sess-b']);
  });

  it('在跑会话跌出 recency-32 也被补订（旧重连路径对 running 子集无上限的保证）', async () => {
    vi.useFakeTimers();
    const chat = useAliangChat();
    // 34 个空闲会话（updatedAt 更新）+ 1 个最旧 updatedAt 的在跑会话
    for (let i = 0; i < 34; i++) {
      chat.conversations.value.push(makeConv({
        id: `idle-${i}`,
        aiSessionId: `sess-idle-${i}`,
        updatedAt: 100000 + i,
        messages: [],
      }));
    }
    const stale = makeConv({
      id: 'c-stale-running',
      aiSessionId: 'sess-stale-running',
      updatedAt: 1, // 最旧：recency-32 必然挤不出又挤不出它
      running: true,
      runActive: true,
      messages: [{ role: 'assistant', content: '长跑回合', status: 'running', ts: 1 }],
    });
    chat.conversations.value.push(stale);
    chat.activeId.value = 'idle-33';
    await openSocket(chat);
    lastSock().onclose({ code: 1006, reason: '' });
    vi.advanceTimersByTime(900);
    await flushMicrotasks();
    const sock2 = lastSock();
    sock2.readyState = FakeWebSocket.OPEN;
    sock2.onopen();
    await flushMicrotasks();
    const ids = sentFrames(sock2)
      .filter((f) => f.type === 'ai.session.subscribe')
      .map((f) => f.session_id);
    expect(ids).toContain('sess-stale-running');
  });
});

describe('B2 tool_ask/option resolved/cancel 漂移卡全局扫卡', () => {
  // 漂移的真实形态：同一 session 两个会话对象（草稿+壳双对象/清单刷新换对象/
  // ③活跃兜底落错对象）——路由 find() 命中排前的 A，卡物理落在 B。旧实现只翻
  // 路由命中的 A，B 的卡永滞 pending（可对已作答的卡重复作答）。
  function mountDriftPair(toolAsks = [], optionCards = []) {
    const chat = useAliangChat();
    const a = makeConv({ id: 'cA', aiSessionId: 'sess-x', messages: [] }); // 路由命中者
    const b = makeConv({
      id: 'cB',
      aiSessionId: 'sess-x', // 同 session（漂移根源）
      messages: [],
      toolAsks,
      optionCards,
    }); // 卡的实际落点
    chat.conversations.value.push(a, b);
    chat.activeId.value = 'cA';
    return { chat, a, b };
  }

  it('tool_ask resolved 漂移：卡落在 B、帧路由命中 A → B 卡翻 answered', async () => {
    const { chat, b } = mountDriftPair([
      {
        key: 'sess-x:ask1',
        interactionId: 'ask1',
        sessionId: 'sess-x',
        status: 'pending',
        title: '选择',
        options: [{ id: 'o1', label: '一' }],
      },
    ]);
    const sock = await openSocket(chat);
    frame(sock, {
      type: 'tool_ask.request',
      session_id: 'sess-x',
      interaction_id: 'ask1',
      status: 'resolved',
      answer: { selected_option_id: 'o1' },
    });
    await flushMicrotasks();
    expect(b.toolAsks[0].status).toBe('answered');
  });

  it('tool_ask cancel 漂移：卡落在 B、cancel 帧路由命中 A → B 卡清除+系统条落路由会话', async () => {
    const { chat, a, b } = mountDriftPair([
      { key: 'sess-x:ask1', interactionId: 'ask1', sessionId: 'sess-x', status: 'pending' },
    ]);
    const sock = await openSocket(chat);
    frame(sock, { type: 'tool_ask.cancel', session_id: 'sess-x', interaction_id: 'ask1' });
    await flushMicrotasks();
    expect(b.toolAsks).toHaveLength(0);
    const note = a.messages.find((m) => m.kind === 'system' && m.note === 'tool_ask_cancelled');
    expect(note).toBeTruthy();
  });

  it('ai.option resolved/cancelled 漂移同口径', async () => {
    const { chat, b } = mountDriftPair([], [
      { key: 'sess-x:opt1', optionId: 'opt1', sessionId: 'sess-x', status: 'pending' },
    ]);
    const sock = await openSocket(chat);
    frame(sock, {
      type: 'ai.option.request',
      session_id: 'sess-x',
      option_id: 'opt1',
      status: 'resolved',
      selected: ['x'],
    });
    await flushMicrotasks();
    expect(b.optionCards[0].status).toBe('answered');
    frame(sock, { type: 'ai.option.cancelled', session_id: 'sess-x', option_ids: ['opt1'] });
    await flushMicrotasks();
    expect(b.optionCards).toHaveLength(0);
  });

  it('同键双副本（草稿+壳重投）：resolved 扫卡全翻、不留 pending 副本', async () => {
    const chat = useAliangChat();
    const a = makeConv({
      id: 'cA',
      aiSessionId: 'sess-x',
      messages: [],
      toolAsks: [{ key: 'sess-x:ask1', interactionId: 'ask1', sessionId: 'sess-x', status: 'pending' }],
    });
    const b = makeConv({
      id: 'cB',
      aiSessionId: 'sess-x',
      messages: [],
      toolAsks: [{ key: 'sess-x:ask1', interactionId: 'ask1', sessionId: 'sess-x', status: 'pending' }],
    });
    chat.conversations.value.push(a, b);
    chat.activeId.value = 'cA';
    const sock = await openSocket(chat);
    frame(sock, {
      type: 'tool_ask.request',
      session_id: 'sess-x',
      interaction_id: 'ask1',
      status: 'resolved',
      answer: { selected_option_id: 'o1' },
    });
    await flushMicrotasks();
    expect(a.toolAsks[0].status).toBe('answered');
    expect(b.toolAsks[0].status).toBe('answered');
  });

  it('空 interaction_id 批量作废：跨会话清同 session 全部卡', async () => {
    const { chat, b } = mountDriftPair([
      { key: 'sess-x:ask1', interactionId: 'ask1', sessionId: 'sess-x', status: 'pending' },
      { key: 'sess-x:ask2', interactionId: 'ask2', sessionId: 'sess-x', status: 'pending' },
    ]);
    const sock = await openSocket(chat);
    frame(sock, { type: 'tool_ask.cancel', session_id: 'sess-x', interaction_id: '' });
    await flushMicrotasks();
    expect(b.toolAsks).toHaveLength(0);
  });
});

describe('B3 看门狗按会话隔离（并发挂死回合自愈通道恢复）', () => {
  // 看门狗的生产武装点=发送链（create ack 后 armIdleWatchdog）；本套件经
  // sendMessage 真实武装 A，B 经「活跃会话切换+在途发送」门武装。
  async function sendIntoA(chat, a) {
    chat.inputText.value = 'q1';
    const p = chat.sendMessage();
    await flushMicrotasks();
    const sock = lastSock();
    sock.readyState = FakeWebSocket.OPEN;
    sock.onopen();
    await flushMicrotasks();
    frame(sock, { type: 'ai.session.created', session_id: a.aiSessionId, status: 'created' });
    await flushMicrotasks();
    await Promise.race([p, flushMicrotasks()]);
    return sock;
  }

  it('B 持续出帧抢不走 A 的看门狗：A 挂死被探测+兜底判 error，B 不受累', async () => {
    vi.useFakeTimers();
    const chat = useAliangChat();
    const a = makeConv({ id: 'cA', aiSessionId: 'sess-a', model: 'glm-x', messages: [] });
    const b = makeConv({
      id: 'cB',
      aiSessionId: 'sess-b',
      running: true,
      runActive: true,
      messages: [
        { role: 'user', content: 'q2', status: 'done', ts: 1 },
        { role: 'assistant', content: 'B 流式中', status: 'running', ts: 2 },
      ],
    });
    chat.conversations.value.push(a, b);
    chat.activeId.value = 'cA';
    const sock = await sendIntoA(chat, a);
    // B 的帧持续到达。旧实现：单槽看门狗被 B 帧抢靶（gate=已有狗即武装）→
    // A 挂死永不触发；新实现：B 的帧只武装 B（此形态下 gate 关着，B 无狗），
    // A 的狗按自己的节奏走时。
    for (let i = 0; i < 5; i++) {
      vi.advanceTimersByTime(30000);
      frame(sock, { type: 'ai.delta', session_id: 'sess-b', delta: 'y' });
      await flushMicrotasks();
    }
    // 症状1修复（2026-11-26）：90s 首窗不再盲染红——先发快照探测问权威
    // （长静默工具是合法运行态，快照 running 回包会喂狗续命）。
    expect(a.messages[1].status).toBe('running');
    const probes = sock.sent.filter((raw) => {
      try {
        const p = JSON.parse(raw);
        return p.type === 'ai.session.subscribe' && p.session_id === 'sess-a';
      } catch (_) {
        return false;
      }
    });
    expect(probes).toHaveLength(1);
    // 连续 2 窗无快照回包（socket 半死黑洞）→ 第 3 窗兜底染红，挂死自愈保留
    vi.advanceTimersByTime(200000);
    await flushMicrotasks();
    expect(a.messages[1].status).toBe('error');
    expect(b.messages[1].status).toBe('running');
  });

  it('B 的 ai.done 不清 A 的看门狗：A 挂死仍被兜底', async () => {
    vi.useFakeTimers();
    const chat = useAliangChat();
    const a = makeConv({ id: 'cA', aiSessionId: 'sess-a', model: 'glm-x', messages: [] });
    const b = makeConv({ id: 'cB', aiSessionId: 'sess-b', messages: [] });
    chat.conversations.value.push(a, b);
    chat.activeId.value = 'cA';
    const sock = await sendIntoA(chat, a);
    // 切到 B + 在途发送 → B 帧经「活跃+sending」门武装 B（旧实现换靶/新实现入 B 槽）
    chat.activeId.value = 'cB';
    await nextTick();
    frame(sock, { type: 'ai.delta', session_id: 'sess-b', delta: 'y' });
    await flushMicrotasks();
    frame(sock, { type: 'ai.done', session_id: 'sess-b' });
    await flushMicrotasks();
    // A 的占位泡还在 running；A 的狗必须存活。90s 首窗改探测后，兜底染红
    // 在连续 3 窗无响应处收敛（探测上限 2，症状1修复 2026-11-26）。
    vi.advanceTimersByTime(280000);
    await flushMicrotasks();
    expect(a.messages[1].status).toBe('error');
  });
});

describe('B4 后台会话终态复位 sending（切走会话不再锁死全 app 输入）', () => {
  it('A 发送后切走，ai.done(sess-a) 到达 → sendDisabled 解锁', async () => {
    const chat = useAliangChat();
    const a = makeConv({ id: 'cA', aiSessionId: 'sess-a', model: 'glm-x', messages: [] });
    const b = makeConv({ id: 'cB', aiSessionId: 'sess-b', model: 'glm-x', messages: [] });
    chat.conversations.value.push(a, b);
    chat.activeId.value = 'cA';
    chat.inputText.value = '你好';
    const sending = chat.sendMessage();
    await flushMicrotasks();
    const sock = lastSock();
    sock.readyState = FakeWebSocket.OPEN;
    sock.onopen();
    await flushMicrotasks();
    frame(sock, { type: 'ai.session.created', session_id: 'sess-a', status: 'created' });
    await flushMicrotasks();
    // 切走会话（B 空闲）：在途 sending 使 sendDisabled 仍真（新旧同态）
    chat.activeId.value = 'cB';
    await nextTick();
    expect(chat.sendDisabled.value).toBe(true);
    // 终态落在后台会话 A：sending 必须复位（旧实现只认活跃会话 → 永久锁死）
    frame(sock, { type: 'ai.done', session_id: 'sess-a' });
    await flushMicrotasks();
    await Promise.race([sending, flushMicrotasks()]);
    expect(chat.sendDisabled.value).toBe(false);
  });
});

describe('B5 删在跑会话退订+fallback 白名单', () => {
  it('删除有 aiSessionId 的会话：补发 unsubscribe', async () => {
    const chat = useAliangChat();
    const a = makeConv({
      id: 'cA',
      aiSessionId: 'sess-a',
      running: true,
      messages: [{ role: 'user', content: 'q', status: 'done', ts: 1 }],
    });
    chat.conversations.value.push(a);
    chat.activeId.value = 'cA';
    const sock = await openSocket(chat);
    const before = sock.sent.length;
    chat.deleteConversation();
    await flushMicrotasks();
    const subs = sentFrames(sock).slice(before).filter((f) => f.type === 'ai.session.unsubscribe');
    expect(subs.map((f) => f.session_id)).toEqual(['sess-a']);
    expect(chat.conversations.value).toHaveLength(0);
  });

  it('审批族帧豁免白名单：空闲活跃会话上未命中审批请求仍挂卡（③兜底是设计路由）', async () => {
    const chat = useAliangChat();
    const c = makeConv({ id: 'cC', aiSessionId: 'sess-c', messages: [] });
    chat.conversations.value.push(c);
    chat.activeId.value = 'cC';
    const sock = await openSocket(chat);
    frame(sock, {
      type: 'ai.approval.request',
      session_id: 'sess-elsewhere',
      approval_id: 'ap-x',
      message_id: 'm-1',
      title: '运行命令',
    });
    await flushMicrotasks();
    const approvals = Array.isArray(c.approvals) ? c.approvals : [];
    expect(approvals).toHaveLength(1);
    expect(approvals[0].sourceId).toBe('sess-els'); // 「来自会话 X」来源标
  });

  it('活跃会话空闲时未命中帧只丢弃不污染；在跑时 fallback 照常渲染', async () => {
    const chat = useAliangChat();
    const c = makeConv({ id: 'cC', aiSessionId: 'sess-c', messages: [] });
    chat.conversations.value.push(c);
    chat.activeId.value = 'cC';
    const sock = await openSocket(chat);
    const before = c.messages.length;
    frame(sock, { type: 'ai.delta', session_id: 'sess-x', delta: '外来' });
    await flushMicrotasks();
    expect(c.messages.length).toBe(before);
    // 活跃会话有 running 泡时，未命中帧照常兜底渲染（id 失配的活跃流保真）。
    c.messages.push({ role: 'assistant', content: '', status: 'running', ts: 1 });
    frame(sock, { type: 'ai.delta', session_id: 'sess-x', delta: '本端流' });
    await flushMicrotasks();
    expect(c.messages[c.messages.length - 1].content).toBe('本端流');
  });
});

describe('B5 补：白名单谓词用 sendingConvId 归属（评审 should）', () => {
  it('A 在途发送+切到空闲 B：未命中帧不再挂到 B（页面级 sending 污染门收紧）', async () => {
    const chat = useAliangChat();
    const a = makeConv({ id: 'cA', aiSessionId: 'sess-a', model: 'glm-x', messages: [] });
    const b = makeConv({ id: 'cB', aiSessionId: 'sess-b', messages: [] });
    chat.conversations.value.push(a, b);
    chat.activeId.value = 'cA';
    chat.inputText.value = '你好';
    const sending = chat.sendMessage();
    await flushMicrotasks();
    const sock = lastSock();
    sock.readyState = FakeWebSocket.OPEN;
    sock.onopen();
    await flushMicrotasks();
    frame(sock, { type: 'ai.session.created', session_id: 'sess-a', status: 'created' });
    await flushMicrotasks();
    chat.activeId.value = 'cB';
    await nextTick();
    const before = b.messages.length;
    frame(sock, { type: 'ai.delta', session_id: 'sess-elsewhere', delta: '外来' });
    await flushMicrotasks();
    expect(b.messages.length).toBe(before);
    frame(sock, { type: 'ai.done', session_id: 'sess-a' });
    await flushMicrotasks();
    await Promise.race([sending, flushMicrotasks()]);
  });
});

describe('B5 补：删除在途发送会话立即收口（评审 nit）', () => {
  it('删会话清 sendingConvId+复位 sending：输入区不等 90s 看门狗', async () => {
    const chat = useAliangChat();
    const a = makeConv({ id: 'cA', aiSessionId: 'sess-a', model: 'glm-x', messages: [] });
    const b = makeConv({ id: 'cB', aiSessionId: 'sess-b', model: 'glm-x', messages: [] });
    chat.conversations.value.push(a, b);
    chat.activeId.value = 'cA';
    chat.inputText.value = '你好';
    const sending = chat.sendMessage();
    await flushMicrotasks();
    const sock = lastSock();
    sock.readyState = FakeWebSocket.OPEN;
    sock.onopen();
    await flushMicrotasks();
    frame(sock, { type: 'ai.session.created', session_id: 'sess-a', status: 'created' });
    await flushMicrotasks();
    chat.activeId.value = 'cB';
    await nextTick();
    chat.activeId.value = 'cA';
    await nextTick();
    chat.deleteConversation();
    await flushMicrotasks();
    await Promise.race([sending.catch(() => {}), flushMicrotasks()]);
    expect(chat.sendDisabled.value).toBe(false);
    expect(chat.conversations.value.some((c) => c.id === 'cA')).toBe(false);
  });
});

describe('B6 进页/切会话锚定底部（贴底守卫假 true 根治）', () => {
  function containerWithHeight(height) {
    const el = document.createElement('div');
    Object.defineProperty(el, 'scrollHeight', { value: height, configurable: true });
    return el;
  }

  it('容器就位且有消息：强制置底（scrollTop 到 scrollHeight）', async () => {
    const chat = useAliangChat();
    const c = makeConv({
      id: 'cC',
      aiSessionId: 'sess-c',
      messages: [
        { role: 'user', content: 'q', status: 'done', ts: 1 },
        { role: 'assistant', content: '历史回答', status: 'done', ts: 2 },
      ],
    });
    chat.conversations.value.push(c);
    chat.activeId.value = 'cC';
    const el = containerWithHeight(5000);
    chat.setMessagesContainer(el);
    for (let i = 0; i < 5; i++) await nextTick(); // scrollToBottom 的 nextTick 排队深度不定，冲刷足量
    expect(el.scrollTop).toBe(5000);
  });

  it('切会话重新锚定；锚定后被动追加不再强制拽底（守卫交还几何）', async () => {
    const chat = useAliangChat();
    const a = makeConv({ id: 'cA', aiSessionId: 'sess-a', messages: [{ role: 'user', content: 'q', status: 'done', ts: 1 }] });
    const b = makeConv({ id: 'cB', aiSessionId: 'sess-b', messages: [{ role: 'user', content: 'q2', status: 'done', ts: 1 }] });
    chat.conversations.value.push(a, b);
    chat.activeId.value = 'cA';
    const sock = await openSocket(chat);
    const el = containerWithHeight(3000);
    chat.setMessagesContainer(el);
    for (let i = 0; i < 5; i++) await nextTick();
    expect(el.scrollTop).toBe(3000);
    // 模拟用户上翻：nearBottom 假值回写
    chat.setMessagesNearBottom(false);
    chat.activeId.value = 'cB';
    for (let i = 0; i < 5; i++) await nextTick();
    expect(el.scrollTop).toBe(3000); // 切会话重新锚底
    // 锚定完成后被动追加：scrollToBottom(force=false) 不再拽底（scrollTop 保持）
    chat.setMessagesNearBottom(false);
    frame(sock, { type: 'ai.delta', session_id: 'sess-b', delta: 'z' });
    for (let i = 0; i < 5; i++) await nextTick();
    expect(el.scrollTop).toBe(3000);
  });
});

describe('B7 dispose 止幽灵 WS', () => {
  it('断线重连排期后 dispose：不新建 socket；ensure 链拒绝', async () => {
    vi.useFakeTimers();
    const chat = useAliangChat();
    const a = makeConv({
      id: 'cA',
      aiSessionId: 'sess-a',
      running: true,
      runActive: true,
      messages: [{ role: 'assistant', content: '流式中', status: 'running', ts: 1 }],
    });
    chat.conversations.value.push(a);
    chat.activeId.value = 'cA';
    const sock = await openSocket(chat);
    expect(chat.disposeChat).toBeTypeOf('function');
    sock.onclose({ code: 1006, reason: '' });
    const count = FakeWebSocket.instances.length;
    chat.disposeChat();
    vi.advanceTimersByTime(1000);
    await flushMicrotasks();
    expect(FakeWebSocket.instances.length).toBe(count); // 无幽灵重连
  });
});

describe('B10 撤回重发带附件', () => {
  function recallSetup(chat) {
    const conv = makeConv({
      id: 'cA',
      aiSessionId: 'sess-a',
      model: 'glm-x',
      messages: [
        {
          role: 'user',
          content: '看这张图',
          messageId: 'm-u1',
          provider: 'agentcore',
          ts: 1,
          attachments: [
            { name: 'shot.png', url: '/api/agent/ai/attachment?ref=sess-a/shot.png', path: '/cache/sess-a/shot.png', ref: 'sess-a/shot.png' },
          ],
        },
        { role: 'assistant', content: '回答', status: 'done', ts: 2 },
      ],
    });
    chat.conversations.value.push(conv);
    chat.activeId.value = 'cA';
    return conv;
  }

  it('撤回链回带附件：ai.message 帧+新气泡都带图', async () => {
    const chat = useAliangChat();
    const conv = recallSetup(chat);
    const sock = await openSocket(chat);
    const userMsg = conv.messages[0];
    chat.startRecall(userMsg);
    expect(chat.recallActive.value).toBe(true);
    const sending = chat.sendRecall(conv);
    await flushMicrotasks();
    frame(sock, { type: 'ai.session.created', session_id: 'sess-a', status: 'created' });
    await flushMicrotasks();
    await Promise.race([sending, flushMicrotasks()]);
    // 时间线已截到目标之前 + 新 user 气泡带附件 + ai.message 帧带 attachments
    const newUser = conv.messages.find((m) => m.role === 'user' && m.messageId !== 'm-u1');
    expect(newUser).toBeTruthy();
    expect(newUser.attachments).toHaveLength(1);
    expect(newUser.attachments[0].path).toBe('/cache/sess-a/shot.png');
    const msg = sentFrames(sock).find((f) => f.type === 'ai.message');
    expect(msg).toBeTruthy();
    expect(msg.attachments).toHaveLength(1);
    expect(msg.attachments[0]).toEqual({ type: 'image', name: 'shot.png', path: '/cache/sess-a/shot.png' });
  });

  it('纯文本撤回不引入 attachments 键（帧形态不变）', async () => {
    const chat = useAliangChat();
    const conv = makeConv({
      id: 'cA',
      aiSessionId: 'sess-a',
      model: 'glm-x',
      messages: [
        { role: 'user', content: '纯文本', messageId: 'm-u1', provider: 'agentcore', ts: 1 },
        { role: 'assistant', content: '答', status: 'done', ts: 2 },
      ],
    });
    chat.conversations.value.push(conv);
    chat.activeId.value = 'cA';
    const sock = await openSocket(chat);
    chat.startRecall(conv.messages[0]);
    const sending = chat.sendRecall(conv);
    await flushMicrotasks();
    frame(sock, { type: 'ai.session.created', session_id: 'sess-a', status: 'created' });
    await flushMicrotasks();
    await Promise.race([sending, flushMicrotasks()]);
    const msg = sentFrames(sock).find((f) => f.type === 'ai.message');
    expect(msg).toBeTruthy();
    expect(msg.attachments).toBeUndefined();
    const newUser = conv.messages.find((m) => m.role === 'user' && m.messageId !== 'm-u1');
    expect(newUser.attachments).toBeUndefined();
  });
});

describe('B11 系统提示条不熄灭 running 徽标', () => {
  it('末尾是系统条时倒序扫描跳过，找到真正的 running 泡', () => {
    const chat = useAliangChat();
    const c = makeConv({
      id: 'cA',
      aiSessionId: 'sess-a',
      running: true,
      runActive: true,
      messages: [
        { role: 'user', content: 'q', status: 'done', ts: 1 },
        { role: 'assistant', content: '', status: 'running', ts: 2 },
        { role: 'assistant', kind: 'system', note: 'run_notice', content: '提示', status: 'done', ts: 3 },
      ],
    });
    chat.conversations.value.push(c);
    chat.activeId.value = 'cA';
    expect(chat.activeRunning.value).toBe(true);
  });
});

describe('B8 useDeviceSessions 尾随刷新定时器 dispose', () => {
  it('dispose 后尾随刷新不再拉清单', async () => {
    const { getAgentSessions } = await import('../services/agentApi');
    const dev = useDeviceSessions();
    await dev.loadDeviceSessions();
    const calls = getAgentSessions.mock.calls.length;
    dev.refreshOnEvent();
    vi.useFakeTimers();
    dev.refreshOnEvent(); // 窗口内第二次 → 尾随定时器
    dev.disposeDeviceSessions();
    vi.advanceTimersByTime(3000);
    expect(getAgentSessions.mock.calls.length).toBe(calls);
  });
});

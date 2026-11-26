import { beforeEach, describe, expect, it, vi } from 'vitest';

// agentApi 整体 mock（同 zombieFlags.test 模式：装配 composable 但网络全劫持）。
vi.mock('../services/agentApi', () => ({
  getAgentIntegrations: vi.fn(),
  getAgentSessions: vi.fn(),
  getAgentSessionDetail: vi.fn(),
  uploadChatAttachment: vi.fn(),
}));

vi.mock('../stores/auth', () => ({
  useAuthStore: () => ({ isAuthenticated: { value: true } }),
}));

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

// ⚠️ 动态 import：CHAT_IDLE_TIMEOUT_MS 在模块顶层读取，ES 静态 import 会
// 提升到下方赋值之前（=读走 90000 默认值，狗永不响——本文件首版踩坑实录）。
// 阈值 100ms + sleep(120)：每 sleep 恰覆盖一个狗窗口（再武装间隔=阈值）。
globalThis.__CHAT_IDLE_TIMEOUT_MS__ = 100;
const { beginRunWindow, conversationForStorage, endRunWindow, normalizeConversation, useAliangChat } = await import('./useAliangChat');

// Fake WebSocket：readyState 手控（同 zombieFlags.test）。
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

function sentTypes(sock) {
  return sock.sent.map((raw) => {
    try {
      return JSON.parse(raw).type;
    } catch (_) {
      return 'malformed';
    }
  });
}

function baseConv() {
  return {
    id: 'c1',
    title: '',
    provider: 'agentcore',
    aiSessionId: 'sess-t',
    running: true,
    runActive: true,
    runningTurnMessageId: 'assistant_A',
    approvals: [{ key: 'sess-t:a1', id: 'a1', status: 'pending', sessionId: 'sess-t' }],
    messages: [
      { role: 'user', content: '问题', status: 'done', ts: 1, messageId: 'm-A' },
      { role: 'assistant', content: '流式中', status: 'running', ts: 2 },
    ],
    steerItems: [],
  };
}

async function mountWithSocket(conv) {
  const chat = useAliangChat();
  chat.conversations.value.push(conv);
  chat.activeId.value = 'c1';
  const stopping = chat.stopRun();
  const sock = FakeWebSocket.instances.at(-1);
  sock.readyState = FakeWebSocket.OPEN;
  sock.onopen();
  await flushMicrotasks();
  await stopping;
  return { chat, conv, sock };
}

function frame(sock, payload) {
  sock.onmessage({ data: JSON.stringify(payload) });
}

// --- 症状1修复批（2026-11-26）：回合归属守卫 + 看门狗探测优先 ---

describe('beginRunWindow/endRunWindow 回合归属跟踪（纯函数）', () => {
  it('beginRunWindow 记 runningTurnMessageId；endRunWindow 清零（对称律）', () => {
    const conv = { id: 'x', messages: [] };
    beginRunWindow(conv, 1, { turnMessageId: 'assistant_A' });
    expect(conv.runningTurnMessageId).toBe('assistant_A');
    expect(conv.runActive).toBe(true);
    endRunWindow(conv);
    expect(conv.runningTurnMessageId).toBe('');
    expect(conv.runActive).toBe(false);
  });

  it('不带 turnMessageId 维持旧语义（空串，归属守卫退近因）', () => {
    const conv = { id: 'x', messages: [] };
    beginRunWindow(conv, 1);
    expect(conv.runningTurnMessageId).toBe('');
  });

  it('回合归属 id 遵守瞬态 flag 纪律（收口评审钉正）：落盘剥离+恢复复位', () => {
    const conv = { id: 'x', messages: [], running: true, runActive: true, runningTurnMessageId: 'assistant_A' };
    const copy = conversationForStorage(conv);
    expect(copy.runningTurnMessageId).toBeUndefined(); // 落盘往返不携陈旧回合 id
    const restored = normalizeConversation({ ...copy, runningTurnMessageId: 'assistant_STALE' });
    expect(restored.runningTurnMessageId).toBe(''); // reload 后守卫退回近因语义
    expect(restored.running).toBe(false);
    expect(restored.runActive).toBe(false);
  });
});

describe('ai.error 回合归属守卫（症状1：错误蔓延染红源之一）', () => {
  beforeEach(() => {
    localStorage.clear();
    FakeWebSocket.instances.length = 0;
    globalThis.WebSocket = FakeWebSocket;
  });

  it('不属于当前回合的 ai.error 整体退场：不染泡/不关窗/不清审批', async () => {
    const conv = baseConv();
    const { sock } = await mountWithSocket(conv);
    frame(sock, { type: 'ai.error', session_id: 'sess-t', message_id: 'assistant_FOREIGN', error: '外来回合失败' });
    expect(conv.messages[1].status).toBe('running'); // 当前泡不染红
    expect(conv.runActive).toBe(true);
    expect(conv.running).toBe(true);
    expect(conv.approvals).toHaveLength(1); // 当前 run 的审批卡不被误清
    expect(conv.messages.some(m => m?.note === 'turn_failed')).toBe(false);
  });

  it('归属命中的 ai.error 照旧：染红+落 turn_failed 条+关窗+清审批', async () => {
    const conv = baseConv();
    const { sock } = await mountWithSocket(conv);
    frame(sock, { type: 'ai.error', session_id: 'sess-t', message_id: 'assistant_A', error: '工具执行失败' });
    expect(conv.messages[1].status).toBe('error');
    expect(conv.runActive).toBe(false);
    expect(conv.approvals).toHaveLength(0);
    const note = conv.messages.find(m => m?.note === 'turn_failed');
    expect(note?.content).toBe('工具执行失败');
    expect(note?.failedMessageId).toBe('assistant_A');
  });

  it('runningTurnMessageId 缺席（旧 agent）维持近因语义不收窄', async () => {
    const conv = baseConv();
    delete conv.runningTurnMessageId;
    const { sock } = await mountWithSocket(conv);
    frame(sock, { type: 'ai.error', session_id: 'sess-t', message_id: 'assistant_FOREIGN', error: 'x' });
    expect(conv.messages[1].status).toBe('error');
  });
});

describe('无活动看门狗探测优先（症状1主根因：长静默工具被盲杀）', () => {
  // 真实定时器：fake timers 与该建连链（ensureAISocket 的 connect timer +
  // waitForCreateAck 5s 死钟 + nowTick 30s 心跳同窗）交互诡异（advance 下狗
  // 不响），阈值 60ms + 真实 sleep 的裕量足够确定性。
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

  beforeEach(() => {
    localStorage.clear();
    FakeWebSocket.instances.length = 0;
    globalThis.WebSocket = FakeWebSocket;
  });

  // 完整发送链武装狗：sendMessage → create ack → armIdleWatchdog + ai.message。
  async function mountSent() {
    const chat = useAliangChat();
    const conv = {
      id: 'c1',
      title: '',
      provider: 'agentcore',
      model: 'glm-x',
      aiSessionId: 'sess-t',
      messages: [],
      steerItems: [],
    };
    chat.conversations.value.push(conv);
    chat.activeId.value = 'c1';
    chat.inputText.value = 'hello';
    const p = chat.sendMessage();
    await flushMicrotasks();
    const sock = FakeWebSocket.instances.at(-1);
    sock.readyState = FakeWebSocket.OPEN;
    sock.onopen();
    await flushMicrotasks();
    frame(sock, { type: 'ai.session.created', session_id: 'sess-t' });
    await flushMicrotasks();
    await p;
    return { chat, conv, sock };
  }

  it('狗到期先探测：发快照订阅、泡保持 running、flag 不动', async () => {
    const { conv, sock } = await mountSent();
    conv.messages.push({ role: 'assistant', content: '执行中', status: 'running', ts: 1 });
    conv.runActive = true;
    conv.running = true;
    sock.sent.length = 0;
    await sleep(120); // > 60ms 阈值，第 1 次到期
    expect(sentTypes(sock)).toContain('ai.session.subscribe'); // 探测发出
    expect(conv.messages.at(-1).status).toBe('running'); // 未盲杀
    expect(conv.runActive).toBe(true);
  });

  it('运行中快照回包：无条件喂狗+探测计数归零，长静默工具不再累积到兜底', async () => {
    const { conv, sock } = await mountSent();
    conv.messages.push({ role: 'assistant', content: '执行中', status: 'running', ts: 1 });
    conv.runActive = true;
    conv.running = true;
    sock.sent.length = 0;
    await sleep(120); // fire#1 → probe(1)
    expect(sentTypes(sock).filter(t => t === 'ai.session.subscribe')).toHaveLength(1);
    // 权威活证：running 快照（sending 在途，inFlight 挡收敛但不挡喂狗）
    frame(sock, { type: 'ai.session.snapshot', session_id: 'sess-t', status: 'running', transcript: [] });
    await sleep(120); // fire#2 → probe 计数已归零再探(1)
    expect(sentTypes(sock).filter(t => t === 'ai.session.subscribe')).toHaveLength(2);
    expect(conv.messages.at(-1).status).toBe('running'); // 两窗口过去仍未染红
  });

  it('连续窗口无任何回包（socket 黑洞）落原判兜底：染红+收窗修 flag 僵尸', async () => {
    const { conv, sock } = await mountSent();
    conv.messages.push({ role: 'assistant', content: '执行中', status: 'running', ts: 1 });
    conv.runActive = true;
    conv.running = true;
    sock.sent.length = 0;
    await sleep(120); // probe(1)
    await sleep(120); // probe(2)
    expect(conv.messages.at(-1).status).toBe('running');
    await sleep(120); // fire#3：超限 → 兜底
    expect(conv.messages.at(-1).status).toBe('error');
    expect(conv.runActive).toBe(false); // 原缺陷漏收窗补齐
    expect(conv.running).toBe(false);
    // 兜底染红可能误判（run 实际仍活）：recoveredRun 放行终态定谳复订阅，
    // ai.done 快照回来经 reconcile 洗白律翻绿补全文。
    expect(conv.recoveredRun).toBe(true);
  });

  it('真帧喂狗：探测计数归零，狗重置', async () => {
    const { conv, sock } = await mountSent();
    conv.messages.push({ role: 'assistant', content: '执行中', status: 'running', ts: 1 });
    conv.runActive = true;
    conv.running = true;
    sock.sent.length = 0;
    frame(sock, { type: 'ai.delta', session_id: 'sess-t', message_id: 'assistant_A', delta: 'x' });
    await sleep(40); // 距喂狗 <60ms 不到期
    expect(conv.messages.at(-1).status).toBe('running');
    expect(sentTypes(sock)).not.toContain('ai.session.subscribe');
    await sleep(120); // 距最后喂狗超阈值 → probe
    expect(sentTypes(sock)).toContain('ai.session.subscribe');
  });
});

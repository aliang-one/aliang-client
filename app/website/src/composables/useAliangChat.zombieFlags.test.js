import { beforeEach, describe, expect, it, vi } from 'vitest';

// agentApi 整体 mock（同 subagent.hydrate.test 模式：装配 composable 但网络
// 全劫持）。useDeviceSessions 用真实现 + 模块级单例桥接（同 hydrate 测试）。
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

import {
  conversationForStorage,
  normalizeConversation,
  useAliangChat,
} from './useAliangChat';

// 2026-11-18 「进行中」僵尸修复批（已定案，勿重新诊断）：
//   ① conversationForStorage 落盘剥 running/runActive；
//   ② normalizeConversation 恢复清洗两 flag；
//   ③ 快照对账 idle 分支补 endRunWindow（此前唯一复位点只有 ai.done/ai.error）；
//   ④ steer ack not_running 降级改发（手机 parity）。
// 本文件钉死上述四触点的行为面；重构成分（上提纯函数）不在此重复断言。

// Fake WebSocket：readyState 手控、实例句柄留在类上，测试手动驱动 onopen，
// 并直接调 onmessage 注入帧（与生产同一 JSON.parse → handleAIEvent 链）。
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

// 微任务冲刷：async 链（steer ack → 降级 ensureAISocket/create 门/sendAI）
// 全走 promise；20 轮足以覆盖最深链路。
async function flushMicrotasks() {
  for (let i = 0; i < 20; i++) await Promise.resolve();
}

// 建一个带僵尸 flag 的本地会话并挂进会话池；stopRun 只是借道 ensureAISocket
// 建 socket（ai.stop 帧对测试无副作用），返回 { chat, conv, sock }。
// 顺序同 hydrate 测试的 openHistorySession：先启动、再手动 onopen、最后 await
// （ensureAISocket 的 promise 由 onopen resolve，await 前置会死等）。
async function mountZombieSession({ steerItems = [] } = {}) {
  const chat = useAliangChat();
  const conv = {
    id: 'c1',
    title: '',
    provider: 'agentcore',
    model: 'glm-x',
    aiSessionId: 'sess-z',
    running: true,
    runActive: true,
    messages: [
      { role: 'user', content: '问题', status: 'done', ts: 1, messageId: 'm-u1' },
      { role: 'assistant', content: '流式中', status: 'running', ts: 2 },
    ],
    steerItems,
  };
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

function sentTypes(sock) {
  return sock.sent.map((raw) => {
    try {
      return JSON.parse(raw).type;
    } catch (_) {
      return 'malformed';
    }
  });
}

describe('conversationForStorage（落盘剥僵尸 flag）', () => {
  it('running/runActive 落盘前剥除，消息与普通字段原样保留', () => {
    const conv = {
      id: 'c1',
      title: 'T',
      model: 'm',
      running: true,
      runActive: true,
      messages: [{ role: 'user', content: 'q', status: 'done', ts: 1 }],
    };
    const copy = conversationForStorage(conv);
    expect(copy.running).toBeUndefined();
    expect(copy.runActive).toBeUndefined();
    expect(copy.id).toBe('c1');
    expect(copy.messages).toHaveLength(1);
    // 原对象不动（拷贝剥离）
    expect(conv.running).toBe(true);
  });

  it('瞬态对象同剥（与既有纪律一致，防止回归撞车）', () => {
    const copy = conversationForStorage({
      id: 'c2',
      running: true,
      runActive: true,
      approvals: [{ key: 'k' }],
      steerItems: [{ messageId: 's1' }],
      pendingConfig: true,
    });
    expect(copy.approvals).toEqual([]);
    expect(copy.steerItems).toEqual([]);
    expect(copy.pendingConfig).toBeUndefined();
    expect(copy.running).toBeUndefined();
    expect(copy.runActive).toBeUndefined();
  });
});

describe('normalizeConversation（恢复清洗僵尸 flag）', () => {
  it('恢复时强制 running/runActive=false；running 气泡不动（流式内容交给快照对账收账）', () => {
    const conv = normalizeConversation({
      id: 'c3',
      running: true,
      runActive: true,
      messages: [{ role: 'assistant', content: '已见内容', status: 'running', ts: 1 }],
    });
    expect(conv.running).toBe(false);
    expect(conv.runActive).toBe(false);
    expect(conv.messages[0].status).toBe('running');
    expect(conv.messages[0].content).toBe('已见内容');
  });

  it('缺失 flag 的旧数据补 false；非对象安全返回 null', () => {
    expect(normalizeConversation({ id: 'c4', messages: [] }).running).toBe(false);
    expect(normalizeConversation(null)).toBe(null);
    expect(normalizeConversation('garbage')).toBe(null);
  });
});

describe('快照对账 idle 收窗（reconcileLocalSnapshot !snapshotRunning 分支）', () => {
  beforeEach(() => {
    localStorage.clear();
    FakeWebSocket.instances.length = 0;
    globalThis.WebSocket = FakeWebSocket;
  });

  it('权威 idle 快照收敛僵尸 flag：running/runActive 复位 + 滞留 running 泡关账', async () => {
    const { conv, sock } = await mountZombieSession();
    sock.onmessage({
      data: JSON.stringify({
        type: 'ai.session.snapshot',
        session_id: 'sess-z',
        status: 'idle',
        transcript: [
          { id: 'm-u1', role: 'user', content: '问题' },
          { id: 'r1', role: 'assistant', content: '流式中' },
        ],
      }),
    });
    await flushMicrotasks();
    expect(conv.running).toBe(false);
    expect(conv.runActive).toBe(false);
    expect(conv.messages[1].status).toBe('done');
  });

  it('running 快照不收窗（run 真活着，flag 保持）', async () => {
    const { conv, sock } = await mountZombieSession();
    sock.onmessage({
      data: JSON.stringify({
        type: 'ai.session.snapshot',
        session_id: 'sess-z',
        status: 'running',
        transcript: [
          { id: 'm-u1', role: 'user', content: '问题' },
          { id: 'r1', role: 'assistant', content: '流式中' },
        ],
      }),
    });
    await flushMicrotasks();
    expect(conv.running).toBe(true);
    expect(conv.runActive).toBe(true);
    expect(conv.messages[1].status).toBe('running');
  });
});

describe('运行错误的实时显示', () => {
  beforeEach(() => {
    localStorage.clear();
    FakeWebSocket.instances.length = 0;
    globalThis.WebSocket = FakeWebSocket;
  });

  it.each(['流式中', null])('已有部分回复或没有气泡时，仍展示上游原因（%s）', async (partial) => {
    const { conv, sock } = await mountZombieSession();
    if (!partial) conv.messages.pop();
    const error = '404 model_not_found';
    const frame = { type: 'ai.error', session_id: 'sess-z', message_id: 'assistant_m-u1', error };
    sock.onmessage({ data: JSON.stringify(frame) });
    sock.onmessage({ data: JSON.stringify(frame) });
    expect(conv.messages.filter(m => m.note === 'turn_failed')).toEqual([
      expect.objectContaining({ content: error }),
    ]);
    if (partial) expect(conv.messages[1].content).toBe(partial);
    expect(conv.running).toBe(false);
  });

  it('旧回合的错误出现在原提问下，下一轮继续接收回复', async () => {
    const { conv, sock } = await mountZombieSession();
    conv.messages.push({ role: 'user', messageId: 'm-u2', ts: 3 });
    conv.messages.push({ role: 'assistant', content: '第二轮回复', status: 'running', ts: 4 });
    sock.onmessage({ data: JSON.stringify({
      type: 'ai.error', session_id: 'sess-z', message_id: 'assistant_m-u1',
      error: 'Upstream access forbidden',
    }) });
    expect(conv.messages[2]).toMatchObject({ note: 'turn_failed', content: 'Upstream access forbidden' });
    expect(conv.messages.at(-1)).toMatchObject({ content: '第二轮回复', status: 'running' });
    expect(conv.runActive).toBe(true);
  });

  it('发送后只收到终态快照时，恢复当前回合失败原因并解锁发送', async () => {
    const { chat, conv, sock } = await mountZombieSession();
    conv.runActive = false;
    conv.running = false;
    conv.messages[1].status = 'done';
    chat.inputText.value = '测试 DeepSeek';
    const sending = chat.sendMessage();
    await flushMicrotasks();
    sock.onmessage({ data: JSON.stringify({ type: 'ai.session.created', session_id: 'sess-z' }) });
    await sending;
    const message = sock.sent.map(JSON.parse).find(frame => frame.type === 'ai.message');
    const snapshot = (transcript) => sock.onmessage({ data: JSON.stringify({
      type: 'ai.session.snapshot', session_id: 'sess-z', status: 'idle', transcript,
    }) });
    snapshot([
      { role: 'user', message_id: 'm-u1', text: '问题' },
      { role: 'system', text: '旧回合错误' },
    ]);
    expect(conv.messages.at(-1).status).toBe('running');
    snapshot([
      { role: 'user', message_id: message.message_id, text: message.content },
      { role: 'system', text: '404 model_not_found' },
    ]);
    expect(conv.messages.at(-1)).toMatchObject({ note: 'turn_failed', content: '404 model_not_found' });
    expect(findRunning(conv)).toBe(null);
    chat.inputText.value = '重新发送';
    expect(chat.sendDisabled.value).toBe(false);
  });
});

describe('steer ack not_running 降级改发（手机 parity）', () => {
  beforeEach(() => {
    localStorage.clear();
    FakeWebSocket.instances.length = 0;
    globalThis.WebSocket = FakeWebSocket;
  });

  const steerItem = () => ({
    messageId: 'steer-1',
    content: '插话内容',
    status: 'sent',
    result: '',
    detail: '',
    ts: 1,
  });

  it('not_running：翻窗 + 原插话改发普通消息（ai.message，不再走 ai.steer），条目翻 downgraded', async () => {
    const item = steerItem();
    const { conv, sock } = await mountZombieSession({ steerItems: [item] });
    const framesBeforeAck = sock.sent.length;

    sock.onmessage({
      data: JSON.stringify({
        type: 'ai.steer.ack',
        session_id: 'sess-z',
        message_id: 'steer-1',
        result: 'not_running',
      }),
    });
    await flushMicrotasks();

    // ① 翻窗：僵尸 flag 复位（此后用户手动重发自然走普通通道，不成环）
    expect(conv.running).toBe(false);
    expect(conv.runActive).toBe(false);
    // ② 改发走 ai.message 正常路径：先 create 确认门（此刻只应有 create，
    //    绝无 ai.steer）
    const typesBeforeCreated = sentTypes(sock).slice(framesBeforeAck);
    expect(typesBeforeCreated).toContain('ai.session.create');
    expect(typesBeforeCreated).not.toContain('ai.steer');
    // create 确认门结算（ai.message 只在 ack created 后发出）
    sock.onmessage({
      data: JSON.stringify({ type: 'ai.session.created', session_id: 'sess-z' }),
    });
    await flushMicrotasks();
    const types = sentTypes(sock).slice(framesBeforeAck);
    expect(types).toContain('ai.message');
    expect(types).not.toContain('ai.steer');
    const messageFrame = sock.sent
      .map((raw) => JSON.parse(raw))
      .find((p) => p.type === 'ai.message');
    expect(messageFrame.session_id).toBe('sess-z');
    expect(messageFrame.content).toBe('插话内容');
    // ③ 气泡落位：user 气泡（原插话文本）+ running 占位
    const userBubble = conv.messages.find((m) => m.role === 'user' && m.content === '插话内容');
    expect(userBubble).toBeTruthy();
    expect(findRunning(conv)).toBeTruthy();
    // ④ 轻量条翻降级终态
    expect(item.status).toBe('downgraded');
  });

  it('只发一次：迟到重投的 not_running ack 不再改发，也不把 downgraded 打回 not_running', async () => {
    const item = steerItem();
    const { conv, sock } = await mountZombieSession({ steerItems: [item] });
    const ack = () =>
      sock.onmessage({
        data: JSON.stringify({
          type: 'ai.steer.ack',
          session_id: 'sess-z',
          message_id: 'steer-1',
          result: 'not_running',
        }),
      });

    ack();
    await flushMicrotasks();
    // create 确认门结算（ai.message 发出前必须有 ack created）
    sock.onmessage({
      data: JSON.stringify({ type: 'ai.session.created', session_id: 'sess-z' }),
    });
    await flushMicrotasks();
    const aiMessageCount = sentTypes(sock).filter((t) => t === 'ai.message').length;
    expect(aiMessageCount).toBe(1);
    expect(item.status).toBe('downgraded');

    // 迟到重投：同一条 ack 再来一次
    const before = sock.sent.length;
    ack();
    await flushMicrotasks();
    const typesAfter = sentTypes(sock).slice(before);
    expect(typesAfter).not.toContain('ai.message');
    expect(typesAfter).not.toContain('ai.steer');
    expect(item.status).toBe('downgraded');
    expect(conv.messages.filter((m) => m.role === 'user' && m.content === '插话内容')).toHaveLength(1);
  });

  it('create 被拒：占位撤除、轻量条保持 not_running（事实陈述不因改发失败失真）', async () => {
    const item = steerItem();
    const { conv, sock } = await mountZombieSession({ steerItems: [item] });
    sock.onmessage({
      data: JSON.stringify({
        type: 'ai.steer.ack',
        session_id: 'sess-z',
        message_id: 'steer-1',
        result: 'not_running',
      }),
    });
    await flushMicrotasks();
    // create 被拒：ai.error（无 message_id = create 期失败）
    sock.onmessage({
      data: JSON.stringify({ type: 'ai.error', session_id: 'sess-z', error: 'boom' }),
    });
    await flushMicrotasks();
    expect(item.status).toBe('not_running');
    // 空 running 占位已撤（sendMessage 早退分支同款）
    expect(findRunning(conv)).toBe(null);
    // user 气泡保留（内容没丢，可手动重发）
    expect(conv.messages.some((m) => m.role === 'user' && m.content === '插话内容')).toBe(true);
    expect(conv.running).toBe(false);
    expect(conv.runActive).toBe(false);
  });

  it('设备壳不降级：壳可能缺 model 且权威态由快照通道收账，保持 not_running 不发帧', async () => {
    const chat = useAliangChat();
    const shell = {
      id: 'shell-1',
      deviceSession: { id: 'entry-1', provider: 'claude', managed_conversation_id: 'mgmt-9' },
      aiSessionId: 'entry-1',
      provider: 'claude',
      model: '',
      running: true,
      runActive: true,
      messages: [],
      steerItems: [{
        messageId: 'steer-2',
        content: '壳插话',
        status: 'not_running',
        result: 'not_running',
        detail: '',
        ts: 1,
      }],
    };
    chat.conversations.value.push(shell);
    chat.activeId.value = 'shell-1';
    const stopping = chat.stopRun();
    const sock = FakeWebSocket.instances.at(-1);
    sock.readyState = FakeWebSocket.OPEN;
    sock.onopen();
    await flushMicrotasks();
    await stopping;
    const before = sock.sent.length;

    sock.onmessage({
      data: JSON.stringify({
        type: 'ai.steer.ack',
        session_id: 'mgmt-9',
        message_id: 'steer-2',
        result: 'not_running',
      }),
    });
    await flushMicrotasks();

    const types = sentTypes(sock).slice(before);
    expect(types).not.toContain('ai.session.create');
    expect(types).not.toContain('ai.message');
    expect(shell.steerItems[0].status).toBe('not_running');
    // 壳的 flag 不被降级路径收窗（权威在设备清单/快照通道）
    expect(shell.running).toBe(true);
    expect(shell.runActive).toBe(true);
  });
});

// 本文件内的 running 泡查找（避免依赖内部实现的 findRunningAssistant 导出形态
// 变化；语义与 useAliangChat.findRunningAssistant 一致）。
function findRunning(conv) {
  const msgs = Array.isArray(conv.messages) ? conv.messages : [];
  for (let i = msgs.length - 1; i >= 0; i--) {
    if (msgs[i] && msgs[i].role === 'assistant' && msgs[i].status === 'running') return msgs[i];
  }
  return null;
}

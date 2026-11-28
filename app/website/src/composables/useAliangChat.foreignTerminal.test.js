// 外来终态关账门控（2026-11-28 三审 F4）：ai.done/ai.error 未命中会话池的帧
// 经活跃兜底白名单（B5）挂靠活跃会话时，只允许关闭渲染中的 running 泡，
// 不得 clearApprovalsForSession / finishRun 关活跃会话的账——否则删除在跑
// 会话后的迟到终态（origin 直投不看订阅）会把新活跃会话的审批卡清掉、
// 输入区提前解锁（答案裂泡+假解锁复合竞态）。门控口径对齐 ai.session.closed
// 的 matchedById 先例 + 在途发送归属豁免（stale id 自端发送仍要清）。
import { beforeEach, describe, expect, it, vi } from 'vitest';

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

import { useAliangChat } from './useAliangChat';

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

async function mountRunningSession() {
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
  };
  chat.conversations.value.push(conv);
  chat.activeId.value = 'c1';
  const stopping = chat.stopRun();
  const sock = lastSock();
  sock.readyState = FakeWebSocket.OPEN;
  sock.onopen();
  await flushMicrotasks();
  await stopping;
  return { chat, conv, sock };
}

function frame(sock, payload) {
  sock.onmessage({ data: JSON.stringify(payload) });
}

function foreignCard() {
  return { key: 'k-foreign', sessionId: 'sess-foreign', status: 'pending', tool: 'Bash', summary: '外来卡' };
}

describe('F4 外来终态关账门控', () => {
  beforeEach(() => {
    localStorage.clear();
    FakeWebSocket.instances.length = 0;
    globalThis.WebSocket = FakeWebSocket;
  });

  it('未命中会话池的 ai.done：保留渲染关泡，精确清外来卡', async () => {
    const { conv, sock } = await mountRunningSession();
    conv.approvals = [foreignCard()];
    frame(sock, { type: 'ai.done', session_id: 'sess-foreign' });
    await flushMicrotasks();
    // 渲染腿保留：兜底关泡（自愈行为，真终态到达前不悬挂）
    expect(conv.messages[1].status).toBe('done');
    // 清卡按 e.session_id 精确清（评审 R1 精化）：外来终态清掉它自己的外来卡
    // （③兜底挂靠的卡随其 run 终结正常退场），但不动活跃会话自己的卡
    expect(conv.approvals).toHaveLength(0);
  });

  it('未命中会话池的 ai.error(message_id 形)：精确清外来卡', async () => {
    const { conv, sock } = await mountRunningSession();
    conv.approvals = [foreignCard()];
    frame(sock, { type: 'ai.error', session_id: 'sess-foreign', message_id: 'm-foreign', error: 'boom' });
    await flushMicrotasks();
    expect(conv.approvals).toHaveLength(0);
  });

  it('命中会话池的 ai.done（matchedById）：清卡关账行为不变', async () => {
    const { conv, sock } = await mountRunningSession();
    conv.approvals = [{ key: 'k-z', sessionId: 'sess-z', status: 'pending', tool: 'Bash', summary: '本会话卡' }];
    frame(sock, { type: 'ai.done', session_id: 'sess-z' });
    await flushMicrotasks();
    expect(conv.running).toBe(false);
    expect(conv.approvals).toHaveLength(0);
  });
});

describe('F4-R 在途发送保护（评审 major：终态腿不得复位在途发送）', () => {
  beforeEach(() => {
    localStorage.clear();
    FakeWebSocket.instances.length = 0;
    globalThis.WebSocket = FakeWebSocket;
  });

  // 在途发送的真实形态 = idle 会话走 create+ai.message 链（sendMessage 置
  // sending/sendingConvId 归发起会话）。外来终态经 B5 白名单挂靠 → 未门控的
  // finishRun 复位 sending → 快照对账的在途守卫（isOwnSendInFlight）失效 →
  // 下一枚 matched idle 快照误收敛在跑回合（假 done/丢 delta）。修复后：
  // 外来终态不动 sending，快照被在途守卫挡住。
  async function mountIdleWithInFlightSend() {
    const chat = useAliangChat();
    const conv = {
      id: 'c1', title: '', provider: 'agentcore', model: 'glm-x', aiSessionId: 'sess-z',
      running: false, runActive: false,
      messages: [
        { role: 'user', content: '旧问题', status: 'done', ts: 1, messageId: 'm-u0' },
        { role: 'assistant', content: '旧回答', status: 'done', ts: 2 },
      ],
    };
    chat.conversations.value.push(conv);
    const bootstrap = {
      id: 'c0', title: '', provider: 'agentcore', model: 'glm-x', aiSessionId: 'sess-0',
      running: true, runActive: true,
      messages: [{ role: 'assistant', content: '', status: 'running', ts: 0 }],
    };
    chat.conversations.value.push(bootstrap);
    chat.activeId.value = 'c0';
    const boot = chat.stopRun();
    const sock = lastSock();
    sock.readyState = FakeWebSocket.OPEN;
    sock.onopen();
    await flushMicrotasks();
    await boot;
    chat.activeId.value = 'c1';
    chat.inputText.value = '在途发送';
    const sending = chat.sendMessage();
    await flushMicrotasks();
    frame(sock, { type: 'ai.session.created', session_id: 'sess-z' });
    await flushMicrotasks();
    await sending;
    return { chat, conv, sock };
  }

  it('外来 ai.done 不复位在途发送：matched idle 快照不得收敛在跑泡', async () => {
    const { conv, sock } = await mountIdleWithInFlightSend();
    frame(sock, { type: 'ai.done', session_id: 'sess-foreign' });
    await flushMicrotasks();
    frame(sock, {
      type: 'ai.session.snapshot', session_id: 'sess-z', status: 'idle',
      transcript: [
        { role: 'user', text: '旧问题', message_id: 'm-u0', ts: 1 },
        { role: 'assistant', text: '旧回答', message_id: 'old-a', ts: 2 },
      ],
    });
    await flushMicrotasks();
    const bubbles = conv.messages.filter(m => m.role === 'assistant');
    expect(bubbles.at(-1)?.status).toBe('running');
    expect(conv.messages.filter(m => m.role === 'user').at(-1)?.content).toBe('在途发送');
  });

  it('外来 ai.status 终态不污染本端在途占位泡正文（二轮评审 major 探针）', async () => {
    const { conv, sock } = await mountIdleWithInFlightSend();
    frame(sock, {
      type: 'ai.status', session_id: 'sess-foreign', status: 'stopped',
      error: '外来停止原因',
    });
    await flushMicrotasks();
    const bubble = conv.messages.filter(m => m.role === 'assistant').at(-1);
    expect(bubble.status).toBe('running');
    expect(bubble.content).toBe('');
  });

  it('外来 ai.error(message_id) 不拆本端在途占位泡/不种外来失败条（二轮评审 major）', async () => {
    const { conv, sock } = await mountIdleWithInFlightSend();
    frame(sock, {
      type: 'ai.error', session_id: 'sess-foreign', message_id: 'assistant_m-foreign',
      error: '外来回合失败',
    });
    await flushMicrotasks();
    const bubbles = conv.messages.filter(m => m.role === 'assistant' && m.kind !== 'system');
    expect(bubbles.at(-1)?.status).toBe('running');
    expect(conv.messages.some(m => m?.note === 'turn_failed')).toBe(false);
  });

  it('外来终态 ai.status(stopped) 同款保护（评审实测的未门控腿）', async () => {
    const { conv, sock } = await mountIdleWithInFlightSend();
    frame(sock, { type: 'ai.status', session_id: 'sess-foreign', status: 'stopped' });
    await flushMicrotasks();
    frame(sock, {
      type: 'ai.session.snapshot', session_id: 'sess-z', status: 'idle',
      transcript: [
        { role: 'user', text: '旧问题', message_id: 'm-u0', ts: 1 },
        { role: 'assistant', text: '旧回答', message_id: 'old-a', ts: 2 },
      ],
    });
    await flushMicrotasks();
    const bubbles = conv.messages.filter(m => m.role === 'assistant');
    expect(bubbles.at(-1)?.status).toBe('running');
    expect(conv.messages.filter(m => m.role === 'user').at(-1)?.content).toBe('在途发送');
  });
});

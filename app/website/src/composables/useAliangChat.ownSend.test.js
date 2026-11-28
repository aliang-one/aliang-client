// 在途发送归属谓词统一（2026-11-28 三审 F3）：B4 之后「本端发送」的归属
// 权威是 sendingConvId（发起会话），页面级 `conv.id === activeId && sending`
// 形态只在「正看着发起会话」时等价——发送后切走会话即漂移：
//   - run.started 的 ownPending 误判外来 → 本端在途占位泡被关账（裂泡根因，
//     空 done 泡随 persist 永久落盘）；
//   - 快照对账 inFlight 守卫对后台在途发送失效；
//   - steer 降级守卫误放行。
// 本文件钉主导站点（run.started ownPending）；三站点共用同一归属助手
// isOwnSendInFlight，其余两站点由既有快照/steer 测试面回归保护。
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

function frame(sock, payload) {
  sock.onmessage({ data: JSON.stringify(payload) });
}

// sendAI 在非 OPEN 时静默丢帧——先借一个临时 running 会话 stopRun 建起
// socket（ensureAISocket 借道，同 auditBatch1 夹具），再切到空闲会话发送。
async function openSocket(chat) {
  const bootstrap = {
    id: 'c0', title: '', provider: 'agentcore', model: 'glm-x', aiSessionId: 'sess-0',
    running: true, runActive: true,
    messages: [{ role: 'assistant', content: '', status: 'running', ts: 0 }],
  };
  chat.conversations.value.push(bootstrap);
  chat.activeId.value = 'c0';
  const stopping = chat.stopRun();
  const sock = lastSock();
  sock.readyState = FakeWebSocket.OPEN;
  sock.onopen();
  await flushMicrotasks();
  await stopping;
  return sock;
}

describe('F3 在途发送归属谓词', () => {
  beforeEach(() => {
    localStorage.clear();
    FakeWebSocket.instances.length = 0;
    globalThis.WebSocket = FakeWebSocket;
  });

  it('发送后切走会话：run.started 不得关掉本端在途占位泡（不裂泡）', async () => {
    const chat = useAliangChat();
    const convA = {
      id: 'c1', title: '', provider: 'agentcore', model: 'glm-x', aiSessionId: 'sess-a',
      running: false, runActive: false,
      messages: [
        { role: 'user', content: '旧问题', status: 'done', ts: 1, messageId: 'm-u0' },
        { role: 'assistant', content: '旧回答', status: 'done', ts: 2 },
      ],
    };
    const convB = {
      id: 'c2', title: '', provider: 'agentcore', model: 'glm-x', aiSessionId: 'sess-b',
      running: false, runActive: false,
      messages: [],
    };
    chat.conversations.value.push(convA, convB);
    const sock = await openSocket(chat);
    chat.activeId.value = 'c1';

    // 本端在 c1 发送（真实流程：sending/sendingConvId 归 c1 + 占位泡）
    chat.inputText.value = '后台跑';
    const sending = chat.sendMessage();
    await flushMicrotasks();
    frame(sock, { type: 'ai.session.created', session_id: 'sess-a' });
    await flushMicrotasks();
    await sending;

    // 切走后 run.started 才到达（三审实证触发序）
    chat.activeId.value = 'c2';
    frame(sock, { type: 'ai.run.started', session_id: 'sess-a', message_id: 'm-u1' });
    await flushMicrotasks();

    const bubbles = convA.messages.filter(m => m.role === 'assistant');
    // 修复前：ownPending=false → 占位泡被关账 + 另产新泡（两条 assistant，
    // 占位条恒为空 done 泡永久落盘）；修复后：占位泡按复用认领（一条且 running）。
    expect(bubbles).toHaveLength(2); // 旧回答 + 复用认领的占位泡
    expect(bubbles[0].status).toBe('done');
    expect(bubbles[1].status).toBe('running');
    expect(convA.runActive).toBe(true);
    expect(convA.runningTurnMessageId).toBe('m-u1');
  });

  it('未切换场景行为不变：run.started 照常认领占位泡', async () => {
    const chat = useAliangChat();
    const convA = {
      id: 'c1', title: '', provider: 'agentcore', model: 'glm-x', aiSessionId: 'sess-a',
      running: false, runActive: false,
      messages: [
        { role: 'user', content: '旧问题', status: 'done', ts: 1, messageId: 'm-u0' },
        { role: 'assistant', content: '旧回答', status: 'done', ts: 2 },
      ],
    };
    chat.conversations.value.push(convA);
    const sock = await openSocket(chat);
    chat.activeId.value = 'c1';

    chat.inputText.value = '前台跑';
    const sending = chat.sendMessage();
    await flushMicrotasks();
    frame(sock, { type: 'ai.session.created', session_id: 'sess-a' });
    await flushMicrotasks();
    await sending;

    frame(sock, { type: 'ai.run.started', session_id: 'sess-a', message_id: 'm-u1' });
    await flushMicrotasks();

    const bubbles = convA.messages.filter(m => m.role === 'assistant');
    expect(bubbles).toHaveLength(2); // 旧回答 + 本回合泡
    expect(bubbles[1].status).toBe('running');
  });
});

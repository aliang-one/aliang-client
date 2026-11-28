// 壳看门狗盲杀修复（2026-11-28 三审 F2）：
// ①探测宽限豁免收窄——`!conv.deviceSession` 把所有设备壳挡在「快照探测优先」
//   之外，长静默（慢工具/手机端审批等待）直接盲杀；可订阅壳（agentcore 裸
//   id）与本地会话同样有权威快照通道，应享受同款探测宽限。
// ②壳回包喂狗点——探测/订阅的 running 快照回包走 handleDeviceSnapshot 壳
//   分支，此前无喂狗点（reconcileLocalSnapshot 只服务本地会话），狗到期即杀。
// 兜底保留：连续 IDLE_PROBE_LIMIT 个窗口无任何回包仍落原判（socket 黑洞）。
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest';

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

function sentTypes(sock) {
  return sock.sent.map((raw) => {
    try {
      return JSON.parse(raw).type;
    } catch (_) {
      return 'malformed';
    }
  });
}

function snapshotRows(n) {
  const rows = [];
  for (let i = 0; i < n; i++) {
    rows.push({ role: i % 2 ? 'assistant' : 'user', text: `snap-${i}`, message_id: `sm-${i}`, ts: i + 1 });
  }
  return rows;
}

describe('F2 壳看门狗探测宽限+喂狗点', () => {
  beforeEach(() => {
    localStorage.clear();
    FakeWebSocket.instances.length = 0;
    globalThis.WebSocket = FakeWebSocket;
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  async function mountShellWithInFlightSend() {
    const chat = useAliangChat();
    const shell = {
      id: 'shell-1', title: '', provider: 'agentcore', model: 'glm-x', aiSessionId: 'sess-dev',
      running: false, runActive: false,
      deviceSession: { id: 'sess-dev', name: 'dev' },
      messages: [
        { role: 'user', content: '旧问题', status: 'done', ts: 1, messageId: 'dm-0' },
        { role: 'assistant', content: '旧回答', status: 'done', ts: 2 },
      ],
    };
    chat.conversations.value.push(shell);
    chat.activeId.value = 'shell-1';
    // 建 socket（借 ensureAISocket 建立链：sendMessage 自身即可，先手动 OPEN）
    chat.inputText.value = '壳上续聊';
    const sending = chat.sendMessage();
    await flushMicrotasks();
    const sock = lastSock();
    sock.readyState = FakeWebSocket.OPEN;
    sock.onopen();
    await flushMicrotasks();
    frame(sock, { type: 'ai.session.created', session_id: 'sess-dev' });
    await flushMicrotasks();
    await sending;
    return { chat, shell, sock };
  }

  it('可订阅壳长静默：先快照探测而非盲杀，running 回包喂狗续命', async () => {
    const { shell, sock } = await mountShellWithInFlightSend();
    const bubble = () => shell.messages.filter(m => m.role === 'assistant').at(-1);
    expect(bubble().status).toBe('running');
    expect(sentTypes(sock).filter(t => t === 'ai.session.subscribe')).toHaveLength(0);

    // 第 1 个 90s 窗口：狗到期 → 快照探测（修复前：壳被豁免直接盲杀）
    await vi.advanceTimersByTimeAsync(90000);
    expect(bubble().status).toBe('running'); // 未被杀
    const probes = sentTypes(sock).filter(t => t === 'ai.session.subscribe');
    expect(probes.length).toBeGreaterThanOrEqual(1);

    // 探测回包：running 快照 → 喂狗（清探测计数+再武装）
    frame(sock, { type: 'ai.session.snapshot', session_id: 'sess-dev', status: 'running', transcript: snapshotRows(4) });
    await flushMicrotasks();
    expect(bubble().status).toBe('running');

    // 喂狗后再等一个窗口：仍活着（计数已归零），第二轮探测发出
    await vi.advanceTimersByTimeAsync(90000);
    expect(bubble().status).toBe('running');
    expect(sentTypes(sock).filter(t => t === 'ai.session.subscribe').length)
      .toBeGreaterThan(probes.length);
  });

  it('连续两个窗口无回包：兜底盲杀保留（socket 黑洞判死不豁免）', async () => {
    const { shell, sock } = await mountShellWithInFlightSend();
    const bubble = () => shell.messages.filter(m => m.role === 'assistant').at(-1);
    // 窗口1、窗口2：各发一次探测，全程无回包（socket 黑洞）
    await vi.advanceTimersByTimeAsync(90000);
    expect(bubble().status).toBe('running');
    await vi.advanceTimersByTimeAsync(90000);
    expect(bubble().status).toBe('running');
    const probes = sentTypes(sock).filter(t => t === 'ai.session.subscribe');
    expect(probes.length).toBe(2);
    // 窗口3：探测额度用尽（IDLE_PROBE_LIMIT=2）→ 落原判兜底
    await vi.advanceTimersByTimeAsync(90000);
    expect(bubble().status).toBe('error');
  });
});

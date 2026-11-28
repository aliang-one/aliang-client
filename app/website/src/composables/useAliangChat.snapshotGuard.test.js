// 快照降级守卫（2026-11-28 三审 F1）：订阅握手快照（journal 切片，服务端帽
// 200 行）晚于详情分页拉全（≤500 行）必然后到——handleDeviceSnapshot 的
// `if (rows.length)` 整体替换会把刚拉全的详情静默降级到 200 行（agentcore
// 会话实际展示帽=200 行，~100 回合）。守卫口径：idle 且已装详情（deviceLoadedAt
// 在）的壳，更小的快照不整体替换（delta/终态重拉既有链路继续负责更新）；
// 未装详情的壳与跑动中的壳（快照是在跑窗口的唯一追赶通道）行为不变。
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
import { getAgentSessionDetail } from '../services/agentApi';

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

function detailRows(n) {
  const rows = [];
  for (let i = 0; i < n; i++) {
    rows.push({ role: i % 2 ? 'assistant' : 'user', content: `detail-${i}`, id: `dm-${i}`, seq: i + 1, ts: i + 1 });
  }
  return rows;
}

// 详情行 id 域 dm-0..dm-(n-1)/seq 1..n；快照=journal 尾部切片 → id/seq 落在
// 详情域尾部（握手快照紧跟详情的确定时序）。追赶（本地缺新尾巴）用独立
// 高位 id/seq 域；守卫判据=快照末行 seq vs 本地最大 seq。
function snapshotRows(n, prefix = 'snap', idBase = 300) {
  const rows = [];
  for (let i = 0; i < n; i++) {
    rows.push({ role: i % 2 ? 'assistant' : 'user', text: `${prefix}-${i}`, message_id: `dm-${idBase - n + i}`, seq: idBase - n + i + 1, ts: i + 1 });
  }
  return rows;
}

function makeShell({ id = 'shell-1', aiSessionId = 'sess-dev', running = false } = {}) {
  return {
    id,
    title: '',
    provider: 'agentcore',
    model: 'glm-x',
    aiSessionId,
    running,
    runActive: running,
    deviceSession: { id: aiSessionId, name: 'dev' },
    messages: [],
  };
}

async function boot(sockReady = true) {
  localStorage.clear();
  FakeWebSocket.instances.length = 0;
  globalThis.WebSocket = FakeWebSocket;
  const chat = useAliangChat();
  let sock = null;
  if (sockReady) {
    // 借 ensureAISocket 建 socket：直接订阅一次打开流程外的探针太绕，用
    // chat 内部 sock 生命周期之外的等价物——sendAI 前必须有 OPEN socket，
    // 这里借 running 壳 stopRun 同款（auditBatch1 夹具）。
    const bootstrap = {
      id: 'c0', title: '', provider: 'agentcore', model: 'glm-x', aiSessionId: 'sess-0',
      running: true, runActive: true,
      messages: [{ role: 'assistant', content: '', status: 'running', ts: 0 }],
    };
    chat.conversations.value.push(bootstrap);
    chat.activeId.value = 'c0';
    const stopping = chat.stopRun();
    sock = lastSock();
    sock.readyState = FakeWebSocket.OPEN;
    sock.onopen();
    await flushMicrotasks();
    await stopping;
  }
  return { chat, sock };
}

describe('F1 快照降级守卫', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('idle 壳已装详情（500 行）：更小的握手快照不整体替换', async () => {
    getAgentSessionDetail.mockResolvedValue({
      transcript: detailRows(300),
      transcript_page: { has_more: false },
    });
    const { chat, sock } = await boot(true);
    const shell = makeShell();
    chat.conversations.value.unshift(shell);
    await chat.openDeviceSession(shell);
    await flushMicrotasks();
    const afterDetail = shell.messages.length;
    expect(afterDetail).toBeGreaterThan(200);

    frame(sock, { type: 'ai.session.snapshot', session_id: 'sess-dev', status: 'idle', transcript: snapshotRows(20) });
    await flushMicrotasks();
    // 修复前：整体替换 → messages 被降级到 20 行上下；修复后：末行 id 命中
    // 本地（本地已是快照超集）→ 保住详情全量
    expect(shell.messages.length).toBe(afterDetail);
    expect(shell.messages[0].content).toBe('detail-0');
  });

  it('断线追赶（快照末行不在本地）：放行替换，守卫不关死补账通道', async () => {
    getAgentSessionDetail.mockResolvedValue({
      transcript: detailRows(300),
      transcript_page: { has_more: false },
    });
    const { chat, sock } = await boot(true);
    const shell = makeShell({ id: 'shell-4' });
    chat.conversations.value.unshift(shell);
    await chat.openDeviceSession(shell);
    await flushMicrotasks();
    const afterDetail = shell.messages.length;
    // 别端新跑了一轮：快照尾部是新 id（dm-5xx，不在本地详情域 dm-0..299）
    frame(sock, { type: 'ai.session.snapshot', session_id: 'sess-dev', status: 'idle', transcript: snapshotRows(20, 'new', 520) });
    await flushMicrotasks();
    expect(shell.messages.length).toBeLessThanOrEqual(21);
    expect(shell.messages.some(m => (m.content || '').startsWith('new-'))).toBe(true);
    expect(shell.messages.length).not.toBe(afterDetail);
  });

  it('未装详情的壳：快照照常整体替换（既有行为不变）', async () => {
    const { chat, sock } = await boot(true);
    const shell = makeShell({ id: 'shell-2' });
    chat.conversations.value.unshift(shell);
    frame(sock, { type: 'ai.session.snapshot', session_id: 'sess-dev', status: 'idle', transcript: snapshotRows(20) });
    await flushMicrotasks();
    expect(shell.messages.length).toBeGreaterThan(0);
    expect(shell.messages.length).toBeLessThanOrEqual(21);
  });

  it('快照末行=系统折行（无 message_id 带 seq）：seq 判据照常判降级跳过', async () => {
    getAgentSessionDetail.mockResolvedValue({
      transcript: detailRows(300),
      transcript_page: { has_more: false },
    });
    const { chat, sock } = await boot(true);
    const shell = makeShell({ id: 'shell-5' });
    chat.conversations.value.unshift(shell);
    await chat.openDeviceSession(shell);
    await flushMicrotasks();
    const afterDetail = shell.messages.length;
    expect(afterDetail).toBeGreaterThan(200);
    // 失败收尾会话：末行=end(error) 系统折行（无 message_id，seq=300）
    frame(sock, {
      type: 'ai.session.snapshot', session_id: 'sess-dev', status: 'idle',
      transcript: [...snapshotRows(19), { role: 'system', text: '执行失败', seq: 300 }],
    });
    await flushMicrotasks();
    expect(shell.messages.length).toBe(afterDetail);
    expect(shell.messages[0].content).toBe('detail-0');
  });

  it('journal 收缩（撤回截断，快照末行 seq < 本地最大）：放行替换', async () => {
    getAgentSessionDetail.mockResolvedValue({
      transcript: detailRows(300),
      transcript_page: { has_more: false },
    });
    const { chat, sock } = await boot(true);
    const shell = makeShell({ id: 'shell-6' });
    chat.conversations.value.unshift(shell);
    await chat.openDeviceSession(shell);
    await flushMicrotasks();
    // 截断后 journal 尾部切片：末行 seq=150（< 本地最大 300）
    const truncated = [];
    for (let i = 0; i < 20; i++) {
      truncated.push({ role: i % 2 ? 'assistant' : 'user', text: `cut-${i}`, message_id: `dm-${130 + i}`, seq: 131 + i, ts: i + 1 });
    }
    frame(sock, { type: 'ai.session.snapshot', session_id: 'sess-dev', status: 'idle', transcript: truncated });
    await flushMicrotasks();
    expect(shell.messages.length).toBeLessThanOrEqual(21);
    expect(shell.messages.some(m => (m.content || '').startsWith('cut-'))).toBe(true);
  });

  it('跑动中的壳：更小的快照仍允许替换（在跑窗口唯一追赶通道）', async () => {
    getAgentSessionDetail.mockResolvedValue({
      transcript: detailRows(300),
      transcript_page: { has_more: false },
    });
    const { chat, sock } = await boot(true);
    const shell = makeShell({ id: 'shell-3', running: true });
    chat.conversations.value.unshift(shell);
    await chat.openDeviceSession(shell);
    await flushMicrotasks();
    // 跑动中 openDeviceSession 跳过重拉（D1 流式守卫）——手动模拟已装详情形态
    shell.deviceLoadedAt = Date.now();
    shell.messages = shell.messages.length ? shell.messages : detailRows(300).map(r => ({
      role: r.role, content: r.content, status: 'done', ts: r.ts,
    }));
    expect(shell.messages.length).toBeGreaterThan(200);

    frame(sock, { type: 'ai.session.snapshot', session_id: 'sess-dev', status: 'running', transcript: snapshotRows(20) });
    await flushMicrotasks();
    expect(shell.messages.length).toBeLessThanOrEqual(21);
  });
});

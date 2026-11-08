import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest';

// agentApi 整体 mock（连带绕开 request 的 token 链）：getAgentSessionDetail
// 让历史装配/断线重拉走真实 ingest 链并断言调用与消费。
vi.mock('../services/agentApi', () => ({
  getAgentIntegrations: vi.fn(),
  getAgentSessions: vi.fn(),
  getAgentSessionDetail: vi.fn(),
  uploadChatAttachment: vi.fn(),
}));

// useDeviceSessions 用真实现（清单拉取已被 agentApi mock 劫持），但
// composable 内部实例与测试句柄必须同源——模块级单例桥接（shellCache/
// deviceSessions 共享），测试才能拿到与 openDeviceSession 相同的壳对象。
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

import { getAgentSessions, getAgentSessionDetail } from '../services/agentApi';
import { useAliangChat } from './useAliangChat';
import { useDeviceSessions } from './useDeviceSessions';

// Fake WebSocket：readyState 手控、实例句柄留在类上，测试手动驱动
// onopen（resolve ensureAISocket 的 promise）与 onclose（触发重连自愈）。
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

// 微任务冲刷：async 链（详情 mock→装配循环→ensureAISocket→refresh）全部
// 走 promise，不依赖 timer；20 轮足以覆盖最深的链路。
async function flushMicrotasks() {
  for (let i = 0; i < 20; i++) await Promise.resolve();
}

// 打开一个可订阅的 agentcore 历史设备会话（裸 id）：清单 mock→load→壳→
// onSidebarItemClick（分流 openDeviceSession）。返回壳与已建 socket 句柄。
async function openHistorySession(chat, sessionId, detail) {
  getAgentSessions.mockResolvedValue({
    sessions: [{ id: sessionId, provider: 'agentcore', status: 'idle', title: '历史' + sessionId, project_path: '/p' }],
  });
  getAgentSessionDetail.mockResolvedValue(detail);
  const handles = useDeviceSessions();
  await handles.loadDeviceSessions();
  const entry = handles.deviceSessions.value.find((s) => s.id === sessionId);
  const shell = handles.shellForEntry(entry);
  const opened = chat.onSidebarItemClick(shell);
  await flushMicrotasks(); // 详情分页完成→装配→ensureAISocket 建连
  const sock = FakeWebSocket.instances.at(-1);
  sock.readyState = FakeWebSocket.OPEN;
  sock.onopen();
  await flushMicrotasks(); // openDeviceSession 尾段（订阅发送）走完
  await opened;
  return { shell, live: chat.conversations.value.find((c) => c.id === shell.id) };
}

// 详情响应夹具：转录两行 + 一条已完成子agent（顶层会话级字段）
const historyDetail = (sessionId) => ({
  id: sessionId,
  transcript: [
    { id: 'm1', role: 'user', content: '历史提问', timestamp: '2026-11-07T01:00:00Z' },
    { id: 'm2', role: 'assistant', content: '历史回答', timestamp: '2026-11-07T01:00:04Z' },
  ],
  subagents: [{
    task_id: 't1',
    name: 'code-reviewer',
    status: 'completed',
    summary: '全部通过',
    result: '全绿报告',
    total_tokens: 900,
    duration_ms: 4200,
    tools: [{ tool: 'Bash', summary: 'npm test', is_error: false }],
  }],
});

describe('openDeviceSession 历史装配（详情 subagents → 面板状态，转录不落壳）', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    FakeWebSocket.instances.length = 0;
    globalThis.WebSocket = FakeWebSocket;
  });

  it('详情带 subagents[]：打开后面板状态有数据（归一为 reducer 形状），messages 只有转录行', async () => {
    const sid = 'sess-hist-1';
    const chat = useAliangChat();
    const { live } = await openHistorySession(chat, sid, historyDetail(sid));

    expect(getAgentSessionDetail).toHaveBeenCalledWith(sid, expect.objectContaining({ limit: expect.any(Number) }));
    // 面板状态入库：会话级键（裸 id = aiSessionId）下一条归一条目
    expect(chat.subagentsBySession[sid]).toHaveLength(1);
    expect(chat.subagentsBySession[sid][0]).toMatchObject({
      taskId: 't1',
      name: 'code-reviewer',
      phase: 'completed',
      summary: '全部通过',
      result: '全绿报告',
    });
    // 转录不落壳：messages 只有详情转录两行，subagent 载荷绝不混进
    expect(live.messages.map((m) => m.content)).toEqual(['历史提问', '历史回答']);
    expect(JSON.stringify(live.messages)).not.toContain('code-reviewer');
    expect(JSON.stringify(live.messages)).not.toContain('全绿报告');
    // 活跃 getter 同源可见（入口条/面板的真实消费路径）
    expect(chat.activeSubagents.value).toHaveLength(1);
  });

  it('TTL 命中（5 分钟内重复打开）不重复拉详情也不重复装配', async () => {
    const sid = 'sess-hist-2';
    const chat = useAliangChat();
    await openHistorySession(chat, sid, historyDetail(sid));
    const first = chat.conversations.value[0];
    expect(first.deviceLoadedAt).toBeGreaterThan(0);
    getAgentSessionDetail.mockClear();
    // 同壳再开：TTL 内跳过详情，面板状态保持原值不重写
    await chat.openDeviceSession(first);
    expect(getAgentSessionDetail).not.toHaveBeenCalled();
    expect(chat.subagentsBySession[sid]).toHaveLength(1);
  });

  it('详情缺 subagents 键（后端降级）：装配静默跳过，面板状态不被覆写为空', async () => {
    const sid = 'sess-hist-3';
    const chat = useAliangChat();
    await openHistorySession(chat, sid, { transcript: [{ id: 'm1', role: 'user', content: '只有转录' }] });
    expect(chat.subagentsBySession[sid]).toBeUndefined();
    expect(chat.conversations.value[0].messages.map((m) => m.content)).toEqual(['只有转录']);
  });
});

describe('WS 断线重连自愈（重连成功 → 已订阅会话补一次子agent详情刷新）', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    FakeWebSocket.instances.length = 0;
    globalThis.WebSocket = FakeWebSocket;
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('断连窗口内完成的子agent：重连成功后经详情重拉恢复到面板状态', async () => {
    const sid = 'sess-reconn-1';
    const chat = useAliangChat();
    // 打开时（首拉）还没有子agent；断线后（重拉）子agent 已完成
    getAgentSessions.mockResolvedValue({
      sessions: [{ id: sid, provider: 'agentcore', status: 'idle', title: 'r', project_path: '/p' }],
    });
    getAgentSessionDetail
      .mockResolvedValueOnce({ id: sid, transcript: [{ id: 'm1', role: 'user', content: '问', timestamp: '' }], subagents: [] })
      .mockResolvedValueOnce({ id: sid, transcript: [], subagents: historyDetail(sid).subagents });
    const handles = useDeviceSessions();
    await handles.loadDeviceSessions();
    const entry = handles.deviceSessions.value.find((s) => s.id === sid);
    const shell = handles.shellForEntry(entry);
    const opened = chat.onSidebarItemClick(shell);
    await flushMicrotasks();
    const firstSock = FakeWebSocket.instances.at(-1);
    firstSock.readyState = FakeWebSocket.OPEN;
    firstSock.onopen();
    await flushMicrotasks();
    await opened;

    expect(chat.subagentsBySession[sid]).toEqual([]); // 打开时确实没有
    expect(getAgentSessionDetail).toHaveBeenCalledTimes(1);

    // 断线：running 气泡让 onclose 的 affected 非空 → 走重连自愈路径
    const live = chat.conversations.value.find((c) => c.id === shell.id);
    live.messages.push({ role: 'assistant', content: '', status: 'running', ts: Date.now() });
    firstSock.onclose({ code: 1006, reason: '' });

    await vi.advanceTimersByTimeAsync(800); // 800ms 退避到点 → 重连
    const secondSock = FakeWebSocket.instances.at(-1);
    expect(secondSock).not.toBe(firstSock);
    secondSock.readyState = FakeWebSocket.OPEN;
    secondSock.onopen();
    await vi.advanceTimersByTimeAsync(0); // 重订阅 + 子agent重拉的 async 链走完

    // 重连成功 → 对已订阅会话触发一次详情刷新
    expect(getAgentSessionDetail).toHaveBeenCalledTimes(2);
    expect(getAgentSessionDetail).toHaveBeenLastCalledWith(sid);
    // 断连窗口内完成的子agent 经重拉恢复（mock 详情带 subagents → store 更新）
    expect(chat.subagentsBySession[sid]).toHaveLength(1);
    expect(chat.subagentsBySession[sid][0]).toMatchObject({ taskId: 't1', phase: 'completed' });
    // messages 未被污染：重拉只消费 subagents[]
    expect(live.messages.map((m) => m.content)).toEqual(['问', '']);
    expect(JSON.stringify(live.messages)).not.toContain('code-reviewer');
  });

  it('重连后重拉失败静默：面板既有状态保持，不断线不报错', async () => {
    const sid = 'sess-reconn-2';
    const chat = useAliangChat();
    getAgentSessions.mockResolvedValue({
      sessions: [{ id: sid, provider: 'agentcore', status: 'idle', title: 'r', project_path: '/p' }],
    });
    getAgentSessionDetail
      .mockResolvedValueOnce({ id: sid, transcript: [], subagents: [] })
      .mockRejectedValueOnce(new Error('boom'));
    const handles = useDeviceSessions();
    await handles.loadDeviceSessions();
    const entry = handles.deviceSessions.value.find((s) => s.id === sid);
    const shell = handles.shellForEntry(entry);
    const opened = chat.onSidebarItemClick(shell);
    await flushMicrotasks();
    const firstSock = FakeWebSocket.instances.at(-1);
    firstSock.readyState = FakeWebSocket.OPEN;
    firstSock.onopen();
    await flushMicrotasks();
    await opened;

    const live = chat.conversations.value.find((c) => c.id === shell.id);
    live.messages.push({ role: 'assistant', content: '', status: 'running', ts: Date.now() });
    firstSock.onclose({ code: 1006, reason: '' });
    await vi.advanceTimersByTimeAsync(800);
    const secondSock = FakeWebSocket.instances.at(-1);
    secondSock.readyState = FakeWebSocket.OPEN;
    secondSock.onopen();
    await vi.advanceTimersByTimeAsync(0);

    expect(getAgentSessionDetail).toHaveBeenCalledTimes(2);
    expect(chat.subagentsBySession[sid]).toEqual([]); // 静默保住既有状态
    expect(live.messages.map((m) => m.content)).toEqual(['']);
  });
});

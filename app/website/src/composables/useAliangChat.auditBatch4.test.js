// 网页聊天审计四批（拍板批 2026-11-21）：存疑 3 条按评审正方修法落地（各带
// 回归钉，评审反方论点见 TODOS/审计输出）：
//   D1 openDeviceSession TTL 重入：跑动中跳过重拉详情（整体替换丢在途回合
//      前缀且终态后无人回取）——权威对账交由快照/终态帧。
//   D2 子代理面板状态随会话切换复位（面板开着切走，面板仍显示旧会话内容、
//      watching 跨会话泄漏）——切会话即收面板。
//   D3 localStorage 跨 tab 后写覆盖前写——storage 事件按 updatedAt 逐会话
//      合并（新者胜、缺者补、本 tab 新草稿不丢）。
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { nextTick } from 'vue';

vi.mock('../services/agentApi', () => ({
  getAgentIntegrations: vi.fn(),
  getAgentSessions: vi.fn(),
  getAgentSessionDetail: vi.fn(),
  uploadChatAttachment: vi.fn(),
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

import { useAliangChat, DEVICE_SESSION_DETAIL_TTL_MS } from './useAliangChat';
import { getAgentSessionDetail } from '../services/agentApi';

// Fake WebSocket（openSocket 借道 ensureAISocket）
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

beforeEach(() => {
  localStorage.clear();
  FakeWebSocket.instances.length = 0;
  globalThis.WebSocket = FakeWebSocket;
  getAgentSessionDetail.mockReset();
});

describe('D1 openDeviceSession TTL 重入：跑动中跳过重拉详情', () => {
  function mountRunningShell(chat) {
    const shell = {
      id: 'dev-entry-1',
      title: '设备会话',
      provider: 'agentcore',
      aiSessionId: 'entry-1',
      deviceSession: { id: 'entry-1', status: 'running' },
      running: true,
      deviceLoadedAt: Date.now() - DEVICE_SESSION_DETAIL_TTL_MS - 1000, // TTL 已过期
      messages: [
        { role: 'user', content: '第一问', status: 'done', ts: 1 },
        { role: 'assistant', content: '已流出的前缀', status: 'running', ts: 2 },
      ],
    };
    chat.conversations.value.push(shell);
    chat.activeId.value = shell.id;
    return shell;
  }

  it('TTL 过期 + 会话在跑：不重拉详情，已流出前缀原样保留', async () => {
    const chat = useAliangChat();
    const shell = mountRunningShell(chat);
    // 先备好 socket（openDeviceSession 尾部 await ensureAISocket，否则挂到超时）
    const stopping = chat.stopRun();
    const sock = FakeWebSocket.instances.at(-1);
    sock.readyState = FakeWebSocket.OPEN;
    sock.onopen();
    await flushMicrotasks();
    await stopping;
    getAgentSessionDetail.mockResolvedValue({
      transcript: [
        { id: 'u1', role: 'user', content: '第一问' },
        { id: 'a1', role: 'assistant', content: 'journal 里的旧全文' },
      ],
      subagents: [],
    });
    await chat.openDeviceSession(shell);
    await flushMicrotasks();
    await flushMicrotasks();
    expect(getAgentSessionDetail).not.toHaveBeenCalled(); // 流式守卫：跳过重拉
    expect(shell.messages.some((m) => m.content === '已流出的前缀')).toBe(true);
    expect(shell.messages.some((m) => m.content === 'journal 里的旧全文')).toBe(false);
  });

  it('TTL 过期 + 会话空闲：照常重拉（回归：权威对账主路径不受影响）', async () => {
    const chat = useAliangChat();
    const shell = {
      id: 'dev-entry-2',
      title: '空闲设备会话',
      provider: 'agentcore',
      aiSessionId: 'entry-2',
      deviceSession: { id: 'entry-2', status: 'idle' },
      running: false,
      deviceLoadedAt: Date.now() - DEVICE_SESSION_DETAIL_TTL_MS - 1000,
      messages: [],
    };
    chat.conversations.value.push(shell);
    chat.activeId.value = shell.id;
    getAgentSessionDetail.mockResolvedValue({
      transcript: [
        { id: 'u1', role: 'user', content: '旧一问' },
        { id: 'a1', role: 'assistant', content: '旧一答' },
      ],
      subagents: [],
    });
    // openDeviceSession 尾部 await ensureAISocket：先跑详情段，再把途中建
    // 的 socket 打开，避免整链挂到超时。
    const opening = chat.openDeviceSession(shell);
    await flushMicrotasks();
    await flushMicrotasks();
    const sock = FakeWebSocket.instances.at(-1);
    if (sock && sock.readyState === FakeWebSocket.CONNECTING) {
      sock.readyState = FakeWebSocket.OPEN;
      sock.onopen();
    }
    await flushMicrotasks();
    await Promise.race([opening, flushMicrotasks()]);
    expect(getAgentSessionDetail).toHaveBeenCalled();
    expect(shell.messages.some((m) => m.content === '旧一答')).toBe(true);
  });
});

describe('D3 localStorage 跨 tab：storage 事件按 updatedAt 逐会话合并', () => {
  it('他 tab 写入的新会话补进本 tab、同 id 新者胜', async () => {
    const chatA = useAliangChat();
    // A tab：本地一个旧草稿
    chatA.conversations.value.push({
      id: 'c-local',
      title: 'A 的草稿',
      provider: 'agentcore',
      model: 'glm-x',
      aiSessionId: 'local-c-local',
      messages: [{ role: 'user', content: 'A 内容', status: 'done', ts: 1 }],
      updatedAt: 100,
    });
    // 他 tab 落盘：同 id 但 updatedAt 更新的版本 + 一个 A 没有的新会话
    const otherTabData = {
      conversations: [
        {
          id: 'c-local',
          title: 'B 改过的标题',
          provider: 'agentcore',
          model: 'glm-x',
          messages: [{ role: 'user', content: 'B 的新内容', status: 'done', ts: 1 }],
          updatedAt: 200,
        },
        {
          id: 'c-from-b',
          title: 'B 新建的会话',
          provider: 'agentcore',
          model: 'glm-x',
          messages: [{ role: 'user', content: 'B 会话内容', status: 'done', ts: 1 }],
          updatedAt: 300,
        },
      ],
      activeConversationId: null,
    };
    window.dispatchEvent(
      new StorageEvent('storage', { key: 'aliang-chat-data', newValue: JSON.stringify(otherTabData) }),
    );
    await flushMicrotasks();
    const local = chatA.conversations.value.find((c) => c.id === 'c-local');
    expect(local.title).toBe('B 改过的标题'); // 新者胜
    expect(local.messages[0].content).toBe('B 的新内容');
    const fromB = chatA.conversations.value.find((c) => c.id === 'c-from-b');
    expect(fromB).toBeTruthy(); // 缺者补
    // 本 tab 独有草稿不丢
    expect(chatA.conversations.value.some((c) => c.id === 'c-local')).toBe(true);
  });

  it('本 tab 更新（updatedAt 更新）不被他 tab 旧值回滚', async () => {
    const chat = useAliangChat();
    chat.conversations.value.push({
      id: 'c-mine',
      title: '本 tab 新标题',
      provider: 'agentcore',
      model: 'glm-x',
      messages: [{ role: 'user', content: '本 tab 新内容', status: 'done', ts: 1 }],
      updatedAt: 500,
    });
    const staleOtherTab = {
      conversations: [
        {
          id: 'c-mine',
          title: '他 tab 旧标题',
          provider: 'agentcore',
          model: 'glm-x',
          messages: [{ role: 'user', content: '旧内容', status: 'done', ts: 1 }],
          updatedAt: 100,
        },
      ],
      activeConversationId: null,
    };
    window.dispatchEvent(
      new StorageEvent('storage', { key: 'aliang-chat-data', newValue: JSON.stringify(staleOtherTab) }),
    );
    await flushMicrotasks();
    const mine = chat.conversations.value.find((c) => c.id === 'c-mine');
    expect(mine.title).toBe('本 tab 新标题');
    expect(mine.messages[0].content).toBe('本 tab 新内容');
  });

  it('非本 key 的 storage 事件与坏 JSON 忽略', async () => {
    const chat = useAliangChat();
    const before = chat.conversations.value.length;
    window.dispatchEvent(new StorageEvent('storage', { key: 'other-key', newValue: '{}' }));
    window.dispatchEvent(new StorageEvent('storage', { key: 'aliang-chat-data', newValue: 'not-json{{' }));
    await flushMicrotasks();
    expect(chat.conversations.value.length).toBe(before);
  });
});

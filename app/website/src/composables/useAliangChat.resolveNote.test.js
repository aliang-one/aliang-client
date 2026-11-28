// 已答回执留痕（2026-11-28 三审 F7 + 二轮评审重定口径）：卡面是瞬态（不落
// 盘，刷新即失），系统条是唯一跨刷新真值——无论卡是否在场（别端翻卡/自答
// 回投/无卡漏帧）都推「已选择」留痕条，瞬态期卡面+条并存是有意代价。去重
// 用独立 resolvedNoteKeys（键级；刻意不用 handledOptionIds 封键——option_id
// 是模型自写 slug 会跨回合重用，封键会把后续同 slug 合法卡拦死在 upsert 门），
// 也不用内容去重（不同提问同选型的第二条真留痕会被吞）。
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

async function mountChat() {
  const chat = useAliangChat();
  const conv = {
    id: 'c1', title: '', provider: 'agentcore', model: 'glm-x', aiSessionId: 'sess-z',
    running: false, runActive: false,
    messages: [{ role: 'user', content: 'q', status: 'done', ts: 1, messageId: 'm-u1' }],
  };
  // socket 引导（sendAI 非 OPEN 静默丢帧）：借临时 running 会话 stopRun 建链
  const bootstrap = {
    id: 'c0', title: '', provider: 'agentcore', model: 'glm-x', aiSessionId: 'sess-0',
    running: true, runActive: true,
    messages: [{ role: 'assistant', content: '', status: 'running', ts: 0 }],
  };
  chat.conversations.value.push(bootstrap, conv);
  chat.activeId.value = 'c0';
  const stopping = chat.stopRun();
  const sock = lastSock();
  sock.readyState = FakeWebSocket.OPEN;
  sock.onopen();
  await flushMicrotasks();
  await stopping;
  chat.activeId.value = 'c1';
  return { chat, conv, sock };
}

describe('F7 已答回执留痕（resolved 无卡推系统条）', () => {
  beforeEach(() => {
    localStorage.clear();
    FakeWebSocket.instances.length = 0;
    globalThis.WebSocket = FakeWebSocket;
  });

  it('option resolved 无卡：推「已选择」系统条（带 picked ids）', async () => {
    const { conv, sock } = await mountChat();
    frame(sock, {
      type: 'ai.option.request', status: 'resolved', session_id: 'sess-z',
      option_id: 'op1', selected: ['a', 'b'], custom_text: '',
    });
    await flushMicrotasks();
    const note = conv.messages.find(m => m.kind === 'system');
    expect(note).toBeTruthy();
    expect(note.content).toContain('a');
    expect(note.content).toContain('b');
  });

  it('tool_ask resolved 无卡：推系统条（picked+自定义文本）', async () => {
    const { conv, sock } = await mountChat();
    frame(sock, {
      type: 'tool_ask.request', status: 'resolved', session_id: 'sess-z',
      interaction_id: 'it1', answer: { selected_option_ids: ['a'], message: '就走这条' },
    });
    await flushMicrotasks();
    const note = conv.messages.find(m => m.kind === 'system');
    expect(note).toBeTruthy();
    expect(note.content).toContain('a');
    expect(note.content).toContain('就走这条');
  });

  it('同键 resolved 重复帧：只推一条（resolvedNoteKeys 键级去重）', async () => {
    const { conv, sock } = await mountChat();
    const payload = {
      type: 'ai.option.request', status: 'resolved', session_id: 'sess-z',
      option_id: 'op1', selected: ['a'], custom_text: '',
    };
    frame(sock, payload);
    frame(sock, payload);
    await flushMicrotasks();
    expect(conv.messages.filter(m => m.kind === 'system')).toHaveLength(1);
  });

  it('不同提问同选型：两条留痕都在（键级去重不吞真留痕）', async () => {
    const { conv, sock } = await mountChat();
    for (const ask of ['it1', 'it2']) {
      frame(sock, {
        type: 'tool_ask.request', status: 'resolved', session_id: 'sess-z',
        interaction_id: ask, answer: { selected_option_ids: ['a'], message: '' },
      });
      await flushMicrotasks();
    }
    expect(conv.messages.filter(m => m.kind === 'system')).toHaveLength(2);
  });

  it('无卡留痕后同 slug 新提问：照常出卡（评审 R4：不封键）', async () => {
    const { conv, sock } = await mountChat();
    frame(sock, {
      type: 'ai.option.request', status: 'resolved', session_id: 'sess-z',
      option_id: 'op1', selected: ['a'], custom_text: '',
    });
    await flushMicrotasks();
    expect(conv.messages.filter(m => m.kind === 'system')).toHaveLength(1);
    // 同会话后续回合模型重用同 slug（opt_yes 形态）：新提问必须照常出卡
    frame(sock, {
      type: 'ai.option.request', session_id: 'sess-z', option_id: 'op1',
      title: '再问', options: [{ id: 'a', label: 'A' }, { id: 'b', label: 'B' }],
    });
    await flushMicrotasks();
    expect(conv.optionCards).toHaveLength(1);
    expect(conv.optionCards[0].title).toBe('再问');
  });

  it('有卡翻卡：卡面+持久留痕条并存（条是唯一跨刷新真值）', async () => {
    const { chat, conv, sock } = await mountChat();
    frame(sock, {
      type: 'ai.option.request', session_id: 'sess-z', option_id: 'op1',
      title: '部署', options: [{ id: 'a', label: 'A' }, { id: 'b', label: 'B' }],
    });
    await flushMicrotasks();
    expect(conv.optionCards).toHaveLength(1);
    frame(sock, {
      type: 'ai.option.request', status: 'resolved', session_id: 'sess-z',
      option_id: 'op1', selected: ['a'], custom_text: '',
    });
    await flushMicrotasks();
    expect(conv.optionCards[0].status).toBe('answered');
    expect(conv.messages.filter(m => m.kind === 'system')).toHaveLength(1);
  });
});

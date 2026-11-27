// 网页聊天审计首批（2026-11-20，TODOS「网页聊天缺陷审计 27 条定案」①②③⑥）
// 行为面锁定。每条缺陷先红后绿：
//   ① ai.status 终态不收窗（后端 ctx 取消/看门狗击杀只发 ai.status 终态帧，
//      词表=agentAIRunEventTerminal；旧实现不收窗 → 气泡被写英文字面量
//      "stopped"、输入区卡「插话发送中」90s 后被看门狗误标 error）
//   ② ensureAISocket 在 close 时永不落定（断线窗口点审批卡永久 responding）
//      + CONNECTING 黑洞超时
//   ③ 审批族帧②壳路由写非响应式 raw 壳（同 handleDeviceSnapshot 先例换代理）
//   ⑥ 附件 chips uid + 同名文件上传改名（同名覆盖/重复 key 双删）
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest';
import { nextTick, watchEffect } from 'vue';

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

import { useAliangChat, AI_RUN_TERMINAL_STATUSES } from './useAliangChat';
import { useDeviceSessions } from './useDeviceSessions';
import { uploadChatAttachment } from '../services/agentApi';

// Fake WebSocket：readyState 手控、实例句柄留在类上，测试手动驱动 onopen，
// 并直接调 onclose/onmessage（与生产同一 JSON.parse → handleAIEvent 链）。
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
    // 与真实 socket 不同：不触发 onclose（测试显式驱动），方便区分
    // 「主动 close→onclose 链」与「纯超时 reject」两条路。
    this.readyState = FakeWebSocket.CLOSED;
  }
}

async function flushMicrotasks() {
  for (let i = 0; i < 20; i++) await Promise.resolve();
}

function lastSock() {
  return FakeWebSocket.instances.at(-1);
}

// 建一个 running 中的本地会话并挂进会话池；stopRun 借道 ensureAISocket 建
// socket（ai.stop 帧对测试无副作用）。顺序同 zombieFlags：先 stopRun 再
// 手动 onopen，最后 await。
async function mountRunningSession({ content = '流式中', runningBubble = true } = {}) {
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
      { role: 'assistant', content, status: runningBubble ? 'running' : 'done', ts: 2 },
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

function sentTypes(sock) {
  return sock.sent.map((raw) => {
    try {
      return JSON.parse(raw).type;
    } catch (_) {
      return 'malformed';
    }
  });
}

it('commits a journal rename only after its receipt and ignores an older title event', async () => {
  localStorage.clear();
  FakeWebSocket.instances.length = 0;
  vi.stubGlobal('WebSocket', FakeWebSocket);
  const { chat, conv, sock } = await mountRunningSession();
  conv.title = 'Old title';
  chat.startRename(conv);
  chat.renameDraft.value = 'New title';
  const saving = chat.commitRename();
  await flushMicrotasks();
  const rename = sock.sent.map(JSON.parse).find(msg => msg.type === 'ai.session.rename');
  expect(rename).toMatchObject({ title: 'New title', session_id: 'sess-z' });
  expect(conv.title).toBe('Old title');
  frame(sock, {
    ...rename, type: 'ai.session.rename.ack', accepted: true,
    title_updated_at: '2026-11-27T01:02:03.123456789Z',
  });
  await saving;
  expect(conv.title).toBe('New title');
  frame(sock, {
    type: 'ai.session.title.updated', session_id: 'sess-z', title: 'Older title',
    title_updated_at: '2026-11-27T01:02:03.123Z',
  });
  expect(conv.title).toBe('New title');
});

describe('① ai.status 终态收窗（词表=agentAIRunEventTerminal）', () => {
  beforeEach(() => {
    localStorage.clear();
    FakeWebSocket.instances.length = 0;
    globalThis.WebSocket = FakeWebSocket;
  });

  it('词表导出与后端逐字对齐（stopped/cancelled/interrupted/timeout/idle_timeout/hard_timeout/output_limited）', () => {
    expect([...AI_RUN_TERMINAL_STATUSES].sort()).toEqual(
      ['cancelled', 'hard_timeout', 'idle_timeout', 'interrupted', 'output_limited', 'stopped', 'timeout'].sort(),
    );
  });

  it('终态 ai.status(stopped) 带 error：收窗+关账，空气泡写人话不写状态词', async () => {
    const { conv, sock } = await mountRunningSession({ content: '' });
    const human = 'AI run went idle (no output for 1m0s) and was stopped';
    frame(sock, { type: 'ai.status', session_id: 'sess-z', status: 'stopped', error: human });
    await flushMicrotasks();
    expect(conv.running).toBe(false);
    expect(conv.runActive).toBe(false);
    expect(conv.messages[1].status).toBe('done');
    expect(conv.messages[1].content).toBe(human);
    // sending 复位经 finishRun 与 ai.done 共用同一函数（既有用例已覆盖），
    // 本用例钉 flags/气泡/复订阅三个本修复新增的收敛面。
    expect(conv.messages[1].status).not.toBe('error');
  });

  it('终态无 error：空气泡写本地化终止文案，绝不透传英文状态词', async () => {
    const { conv, sock } = await mountRunningSession({ content: '' });
    frame(sock, { type: 'ai.status', session_id: 'sess-z', status: 'idle_timeout' });
    await flushMicrotasks();
    expect(conv.messages[1].content).toBeTruthy();
    expect(conv.messages[1].content).not.toBe('idle_timeout');
    expect(conv.messages[1].status).toBe('done');
  });

  it('终态触发定谳复订阅（同 ai.done 的 runDoneNeedsSnapshot 链）', async () => {
    const { sock } = await mountRunningSession();
    frame(sock, { type: 'ai.status', session_id: 'sess-z', status: 'cancelled' });
    await flushMicrotasks();
    expect(sentTypes(sock)).toContain('ai.session.subscribe');
  });

  it('终态且气泡已有正文：正文原样保留（终止文案不覆盖已流出的答案）', async () => {
    const { conv, sock } = await mountRunningSession({ content: '已有正文' });
    frame(sock, { type: 'ai.status', session_id: 'sess-z', status: 'stopped', error: 'boom' });
    await flushMicrotasks();
    expect(conv.messages[1].content).toBe('已有正文');
    expect(conv.messages[1].status).toBe('done');
    expect(conv.running).toBe(false);
    expect(conv.runActive).toBe(false);
  });

  it('非终态 stopping：空气泡写本地化文案（不写 "stopping" 字面量），run 不收窗', async () => {
    const { conv, sock } = await mountRunningSession({ content: '' });
    frame(sock, { type: 'ai.status', session_id: 'sess-z', status: 'stopping' });
    await flushMicrotasks();
    expect(conv.messages[1].content).toBeTruthy();
    expect(conv.messages[1].content).not.toBe('stopping');
    expect(conv.running).toBe(true);
    expect(conv.runActive).toBe(true);
  });

  it('非终态且有内容：气泡内容原样保留', async () => {
    const { conv, sock } = await mountRunningSession({ content: '已有正文' });
    frame(sock, { type: 'ai.status', session_id: 'sess-z', status: 'stopping' });
    await flushMicrotasks();
    expect(conv.messages[1].content).toBe('已有正文');
  });

  it('未知非终态状态词不污染气泡', async () => {
    const { conv, sock } = await mountRunningSession({ content: '' });
    frame(sock, { type: 'ai.status', session_id: 'sess-z', status: 'waiting_approval' });
    await flushMicrotasks();
    expect(conv.messages[1].content).toBe('');
  });

  it('retry_active 中的终态词不算终态（重连挂起）', async () => {
    const { conv, sock } = await mountRunningSession();
    frame(sock, { type: 'ai.status', session_id: 'sess-z', status: 'interrupted', retry_active: true });
    await flushMicrotasks();
    expect(conv.running).toBe(true);
    expect(conv.messages[1].status).toBe('running');
  });

  it('teardown(cancelled) 终态同样收窗（agentAITeardownStatus="cancelled" 的实际链）', async () => {
    const { conv, sock } = await mountRunningSession({ content: '' });
    frame(sock, { type: 'ai.status', session_id: 'sess-z', status: 'cancelled', error: 'agent exited' });
    await flushMicrotasks();
    expect(conv.running).toBe(false);
    expect(conv.messages[1].content).toBe('agent exited');
  });
});

describe('② ensureAISocket close 落定 + CONNECTING 超时', () => {
  beforeEach(() => {
    localStorage.clear();
    FakeWebSocket.instances.length = 0;
    globalThis.WebSocket = FakeWebSocket;
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  // 挂一个待审批会话（socket 已开，审批卡 pending）。
  async function mountPendingApproval() {
    const chat = useAliangChat();
    const conv = {
      id: 'c1',
      title: '',
      provider: 'agentcore',
      model: 'glm-x',
      aiSessionId: 'sess-z',
      running: false,
      runActive: false,
      messages: [
        { role: 'user', content: '帮我跑个命令', status: 'done', ts: 1, messageId: 'm-u1' },
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
    frame(sock, {
      type: 'ai.approval.request',
      session_id: 'sess-z',
      approval_id: 'ap1',
      message_id: 'm-u1',
      title: '运行命令',
      command: 'ls',
    });
    await flushMicrotasks();
    return { chat, conv, sock };
  }

  it('断线窗口点审批：CONNECTING 即 close → 卡复位 pending（不再永久 responding）', async () => {
    const { chat, conv, sock } = await mountPendingApproval();
    expect(conv.approvals).toHaveLength(1);
    expect(conv.approvals[0].status).toBe('pending');

    // 第一条腿正常断开（无 running 无 sending → 无重连排期，干净现场）。
    sock.onclose({ code: 1006, reason: '' });
    await flushMicrotasks();
    expect(chat.respondApproval).toBeTypeOf('function');

    // 断线窗口点审批：新建 socket 进入 CONNECTING，随后连接失败 close。
    const responding = chat.respondApproval(conv, conv.approvals[0], 'allow');
    const sock2 = lastSock();
    expect(FakeWebSocket.instances.length).toBe(2);
    sock2.onclose({ code: 1006, reason: '' });
    await flushMicrotasks();
    await Promise.race([responding, flushMicrotasks()]);
    expect(conv.approvals[0].status).toBe('pending');
    expect(conv.approvals[0].error).toBeTruthy();
  });

  it('close 落定后可重试：再次 respondApproval 新建 socket 且成功后标记已处理', async () => {
    const { chat, conv, sock } = await mountPendingApproval();
    // 先正常断开第一腿（无 running 无 sending → 无重连排期，干净现场）。
    sock.onclose({ code: 1006, reason: '' });
    await flushMicrotasks();
    // 第一次：CONNECTING 即断（卡复位 pending）
    const first = chat.respondApproval(conv, conv.approvals[0], 'allow');
    lastSock().onclose({ code: 1006, reason: '' });
    await Promise.race([first, flushMicrotasks()]);
    expect(conv.approvals[0].status).toBe('pending');

    // 第二次：连上 → 发送成功 → markApprovalHandled
    const second = chat.respondApproval(conv, conv.approvals[0], 'allow');
    const sock3 = lastSock();
    expect(FakeWebSocket.instances.length).toBe(3);
    sock3.readyState = FakeWebSocket.OPEN;
    sock3.onopen();
    await flushMicrotasks();
    await Promise.race([second, flushMicrotasks()]);
    // 成功即收卡（markApprovalHandled = handled 去重集 + removeApproval）：
    // 卡从列表消失正是「已处理」的可观察面。
    expect(conv.approvals).toHaveLength(0);
    const types = sentTypes(sock3);
    expect(types).toContain('ai.approval.response');
  });

  it('迟到 close 不拆新连接的超时保险丝（评审 should）：空窗重建的 socket 仍有黑洞保护', async () => {
    vi.useFakeTimers();
    const { chat, conv, sock } = await mountPendingApproval();
    sock.onclose({ code: 1006, reason: '' });
    await flushMicrotasks();
    // 第一腿：CONNECTING 黑洞 → 10s 超时 reject（卡复位 pending）
    const first = chat.respondApproval(conv, conv.approvals[0], 'allow');
    const sock2 = lastSock();
    vi.advanceTimersByTime(10100);
    await flushMicrotasks();
    await Promise.race([first.catch(() => {}), flushMicrotasks()]);
    expect(conv.approvals[0].status).toBe('pending');
    // 超时空窗重建：sock3 进入 CONNECTING（sock2 的 close 事件尚在途）
    const second = chat.respondApproval(conv, conv.approvals[0], 'allow');
    const sock3 = lastSock();
    expect(sock3.readyState).toBe(FakeWebSocket.CONNECTING);
    // sock2 的迟到 close 送达：不得拆 sock3 的保险丝、不得清它的挂起 Promise
    sock2.onclose({ code: 1006, reason: '' });
    await flushMicrotasks();
    // sock3 同样黑洞：10s 后必须落定（旧实现保险丝被迟到 close 拆掉→永不落定）
    vi.advanceTimersByTime(10100);
    await flushMicrotasks();
    await Promise.race([second.catch(() => {}), flushMicrotasks()]);
    expect(conv.approvals[0].status).toBe('pending');
    expect(conv.approvals[0].error).toBeTruthy();
  });

  it('CONNECTING 黑洞：超时后 promise reject（调用方 catch 兜底，不永久挂死）', async () => {
    vi.useFakeTimers();
    const { chat, conv, sock } = await mountPendingApproval();
    // 先正常断开第一条腿（无 running 无 sending → 无重连排期，干净现场）。
    sock.onclose({ code: 1006, reason: '' });
    await flushMicrotasks();
    const responding = chat.respondApproval(conv, conv.approvals[0], 'allow');
    const sock2 = lastSock();
    expect(sock2.readyState).toBe(FakeWebSocket.CONNECTING);
    vi.advanceTimersByTime(10100);
    await flushMicrotasks();
    await Promise.race([responding.catch(() => {}), flushMicrotasks()]);
    expect(conv.approvals[0].status).toBe('pending');
    expect(conv.approvals[0].error).toBeTruthy();
  });
});

describe('③ 审批族帧②壳路由写响应式代理（已在会话池的壳）', () => {
  beforeEach(() => {
    localStorage.clear();
    FakeWebSocket.instances.length = 0;
    globalThis.WebSocket = FakeWebSocket;
  });

  it('帧 session_id=source_session_id 命中池内壳：经代理写卡，watch 触发', async () => {
    const dev = useDeviceSessions();
    dev.deviceSessions.value = [
      {
        id: 'entry-1',
        title: 'dev 会话',
        provider: 'agentcore',
        status: 'idle',
        source_session_id: 'native-xyz',
      },
    ];
    const chat = useAliangChat();
    // 池内壳：id 与 deviceSessionToConversation 的 'dev-' + entry.id 对齐。
    const pooled = {
      id: 'dev-entry-1',
      title: 'dev 会话',
      provider: 'agentcore',
      aiSessionId: 'entry-1',
      deviceSession: dev.deviceSessions.value[0],
      running: false,
      messages: [],
      approvals: [],
    };
    chat.conversations.value.push(pooled);
    chat.activeId.value = 'dev-entry-1';

    // reactivity 探针：raw 写不触发（缺陷现状），代理写必触发（修复后）。
    let seen = -1;
    const stopWatch = watchEffect(() => {
      const c = chat.conversations.value.find((x) => x.id === 'dev-entry-1');
      seen = Array.isArray(c && c.approvals) ? c.approvals.length : -1;
    });
    expect(seen).toBe(0);

    const stopping = chat.stopRun();
    const sock = lastSock();
    sock.readyState = FakeWebSocket.OPEN;
    sock.onopen();
    await flushMicrotasks();
    await stopping;
    frame(sock, {
      type: 'ai.approval.request',
      session_id: 'native-xyz',
      approval_id: 'ap-shell-1',
      message_id: 'm-1',
      title: '运行命令',
    });
    await flushMicrotasks();
    await nextTick();
    // 修复前：upsertApproval 写 raw 壳，watchEffect 不触发，seen 恒 0。
    expect(seen).toBe(1);
    const pooledNow = chat.conversations.value.find((x) => x.id === 'dev-entry-1');
    expect(pooledNow.approvals).toHaveLength(1);
    stopWatch();
  });
});

describe('⑥ 附件 uid + 同名上传改名', () => {
  beforeEach(() => {
    localStorage.clear();
    FakeWebSocket.instances.length = 0;
    globalThis.WebSocket = FakeWebSocket;
    uploadChatAttachment.mockReset();
  });

  function makeChatWithConv() {
    const chat = useAliangChat();
    const conv = {
      id: 'c1',
      title: '',
      provider: 'agentcore',
      model: 'glm-x',
      aiSessionId: 'sess-z',
      running: false,
      messages: [],
    };
    chat.conversations.value.push(conv);
    chat.activeId.value = 'c1';
    return chat;
  }

  it('每条 pending 附件带本地 uid，移除按 uid 精确命中', async () => {
    const chat = makeChatWithConv();
    uploadChatAttachment
      .mockResolvedValueOnce({ ref: 'sess-z/shot.png', name: 'shot.png', mime: 'image/png', bytes: 1 })
      .mockResolvedValueOnce({ ref: 'sess-z/cat.png', name: 'cat.png', mime: 'image/png', bytes: 1 });
    const mkFile = (name) => new File(['x'], name, { type: 'image/png' });
    chat.addPendingAttachments([mkFile('shot.png'), mkFile('cat.png')]);
    await flushMicrotasks();
    expect(chat.pendingAttachments.value).toHaveLength(2);
    const uid0 = chat.pendingAttachments.value[0].uid;
    const uid1 = chat.pendingAttachments.value[1].uid;
    expect(uid0).toBeTruthy();
    expect(uid1).toBeTruthy();
    expect(uid0).not.toBe(uid1);
    chat.removePendingAttachment(uid0);
    expect(chat.pendingAttachments.value).toHaveLength(1);
    expect(chat.pendingAttachments.value[0].name).toBe('cat.png');
  });

  it('同名文件同批上传：第二个改名（不同文件名 → 服务端不覆盖），chips uid 互异', async () => {
    const chat = makeChatWithConv();
    const uploadedNames = [];
    uploadChatAttachment.mockImplementation(async (_sid, file) => {
      uploadedNames.push(file.name);
      return { ref: 'sess-z/' + file.name, name: file.name, mime: 'image/png', bytes: 1 };
    });
    const mkFile = (name) => new File(['x'], name, { type: 'image/png' });
    chat.addPendingAttachments([mkFile('shot.png'), mkFile('shot.png')]);
    await flushMicrotasks();
    expect(uploadedNames).toHaveLength(2);
    expect(uploadedNames[0]).not.toBe(uploadedNames[1]);
    expect(uploadedNames[0]).toBe('shot.png');
    // 改名保持扩展名（服务端白名单按 ext 校验）。
    expect(uploadedNames[1]).toMatch(/\.png$/);
    expect(chat.pendingAttachments.value).toHaveLength(2);
    expect(chat.pendingAttachments.value[0].uid).not.toBe(chat.pendingAttachments.value[1].uid);
  });

  it('与既有 pending 同名也改名（跨批次同名不覆盖）', async () => {
    const chat = makeChatWithConv();
    const uploadedNames = [];
    uploadChatAttachment.mockImplementation(async (_sid, file) => {
      uploadedNames.push(file.name);
      return { ref: 'sess-z/' + file.name, name: file.name, mime: 'image/png', bytes: 1 };
    });
    const mkFile = (name) => new File(['x'], name, { type: 'image/png' });
    chat.addPendingAttachments([mkFile('shot.png')]);
    await flushMicrotasks();
    chat.addPendingAttachments([mkFile('shot.png')]);
    await flushMicrotasks();
    expect(uploadedNames[0]).toBe('shot.png');
    expect(uploadedNames[1]).not.toBe('shot.png');
    expect(chat.pendingAttachments.value).toHaveLength(2);
  });

  it('净化域撞名也改名：原始名互异但服务端净化后同 base（"a b.png"/"ab.png"）', async () => {
    const chat = makeChatWithConv();
    const uploadedNames = [];
    uploadChatAttachment.mockImplementation(async (_sid, file) => {
      uploadedNames.push(file.name);
      return { ref: 'sess-z/' + file.name, name: file.name, mime: 'image/png', bytes: 1 };
    });
    const mkFile = (name) => new File(['x'], name, { type: 'image/png' });
    chat.addPendingAttachments([mkFile('a b.png'), mkFile('ab.png')]);
    await flushMicrotasks();
    // "a b" 净化成 "ab" → 两张同落 ab.png；第二个必须在净化域出号（ab-2.png）。
    expect(uploadedNames[0]).toBe('a b.png');
    expect(uploadedNames[1]).toBe('ab-2.png');
    expect(chat.pendingAttachments.value).toHaveLength(2);
  });

  it('dot-at-0 文件名（".png"）不参与撞名域（服务端时间戳兜底），不改名不越白名单', async () => {
    const chat = makeChatWithConv();
    const uploadedNames = [];
    uploadChatAttachment.mockImplementation(async (_sid, file) => {
      uploadedNames.push(file.name);
      return { ref: 'sess-z/' + file.name, name: file.name, mime: 'image/png', bytes: 1 };
    });
    const mkFile = (name) => new File(['x'], name, { type: 'image/png' });
    chat.addPendingAttachments([mkFile('.png'), mkFile('.png')]);
    await flushMicrotasks();
    // base 净化后为空 → 服务端 up-<nano> 兜底永不撞；改名产出 ".png-2" 会越
    // 白名单（filepath.Ext(".png-2") 非法），必须原样放行。
    expect(uploadedNames).toEqual(['.png', '.png']);
    expect(chat.pendingAttachments.value).toHaveLength(2);
  });

  it('大小写扩展名同身份（"shot.PNG"/"shot.png"）：第二个改名', async () => {
    const chat = makeChatWithConv();
    const uploadedNames = [];
    uploadChatAttachment.mockImplementation(async (_sid, file) => {
      uploadedNames.push(file.name);
      return { ref: 'sess-z/' + file.name, name: file.name, mime: 'image/png', bytes: 1 };
    });
    chat.addPendingAttachments([
      new File(['x'], 'shot.PNG', { type: 'image/png' }),
      new File(['x'], 'shot.png', { type: 'image/png' }),
    ]);
    await flushMicrotasks();
    // 服务端 ext 转小写落盘 → 同一文件；净化域键必须判撞。候选名取撞名
    // 文件自己的扩展名（shot.png → shot-2.png）。
    expect(uploadedNames[0]).toBe('shot.PNG');
    expect(uploadedNames[1]).toBe('shot-2.png');
  });
});

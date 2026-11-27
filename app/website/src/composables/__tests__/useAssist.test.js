// 远程协助 P1 Task 9:前端入口申请弹窗的 composable 状态机。
// 覆盖:快照映射(idle/requested/active/ended)、四类信封错误码 → 友好 i18n
// KEY、refresh 轮询、cancel/end 动作、观看 WS(重连 800ms×2 退避 + assist.end
// 终态 + close1000 不再重连)、polling 2s 周期与请求态退出自动停。
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  getAssistGates: vi.fn(),
  getAssistSession: vi.fn(),
  requestAssist: vi.fn(),
  cancelAssist: vi.fn(),
  endAssist: vi.fn(),
}));

vi.mock('../../services/agentApi', () => api);

import { useAssist, resetForTests, assistEndReasonKey } from '../useAssist';

// Fake WebSocket:readyState 手控、实例句柄留在类上;测试手动驱动
// onopen/onclose/onmessage(与生产同一 JSON.parse → handleFrame 链)。
class FakeWebSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;
  static instances = [];
  constructor(url) {
    this.url = url;
    this.readyState = FakeWebSocket.CONNECTING;
    FakeWebSocket.instances.push(this);
  }
  send() {}
  close() {
    this.readyState = FakeWebSocket.CLOSED;
  }
}

function snap(overrides = {}) {
  return { state: '', session_id: '', message: '', ...overrides };
}

function enrichedError(message, { code, status } = {}) {
  const err = new Error(message);
  if (typeof code === 'number') err.code = code;
  if (typeof status === 'number') err.status = status;
  return err;
}

beforeEach(() => {
  for (const fn of Object.values(api)) fn.mockReset();
  resetForTests();
  FakeWebSocket.instances = [];
  globalThis.WebSocket = FakeWebSocket;
});

afterEach(() => {
  vi.useRealTimers();
  resetForTests();
});

describe('useAssist 快照映射与动作', () => {
  it('初始为 idle;checkGates 反映 enabled', async () => {
    api.getAssistGates.mockResolvedValue({ enabled: true });
    const a = useAssist();
    expect(a.state.value).toBe('idle');
    expect(a.gatesEnabled.value).toBe(false);
    await a.checkGates();
    expect(api.getAssistGates).toHaveBeenCalledTimes(1);
    expect(a.gatesEnabled.value).toBe(true);
    expect(a.gatesLoaded.value).toBe(true);
  });

  it('gates 拉取失败时保守隐藏(disabled)', async () => {
    api.getAssistGates.mockRejectedValue(new Error('boom'));
    const a = useAssist();
    await a.checkGates();
    expect(a.gatesEnabled.value).toBe(false);
    expect(a.gatesLoaded.value).toBe(true);
  });

  it('request 成功 → requested/id/message', async () => {
    api.requestAssist.mockResolvedValue(snap({ state: 'requested', session_id: 's1', message: '帮我看下' }));
    const a = useAssist();
    const ok = await a.request('帮我看下');
    expect(ok).toBe(true);
    expect(api.requestAssist).toHaveBeenCalledWith('帮我看下');
    expect(a.state.value).toBe('requested');
    expect(a.sessionId.value).toBe('s1');
    expect(a.message.value).toBe('帮我看下');
    expect(a.lastError.value).toBe('');
  });

  it('refresh:state 空串 → idle(零值快照)', async () => {
    api.getAssistSession.mockResolvedValue(snap());
    const a = useAssist();
    await a.refresh();
    expect(a.state.value).toBe('idle');
    expect(a.sessionId.value).toBe('');
  });

  it('refresh:active 快照映射', async () => {
    api.getAssistSession.mockResolvedValue(snap({ state: 'active', session_id: 's2', message: 'hi' }));
    const a = useAssist();
    await a.refresh();
    expect(a.state.value).toBe('active');
    expect(a.sessionId.value).toBe('s2');
  });

  it('refresh 检测到 session 变化时清空残留帧(防串会话)', async () => {
    const a = useAssist();
    a.sessionId.value = 'session-A';
    a.frames.value = [{ type: 'terminal.output', data: 'from A' }];
    api.getAssistSession.mockResolvedValue(snap({ state: 'active', session_id: 'session-B' }));
    await a.refresh();
    expect(a.sessionId.value).toBe('session-B');
    expect(a.frames.value).toEqual([]);
  });

  it('resetTransient 清空瞬态但保留 gates', async () => {
    api.getAssistGates.mockResolvedValue({ enabled: true });
    const a = useAssist();
    await a.checkGates();
    a.state.value = 'ended';
    a.endReason.value = 'admin_end';
    a.sessionId.value = 's1';
    a.frames.value = [{ type: 'terminal.output' }];
    a.resetTransient();
    expect(a.state.value).toBe('idle');
    expect(a.endReason.value).toBe('');
    expect(a.sessionId.value).toBe('');
    expect(a.frames.value).toEqual([]);
    expect(a.gatesEnabled.value).toBe(true);
    expect(a.gatesLoaded.value).toBe(true);
  });

  it('cancel 成功 → ended(client_end)', async () => {
    api.cancelAssist.mockResolvedValue(snap({ state: 'ended', end_reason: 'client_end', session_id: 's1' }));
    const a = useAssist();
    const ok = await a.cancel();
    expect(ok).toBe(true);
    expect(a.state.value).toBe('ended');
    expect(a.endReason.value).toBe('client_end');
  });

  it('end 成功 → ended', async () => {
    api.endAssist.mockResolvedValue(snap({ state: 'ended', end_reason: 'client_end', session_id: 's1' }));
    const a = useAssist();
    await a.end();
    expect(a.state.value).toBe('ended');
  });
});

describe('useAssist 错误码 → 文案 KEY', () => {
  it('105/429 冷却 → assist_error_cooldown', async () => {
    api.requestAssist.mockRejectedValue(enrichedError('cooldown', { code: 105, status: 429 }));
    const a = useAssist();
    const ok = await a.request('x');
    expect(ok).toBe(false);
    expect(a.lastError.value).toBe('assist_error_cooldown');
    expect(a.state.value).toBe('idle');
  });

  it('104/409 request → assist_error_busy', async () => {
    api.requestAssist.mockRejectedValue(enrichedError('busy', { code: 104, status: 409 }));
    const a = useAssist();
    await a.request('x');
    expect(a.lastError.value).toBe('assist_error_busy');
  });

  it('104/409 cancel → assist_error_not_pending', async () => {
    api.cancelAssist.mockRejectedValue(enrichedError('no pending', { code: 104, status: 409 }));
    const a = useAssist();
    await a.cancel();
    expect(a.lastError.value).toBe('assist_error_not_pending');
  });

  it('104/409 end → assist_error_nothing_to_end', async () => {
    api.endAssist.mockRejectedValue(enrichedError('nothing', { code: 104, status: 409 }));
    const a = useAssist();
    await a.end();
    expect(a.lastError.value).toBe('assist_error_nothing_to_end');
  });

  it('201/500 → assist_error_offline', async () => {
    api.requestAssist.mockRejectedValue(enrichedError('signaling unavailable', { code: 201, status: 500 }));
    const a = useAssist();
    await a.request('x');
    expect(a.lastError.value).toBe('assist_error_offline');
  });

  it('403 → assist_error_disabled', async () => {
    api.requestAssist.mockRejectedValue(enrichedError('Remote assist is disabled', { status: 403 }));
    const a = useAssist();
    await a.request('x');
    expect(a.lastError.value).toBe('assist_error_disabled');
  });

  it('end_reason 文案 KEY 映射未知值回落 unknown', () => {
    expect(assistEndReasonKey('admin_end')).toBe('assist_end_admin');
    expect(assistEndReasonKey('client_end')).toBe('assist_end_client');
    expect(assistEndReasonKey('device_offline')).toBe('assist_end_offline');
    expect(assistEndReasonKey('duration_cap')).toBe('assist_end_duration_cap');
    expect(assistEndReasonKey('whatever')).toBe('assist_end_unknown');
    expect(assistEndReasonKey('')).toBe('assist_end_unknown');
  });
});

describe('useAssist 观看 WS', () => {
  it('connect 建 /api/agent/assist/stream?s 连接,onopen → open', () => {
    const a = useAssist();
    a.sessionId.value = 's1';
    a.watchWs.connect();
    const sock = FakeWebSocket.instances.at(-1);
    expect(sock.url).toContain('/api/agent/assist/stream');
    expect(sock.url).toContain('session_id=s1');
    expect(a.watchStatus.value).toBe('connecting');
    sock.readyState = FakeWebSocket.OPEN;
    sock.onopen();
    expect(a.watchStatus.value).toBe('open');
  });

  it('assist.end 帧 → ended + endReason,并记录帧', () => {
    const a = useAssist();
    a.sessionId.value = 's1';
    a.watchWs.connect();
    const sock = FakeWebSocket.instances.at(-1);
    sock.readyState = FakeWebSocket.OPEN;
    sock.onopen();
    sock.onmessage({ data: JSON.stringify({ type: 'assist.end', session_id: 's1', reason: 'admin_end' }) });
    expect(a.state.value).toBe('ended');
    expect(a.endReason.value).toBe('admin_end');
    expect(a.frames.value.some((f) => f.type === 'assist.end')).toBe(true);
  });

  it('意外断开 → 800ms 后重连,最多 2 次', () => {
    vi.useFakeTimers();
    const a = useAssist();
    a.watchWs.connect();
    expect(FakeWebSocket.instances.length).toBe(1);

    FakeWebSocket.instances[0].onclose({ code: 1011, reason: 'idle' });
    vi.advanceTimersByTime(799);
    expect(FakeWebSocket.instances.length).toBe(1);
    vi.advanceTimersByTime(1);
    expect(FakeWebSocket.instances.length).toBe(2);

    FakeWebSocket.instances[1].onclose({ code: 1011 });
    vi.advanceTimersByTime(800);
    expect(FakeWebSocket.instances.length).toBe(3);

    // 第三次断开超出重连预算:不再新建
    FakeWebSocket.instances[2].onclose({ code: 1011 });
    vi.advanceTimersByTime(2000);
    expect(FakeWebSocket.instances.length).toBe(3);
    expect(a.watchStatus.value).toBe('closed');
  });

  it('assist.end + close1000 是唯一终态:不再重连', () => {
    vi.useFakeTimers();
    const a = useAssist();
    a.sessionId.value = 's1';
    a.watchWs.connect();
    const sock = FakeWebSocket.instances.at(-1);
    sock.readyState = FakeWebSocket.OPEN;
    sock.onopen();
    sock.onmessage({ data: JSON.stringify({ type: 'assist.end', reason: 'client_end' }) });
    sock.onclose({ code: 1000 });
    vi.advanceTimersByTime(3000);
    expect(FakeWebSocket.instances.length).toBe(1);
    expect(a.watchStatus.value).toBe('closed');
  });

  it('Upgrade 后立刻断开的风暴被预算扼杀(onopen 不重置重连预算)', () => {
    vi.useFakeTimers();
    const a = useAssist();
    a.watchWs.connect();
    const flap = (sock) => {
      sock.readyState = FakeWebSocket.OPEN;
      sock.onopen();
      sock.onclose({ code: 1011, reason: 'auth flap' });
    };
    // 第 1 次:open→close 立即
    flap(FakeWebSocket.instances[0]);
    vi.advanceTimersByTime(800);
    expect(FakeWebSocket.instances.length).toBe(2);
    // 第 2 次同样 open→close 立即
    flap(FakeWebSocket.instances[1]);
    vi.advanceTimersByTime(800);
    expect(FakeWebSocket.instances.length).toBe(3);
    // 第 3 次:预算已耗尽 → 不再新建(旧实现 open 归零会无限重连)
    flap(FakeWebSocket.instances[2]);
    vi.advanceTimersByTime(50000);
    expect(FakeWebSocket.instances.length).toBe(3);
    expect(a.watchStatus.value).toBe('closed');
  });

  it('connect 清空上一会话残留帧', () => {
    const a = useAssist();
    a.frames.value = [{ type: 'terminal.output', data: 'old-session' }];
    a.watchWs.connect();
    expect(a.frames.value).toEqual([]);
  });

  it('生产性帧到达后重置重连预算', () => {
    vi.useFakeTimers();
    const a = useAssist();
    a.watchWs.connect();
    // 第 1 轮断开 → 触发第 1 次重连
    FakeWebSocket.instances[0].onclose({ code: 1011 });
    vi.advanceTimersByTime(800);
    expect(FakeWebSocket.instances.length).toBe(2);
    const sock2 = FakeWebSocket.instances[1];
    sock2.readyState = FakeWebSocket.OPEN;
    sock2.onopen();
    // 生产性帧:预算归零
    sock2.onmessage({ data: JSON.stringify({ type: 'terminal.output', data: 'ok' }) });
    // 再断 → 仍可重连(预算已被合法帧补充)
    sock2.onclose({ code: 1011 });
    vi.advanceTimersByTime(800);
    expect(FakeWebSocket.instances.length).toBe(3);
  });

  it('disconnect 主动关闭后不再重连', () => {
    vi.useFakeTimers();
    const a = useAssist();
    a.watchWs.connect();
    const sock = FakeWebSocket.instances.at(-1);
    a.watchWs.disconnect();
    sock.onclose({ code: 1011 });
    vi.advanceTimersByTime(3000);
    expect(FakeWebSocket.instances.length).toBe(1);
  });
});

describe('useAssist 申请态轮询', () => {
  it('startPolling 每 2s refresh;转 active 后自动停', async () => {
    vi.useFakeTimers();
    api.getAssistSession.mockResolvedValueOnce(snap({ state: 'requested', session_id: 's1' }));
    const a = useAssist();
    a.startPolling();
    expect(api.getAssistSession).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(2000);
    expect(api.getAssistSession).toHaveBeenCalledTimes(1);
    expect(a.state.value).toBe('requested');

    api.getAssistSession.mockResolvedValueOnce(snap({ state: 'active', session_id: 's1' }));
    await vi.advanceTimersByTimeAsync(2000);
    expect(a.state.value).toBe('active');

    // 已离开 requested → 轮询停止
    await vi.advanceTimersByTimeAsync(6000);
    expect(api.getAssistSession).toHaveBeenCalledTimes(2);
  });

  it('stopPolling 立即停', async () => {
    vi.useFakeTimers();
    api.getAssistSession.mockResolvedValue(snap({ state: 'requested' }));
    const a = useAssist();
    a.startPolling();
    a.stopPolling();
    await vi.advanceTimersByTimeAsync(6000);
    expect(api.getAssistSession).not.toHaveBeenCalled();
  });
});

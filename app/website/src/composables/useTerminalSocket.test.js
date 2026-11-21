import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

class MockWebSocket {
  static instances = [];
  static CONNECTING = 0; static OPEN = 1; static CLOSING = 2; static CLOSED = 3;
  constructor(url) {
    this.url = url;
    this.readyState = 0;
    this.sent = [];
    MockWebSocket.instances.push(this);
  }
  send(data) { this.sent.push(data); }
  close() { this.readyState = 3; this.onclose && this.onclose({ code: 1000, reason: '' }); }
  open() { this.readyState = 1; this.onopen && this.onopen(); }
  emit(data) { this.onmessage && this.onmessage({ data: JSON.stringify(data) }); }
}
vi.stubGlobal('WebSocket', MockWebSocket);

import { createTerminalSocket } from './useTerminalSocket';

describe('createTerminalSocket', () => {
  beforeEach(() => { MockWebSocket.instances = []; vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it('open 后先发 auth 再发 create(attach:true)', () => {
    const frames = [];
    const sock = createTerminalSocket({ token: 'tk', sessionId: 's1', rows: 24, cols: 80, onFrame: (f) => frames.push(f) });
    const ws = MockWebSocket.instances.at(-1);
    expect(ws.url).toContain('/api/agent/web-terminal/stream');
    ws.open();
    expect(JSON.parse(ws.sent[0])).toEqual({ type: 'terminal.auth', token: 'tk' });
    const create = JSON.parse(ws.sent[1]);
    expect(create.type).toBe('terminal.create');
    expect(create.session_id).toBe('s1');
    expect(create.attach).toBe(true);
    sock.dispose();
  });

  it('output/replay 帧路由到 onFrame;exit 帧回调 onExit', () => {
    const frames = [];
    let exited = false;
    const sock = createTerminalSocket({ token: 'tk', sessionId: 's1', rows: 24, cols: 80, onFrame: (f) => frames.push(f), onExit: () => { exited = true; } });
    const ws = MockWebSocket.instances.at(-1);
    ws.open();
    ws.emit({ type: 'terminal.output', session_id: 's1', data: 'hello' });
    ws.emit({ type: 'terminal.replay', session_id: 's1', data: 'old', seq: 0, final: true });
    ws.emit({ type: 'terminal.exit', session_id: 's1' });
    expect(frames.map((f) => f.data)).toEqual(['hello', 'old']);
    expect(exited).toBe(true);
    sock.dispose();
  });

  it('意外断开重连(≤2 次,800ms 退避)且重连带 attach', () => {
    const sock = createTerminalSocket({ token: 'tk', sessionId: 's1', rows: 24, cols: 80, onFrame: () => {}, onStatus: () => {} });
    const first = MockWebSocket.instances[0];
    first.open();
    first.readyState = 3;
    first.onclose({ code: 1006, reason: '' });
    vi.advanceTimersByTime(900);
    const second = MockWebSocket.instances[1];
    expect(second).toBeTruthy();
    second.open();
    expect(JSON.parse(second.sent[1]).attach).toBe(true);
    sock.dispose();
  });

  it('resize/close 走 send;dispose 后不再重连', () => {
    const sock = createTerminalSocket({ token: 'tk', sessionId: 's1', rows: 24, cols: 80, onFrame: () => {} });
    const ws = MockWebSocket.instances.at(-1);
    ws.open();
    sock.resize(30, 120);
    sock.close();
    const types = ws.sent.map((d) => JSON.parse(d).type);
    expect(types).toContain('terminal.resize');
    expect(types).toContain('terminal.close');
    sock.dispose();
    vi.advanceTimersByTime(5000);
    expect(MockWebSocket.instances.length).toBe(1); // dispose 后零重连
  });

  it('重连预算耗尽 → dead 状态', () => {
    const statuses = [];
    const sock = createTerminalSocket({ token: 'tk', sessionId: 's1', rows: 24, cols: 80, onFrame: () => {}, onStatus: (s) => statuses.push(s) });
    for (let round = 0; round < 3; round++) {
      const ws = MockWebSocket.instances.at(-1);
      ws.open();
      ws.readyState = 3;
      ws.onclose({ code: 1006, reason: '' });
      vi.advanceTimersByTime(900);
    }
    expect(statuses.some((s) => s.kind === 'dead')).toBe(true);
    sock.dispose();
  });

  it('生产性入站帧归零重连预算', () => {
    const sock = createTerminalSocket({ token: 'tk', sessionId: 's1', rows: 24, cols: 80, onFrame: () => {} });
    const ws = MockWebSocket.instances[0];
    ws.open();
    ws.readyState = 3;
    ws.onclose({ code: 1006, reason: '' }); // 烧掉第 1 次预算
    vi.advanceTimersByTime(900);
    const second = MockWebSocket.instances[1];
    expect(second).toBeTruthy();
    second.open();
    second.emit({ type: 'terminal.output', session_id: 's1', data: 'x' }); // 归零(仅入站帧归零,open 不归零)
    second.readyState = 3;
    second.onclose({ code: 1006, reason: '' }); // 若未归零此处 attempts=2 → 下一轮 dead
    vi.advanceTimersByTime(900);
    const third = MockWebSocket.instances[2];
    expect(third).toBeTruthy();
    third.open();
    third.emit({ type: 'terminal.output', session_id: 's1', data: 'y' }); // 再归零
    third.readyState = 3;
    third.onclose({ code: 1006, reason: '' });
    vi.advanceTimersByTime(900);
    // 有归零 → 第 4 条连接;无归零 → 早在上一轮 dead,这里只有 3 条
    expect(MockWebSocket.instances[3]).toBeTruthy();
    sock.dispose();
  });
});

// 远程协助 P1 Task 10:客户端观看页。轻组件测试,仓内手法 = createApp + h
// 挂载 + vi.mock 提升(TerminalPane.test.js / useAssist.test.js 同源):
//   - @xterm/xterm / @xterm/addon-fit 用记录型假体(jsdom 无 canvas,真实
//     xterm 不可用);
//   - services/agentApi 打桩,useAssist 单例用真的——观看页与 composable
//     的接线(connect / frames / assist.end 终态)就是被测面;
//   - WebSocket 用 FakeWebSocket 手动驱动 onopen/onmessage/onclose
//     (useAssist.test.js 同款)。
// 覆盖:waiting / active 渲染、只读 disableStdin、output/replay 逐帧 write、
// 重连清屏续屏(term.clear + 只写新回放)、assist.end 停 WS + 终态横幅 + 原因
// 文案 + 末屏保留、Esc 返回、结束协助双段确认。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { createApp, h, nextTick } from 'vue';

const mocks = vi.hoisted(() => ({
  termInstances: [],
  ro: { instances: [] },
}));

// xterm 假体:记录 opts/open/write/clear/dispose;read-only 断言取 opts.disableStdin。
vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    constructor(opts) {
      this.opts = opts;
      this.rows = 24;
      this.cols = 80;
      this.written = '';
      this.clearCalls = 0;
      this.disposed = false;
      mocks.termInstances.push(this);
    }
    loadAddon(addon) { this.addon = addon; }
    open(el) { this.openedAt = el; }
    write(data) { this.written += data; }
    clear() { this.clearCalls += 1; this.written = ''; }
    onData(cb) { this.dataCb = cb; return { dispose: () => { this.dataCb = null; } }; }
    dispose() { this.disposed = true; }
  },
}));

vi.mock('@xterm/addon-fit', () => ({
  FitAddon: class {
    constructor() { this.fitCalls = 0; }
    fit() { this.fitCalls += 1; }
  },
}));

const api = vi.hoisted(() => ({
  getAssistGates: vi.fn(),
  getAssistSession: vi.fn(),
  requestAssist: vi.fn(),
  cancelAssist: vi.fn(),
  endAssist: vi.fn(),
}));
vi.mock('../../../services/agentApi', () => api);

import AssistWatchPage from '../AssistWatchPage.vue';
import { useAssist, resetForTests } from '../../../composables/useAssist';
import { useNavigation } from '../../../composables/useNavigation';
import { useI18n } from '../../../i18n';

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
  close() { this.readyState = FakeWebSocket.CLOSED; }
}

const { t } = useI18n();
const { currentPage } = useNavigation();

function snap(overrides = {}) {
  return { state: '', session_id: '', message: '', ...overrides };
}

let apps = [];

async function mountPage() {
  const el = document.createElement('div');
  document.body.appendChild(el);
  const app = createApp({ render: () => h(AssistWatchPage) });
  app.mount(el);
  await nextTick();
  await nextTick();
  apps.push(app);
  return { el, app, term: mocks.termInstances[mocks.termInstances.length - 1] };
}

function openSock(sock) {
  sock.readyState = FakeWebSocket.OPEN;
  sock.onopen();
}

function send(sock, frame) {
  sock.onmessage({ data: JSON.stringify(frame) });
}

function flushMicro() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

beforeEach(() => {
  for (const fn of Object.values(api)) fn.mockReset();
  api.getAssistSession.mockResolvedValue(snap());
  resetForTests();
  FakeWebSocket.instances = [];
  globalThis.WebSocket = FakeWebSocket;
  mocks.termInstances.length = 0;
  mocks.ro.instances.length = 0;
  currentPage.value = 'dashboard';
  global.ResizeObserver = class {
    constructor(cb) { this.cb = cb; mocks.ro.instances.push(this); }
    observe(el) { this.el = el; }
    disconnect() { this.disconnected = true; }
  };
});

afterEach(() => {
  vi.useRealTimers();
  for (const app of apps) { try { app.unmount(); } catch (_) { /* noop */ } }
  apps = [];
  resetForTests();
  document.body.innerHTML = '';
  delete global.ResizeObserver;
});

describe('AssistWatchPage 状态渲染', () => {
  it('requested → 等待覆盖层 + 顶栏短标签「等待中」', async () => {
    const a = useAssist();
    a.state.value = 'requested';
    a.sessionId.value = 's1';
    const m = await mountPage();
    const waiting = m.el.querySelector('[data-testid="assist-watch-waiting"]');
    expect(waiting).toBeTruthy();
    // 详细文案在覆盖层(不丢),顶栏只放短标签避免重复
    expect(waiting.textContent).toContain(t('assist_watch_waiting'));
    expect(m.el.querySelector('[data-testid="assist-watch-status"]').textContent).toContain(t('assist_watch_status_waiting'));
  });

  it('active → 「管理员正在操作」指示 + xterm 只读(disableStdin)', async () => {
    const a = useAssist();
    a.state.value = 'active';
    a.sessionId.value = 's1';
    const m = await mountPage();
    expect(m.el.querySelector('[data-testid="assist-watch-status"]').textContent).toContain(t('assist_watch_manager_acting'));
    expect(m.term.opts.disableStdin).toBe(true);
    // 建连 URL 带 session_id
    const sock = FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
    expect(sock.url).toContain('/api/agent/assist/stream');
    expect(sock.url).toContain('session_id=s1');
  });

  it('idle → 「无进行中的协助」+ 不建 WS', async () => {
    const m = await mountPage();
    await flushMicro();
    const none = m.el.querySelector('[data-testid="assist-watch-none"]');
    expect(none).toBeTruthy();
    expect(none.textContent).toContain(t('assist_watch_none'));
    // idle 顶栏不挂状态标签(主体已说明)
    expect(m.el.querySelector('[data-testid="assist-watch-status"]').textContent.trim()).toBe('');
    expect(FakeWebSocket.instances.length).toBe(0);
  });
});

describe('AssistWatchPage 只读 xterm 接帧', () => {
  it('terminal.output / terminal.replay 逐帧按序 write,保留 \\r\\n;非字符串跳过', async () => {
    const a = useAssist();
    a.state.value = 'active';
    a.sessionId.value = 's1';
    const m = await mountPage();
    const sock = FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
    openSock(sock);
    send(sock, { type: 'terminal.replay', data: 'line1\r\n' });
    send(sock, { type: 'terminal.output', data: 'line2\r\n' });
    send(sock, { type: 'terminal.output', data: '' });
    send(sock, { type: 'terminal.output', data: null });
    send(sock, { type: 'terminal.exit' });
    await nextTick();
    expect(m.term.written).toBe('line1\r\nline2\r\n');
  });
});

describe('AssistWatchPage 断线重连续屏', () => {
  it('重开 → term.clear 后只写新回放批(不重放旧帧)', async () => {
    vi.useFakeTimers();
    const a = useAssist();
    a.state.value = 'active';
    a.sessionId.value = 's1';
    const m = await mountPage();
    const sock1 = FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
    openSock(sock1);
    send(sock1, { type: 'terminal.replay', data: 'OLD\r\n' });
    await nextTick();
    expect(m.term.written).toBe('OLD\r\n');

    // 意外断开 → 800ms 后重建(useAssist 重连退避)
    sock1.onclose({ code: 1011 });
    await vi.advanceTimersByTimeAsync(800);
    const sock2 = FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
    expect(sock2).not.toBe(sock1);
    openSock(sock2);
    send(sock2, { type: 'terminal.replay', data: 'NEW\r\n' });
    await nextTick();
    // 清屏一次,且屏幕只剩新回放(旧 OLD 不重放)
    expect(m.term.clearCalls).toBe(1);
    expect(m.term.written).toBe('NEW\r\n');
  });

  it('长等待耗尽重连预算后获批 → state→active 重建连接并接帧(防永久空白)', async () => {
    vi.useFakeTimers();
    // wait 模式轮询保持 requested(避免 2s poll 把状态推走干扰时序)
    api.getAssistSession.mockResolvedValue(snap({ state: 'requested', session_id: 's1' }));
    const a = useAssist();
    a.state.value = 'requested';
    a.sessionId.value = 's1';
    const m = await mountPage();
    // 零流量 → 服务端 90s 读窗关闭等待 WS;预算只在生产性帧归零,三次关闭耗尽
    FakeWebSocket.instances[0].onclose({ code: 1011 });
    await vi.advanceTimersByTimeAsync(800);
    FakeWebSocket.instances[1].onclose({ code: 1011 });
    await vi.advanceTimersByTimeAsync(800);
    FakeWebSocket.instances[2].onclose({ code: 1011 });
    await vi.advanceTimersByTimeAsync(800);
    expect(a.watchStatus.value).toBe('closed');
    expect(FakeWebSocket.instances.length).toBe(3); // 预算耗尽,不再新建

    // 获批 → 必须新建一条活连接,否则空白终端不可恢复
    a.state.value = 'active';
    await nextTick();
    expect(FakeWebSocket.instances.length).toBe(4);
    const sock = FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
    openSock(sock);
    send(sock, { type: 'terminal.output', data: 'post-approval\r\n' });
    await nextTick();
    expect(m.term.written).toBe('post-approval\r\n');
  });
});

describe('AssistWatchPage assist.end 终态', () => {
  it('assist.end → 停 WS(不复连)+ 终态横幅原因文案 + 末屏内容保留', async () => {
    vi.useFakeTimers();
    const a = useAssist();
    a.state.value = 'active';
    a.sessionId.value = 's1';
    const m = await mountPage();
    const sock = FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
    openSock(sock);
    send(sock, { type: 'terminal.output', data: 'final-output\r\n' });
    await nextTick();
    expect(m.term.written).toBe('final-output\r\n');

    send(sock, { type: 'assist.end', session_id: 's1', reason: 'admin_end' });
    sock.onclose({ code: 1000 });
    await nextTick();
    await vi.advanceTimersByTimeAsync(5000);

    expect(a.state.value).toBe('ended');
    const banner = m.el.querySelector('[data-testid="assist-watch-ended"]');
    expect(banner).toBeTruthy();
    expect(banner.textContent).toContain(t('assist_end_admin'));
    // 不再重连
    expect(FakeWebSocket.instances.length).toBe(1);
    // 终态不清屏:末屏内容仍可见
    expect(m.term.written).toBe('final-output\r\n');
    expect(m.term.clearCalls).toBe(0);
  });

  it('结束协助双段确认:首击只进入确认态,确认后才 POST end', async () => {
    api.endAssist.mockResolvedValue(snap({ state: 'ended', end_reason: 'client_end', session_id: 's1' }));
    const a = useAssist();
    a.state.value = 'active';
    a.sessionId.value = 's1';
    const m = await mountPage();
    // 首击:进入确认态,未发请求
    m.el.querySelector('[data-testid="assist-watch-end"]').click();
    await nextTick();
    expect(api.endAssist).not.toHaveBeenCalled();
    expect(m.el.querySelector('[data-testid="assist-watch-end-confirm"]')).toBeTruthy();
    // 确认:发请求并落终态
    m.el.querySelector('[data-testid="assist-watch-end-confirm"]').click();
    await flushMicro();
    await nextTick();
    expect(api.endAssist).toHaveBeenCalledTimes(1);
    expect(a.state.value).toBe('ended');
    expect(m.el.querySelector('[data-testid="assist-watch-ended"]')).toBeTruthy();
  });

  it('结束失败 → 显示映射原因 + 一次 refresh 从服务端收敛(不再静默)', async () => {
    const err = new Error('nothing to end');
    err.code = 104;
    err.status = 409;
    api.endAssist.mockRejectedValue(err);
    api.getAssistSession.mockResolvedValue(snap({ state: 'ended', end_reason: 'admin_end', session_id: 's1' }));
    const a = useAssist();
    a.state.value = 'active';
    a.sessionId.value = 's1';
    const m = await mountPage();
    m.el.querySelector('[data-testid="assist-watch-end"]').click();
    await nextTick();
    m.el.querySelector('[data-testid="assist-watch-end-confirm"]').click();
    await flushMicro();
    await nextTick();
    await flushMicro();
    const hint = m.el.querySelector('[data-testid="assist-watch-action-error"]');
    expect(hint).toBeTruthy();
    expect(hint.textContent).toContain(t('assist_error_nothing_to_end'));
    // 失败后已拉一次真源(refresh),最终态由服务端收敛而来
    expect(api.getAssistSession).toHaveBeenCalled();
    expect(a.state.value).toBe('ended');
  });
});

describe('AssistWatchPage Esc 返回', () => {
  it('Esc → 返回仪表盘(组件自管 esc 层)', async () => {
    const a = useAssist();
    a.state.value = 'active';
    a.sessionId.value = 's1';
    currentPage.value = 'assist';
    await mountPage();
    const ev = new KeyboardEvent('keydown', { key: 'Escape' });
    window.dispatchEvent(ev);
    await nextTick();
    expect(currentPage.value).toBe('dashboard');
  });
});

// 终端单窗格（Task 15, plan 2026-11-21-web-terminal-phase1 §Task 15）：轻组件
// 测试。仓内手法 = createApp + h 挂载（同 PinGuard.test.js）;mock 掉
// @xterm/xterm / @xterm/addon-fit / useTerminalSocket（vi.mock 工厂随 import
// 提升到文件头,工厂内引用的顶层对象必须经 vi.hoisted 同步提升,同
// UserInfoSettings.test.js）。ResizeObserver 在 jsdom 不存在,注入捕获回调的
// 桩以驱动去抖断言。文案期望经 t() 动态取——term_* 键 Task 16 才入词表,
// 缺失时 t() 回落键名本身（src/i18n/index.js 既定行为）,断言两侧同源故先后
// 皆成立。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { createApp, h, nextTick } from 'vue';
import TerminalPane from './TerminalPane.vue';
import { useI18n } from '../../i18n';
import { createTerminalSocket } from '../../composables/useTerminalSocket';

const mocks = vi.hoisted(() => ({
  // xterm Terminal 假体：记录 open/write/dispose 与 onData 回调,行列为常量
  termInstances: [],
  // ResizeObserver 桩状态：捕获最近一次回调与全部实例
  ro: { instances: [] },
}));

vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    constructor(opts) {
      this.opts = opts;
      this.rows = 24;
      this.cols = 80;
      this.written = '';
      this.disposed = false;
      this.dataHandlers = [];
      mocks.termInstances.push(this);
    }
    loadAddon(addon) { this.addon = addon; }
    open(el) { this.openedAt = el; }
    write(data) { this.written += data; }
    onData(cb) {
      this.dataHandlers.push(cb);
      return { dispose: () => { this.dataHandlers = this.dataHandlers.filter((f) => f !== cb); } };
    }
    dispose() { this.disposed = true; }
  },
}));

vi.mock('@xterm/addon-fit', () => ({
  FitAddon: class {
    constructor() { this.fitCalls = 0; }
    fit() { this.fitCalls += 1; }
  },
}));

vi.mock('../../composables/useTerminalSocket', () => ({
  createTerminalSocket: vi.fn(),
}));

const { t } = useI18n();

let sockHandlers;
let sock;

beforeEach(() => {
  vi.clearAllMocks();
  mocks.termInstances.length = 0;
  mocks.ro.instances.length = 0;
  sockHandlers = {};
  sock = {
    input: vi.fn(),
    resize: vi.fn(),
    close: vi.fn(),
    dispose: vi.fn(),
  };
  createTerminalSocket.mockImplementation((opts) => {
    sockHandlers.frame = opts.onFrame;
    sockHandlers.status = opts.onStatus;
    sockHandlers.exit = opts.onExit;
    return sock;
  });
  global.ResizeObserver = class {
    constructor(cb) { this.cb = cb; this.disconnected = false; mocks.ro.instances.push(this); }
    observe(el) { this.el = el; }
    unobserve() { /* noop */ }
    disconnect() { this.disconnected = true; }
  };
});

afterEach(() => {
  delete global.ResizeObserver;
  document.body.innerHTML = '';
});

async function mount() {
  const el = document.createElement('div');
  document.body.appendChild(el);
  const events = { exit: 0, dead: 0 };
  const app = createApp({
    render: () => h(TerminalPane, {
      token: 'tok_abc',
      onExit: () => { events.exit += 1; },
      onDead: () => { events.dead += 1; },
    }),
  });
  app.mount(el);
  await nextTick();
  const term = mocks.termInstances[0];
  return { el, events, app, term, opts: createTerminalSocket.mock.calls[0][0] };
}

describe('TerminalPane', () => {
  it('挂载即建 socket（web- 会话号 + 当前行列）并 open 进宿主 + 首次 fit', async () => {
    const m = await mount();
    expect(createTerminalSocket).toHaveBeenCalledTimes(1);
    expect(m.opts.token).toBe('tok_abc');
    expect(m.opts.sessionId).toMatch(/^web-[a-z0-9]{8}$/);
    expect(m.opts.rows).toBe(24);
    expect(m.opts.cols).toBe(80);
    // xterm open 进 pane 内宿主 div,fit addon 已挂载且首调
    expect(m.term.openedAt).toBe(m.el.querySelector('[data-testid="terminal-pane"] > div'));
    expect(m.term.addon.fitCalls).toBe(1);
    // ResizeObserver 观察的是宿主 div 本体
    expect(mocks.ro.instances[0].el).toBe(m.term.openedAt);
  });

  it('onFrame：字符串分片按序 write,空串/非字符串跳过', async () => {
    const m = await mount();
    sockHandlers.frame({ type: 'terminal.output', data: 'hel' });
    sockHandlers.frame({ type: 'terminal.replay', data: 'lo\n' });
    sockHandlers.frame({ type: 'terminal.output', data: '' });
    sockHandlers.frame({ type: 'terminal.output', data: null });
    sockHandlers.frame({ type: 'terminal.output', data: 123 });
    expect(m.term.written).toBe('hello\n');
  });

  it('onData → sock.input（键盘输入透传）', async () => {
    const m = await mount();
    m.term.dataHandlers[0]('ls\r');
    expect(sock.input).toHaveBeenCalledWith('ls\r');
  });

  it('状态徽标:closed→重连中 / error→服务端文案 / created→清空', async () => {
    const m = await mount();
    sockHandlers.status({ kind: 'closed', code: 1006 });
    await nextTick();
    expect(m.el.querySelector('[data-testid="terminal-status"]').textContent).toContain(t('term_reconnecting'));
    sockHandlers.status({ kind: 'error', message: 'boom' });
    await nextTick();
    expect(m.el.querySelector('[data-testid="terminal-status"]').textContent).toContain('boom');
    sockHandlers.status({ kind: 'created', resumed: false, pty: true });
    await nextTick();
    expect(m.el.querySelector('[data-testid="terminal-status"]')).toBe(null);
  });

  it('exit → 徽标 + 上抛 exit 一次', async () => {
    const m = await mount();
    sockHandlers.exit({ type: 'terminal.exit', code: 0 });
    await nextTick();
    expect(m.el.querySelector('[data-testid="terminal-status"]').textContent).toContain(t('term_exit'));
    expect(m.events.exit).toBe(1);
    expect(m.events.dead).toBe(0);
  });

  it('dead 闩：连抛两次 dead 只上抛一次（工作区不重复回退 PinGuard）', async () => {
    const m = await mount();
    sockHandlers.status({ kind: 'dead' });
    sockHandlers.status({ kind: 'dead' });
    await nextTick();
    expect(m.el.querySelector('[data-testid="terminal-status"]').textContent).toContain(t('term_disconnected'));
    expect(m.events.dead).toBe(1);
  });

  it('本会话 error → 退避 1.5s 后以全新随机 ID 自动重建(旧 sock 已释放)', async () => {
    vi.useFakeTimers();
    try {
      const m = await mount();
      const firstId = createTerminalSocket.mock.calls[0][0].sessionId;
      sockHandlers.status({ kind: 'error', message: 'terminal session limit reached: 4', sessionId: firstId });
      await nextTick();
      expect(m.el.querySelector('[data-testid="terminal-status"]').textContent).toContain(t('term_reconnecting'));
      expect(createTerminalSocket).toHaveBeenCalledTimes(1);
      vi.advanceTimersByTime(1500);
      await nextTick();
      expect(createTerminalSocket).toHaveBeenCalledTimes(2);
      const secondOpts = createTerminalSocket.mock.calls[1][0];
      expect(secondOpts.sessionId).toMatch(/^web-[a-z0-9]{8}$/);
      expect(secondOpts.sessionId).not.toBe(firstId); // 重建=全新会话,不再打死 ID
      expect(secondOpts.token).toBe('tok_abc');
      expect(sock.dispose).toHaveBeenCalledTimes(1); // 旧 socket 先释放
    } finally {
      vi.useRealTimers();
    }
  });

  it('重建链:第二次 error → 退避 4s 第三次建连;第三次 error 触顶停重建', async () => {
    vi.useFakeTimers();
    try {
      const m = await mount();
      sockHandlers.status({ kind: 'error', message: 'limit', sessionId: createTerminalSocket.mock.calls[0][0].sessionId });
      vi.advanceTimersByTime(1500);
      sockHandlers.status({ kind: 'error', message: 'limit', sessionId: createTerminalSocket.mock.calls[1][0].sessionId });
      vi.advanceTimersByTime(4000);
      expect(createTerminalSocket).toHaveBeenCalledTimes(3);
      sockHandlers.status({ kind: 'error', message: 'limit', sessionId: createTerminalSocket.mock.calls[2][0].sessionId });
      vi.advanceTimersByTime(10000);
      await nextTick();
      expect(createTerminalSocket).toHaveBeenCalledTimes(3); // 上限 2 次重建
      // 触顶后错误文案保留(用户手动关窗重开,不再静默重建)
      expect(m.el.querySelector('[data-testid="terminal-status"]').textContent).toContain('limit');
    } finally {
      vi.useRealTimers();
    }
  });

  it('全局 error(session_id 空,如 re-locked)不触发重建——重建只会再撞闸', async () => {
    vi.useFakeTimers();
    try {
      const m = await mount();
      sockHandlers.status({ kind: 'error', message: 'web terminal re-locked', sessionId: '' });
      vi.advanceTimersByTime(10000);
      await nextTick();
      expect(createTerminalSocket).toHaveBeenCalledTimes(1);
      expect(m.el.querySelector('[data-testid="terminal-status"]').textContent).toContain('web terminal re-locked');
    } finally {
      vi.useRealTimers();
    }
  });

  it('重建期间卸载:定时器已清,不再重建,新 sock 不泄漏', async () => {
    vi.useFakeTimers();
    try {
      const m = await mount();
      sockHandlers.status({ kind: 'error', message: 'limit', sessionId: createTerminalSocket.mock.calls[0][0].sessionId });
      m.app.unmount(); // 卸载时重建定时器挂起中
      vi.advanceTimersByTime(5000);
      expect(createTerminalSocket).toHaveBeenCalledTimes(1); // 卸载斩断重建
      expect(sock.dispose).toHaveBeenCalledTimes(1); // 仅卸载链自己的 dispose
    } finally {
      vi.useRealTimers();
    }
  });

  it('ResizeObserver 100ms 去抖后 fit + resize', async () => {
    vi.useFakeTimers();
    try {
      const m = await mount();
      const initialFits = m.term.addon.fitCalls;
      mocks.ro.instances[0].cb();
      mocks.ro.instances[0].cb(); // 连续两跳只留一次
      vi.advanceTimersByTime(99);
      expect(m.term.addon.fitCalls).toBe(initialFits); // 未满去抖窗不 fit
      expect(sock.resize).not.toHaveBeenCalled();
      vi.advanceTimersByTime(1);
      expect(m.term.addon.fitCalls).toBe(initialFits + 1); // 恰 100ms 触发一次
      expect(sock.resize).toHaveBeenCalledWith(24, 80);
    } finally {
      vi.useRealTimers();
    }
  });

  it('卸载：observer 断开 + sock/term 释放 + 去抖定时器不再触发 fit', async () => {
    vi.useFakeTimers();
    try {
      const m = await mount();
      mocks.ro.instances[0].cb(); // 挂起未触发的去抖
      m.app.unmount();
      expect(sock.dispose).toHaveBeenCalledTimes(1);
      expect(m.term.disposed).toBe(true);
      // onData 订阅经 dataSub.dispose() 注销:mock 的 dataHandlers 应被清空
      expect(m.term.dataHandlers.length).toBe(0);
      expect(mocks.ro.instances[0].disconnected).toBe(true);
      vi.advanceTimersByTime(1000); // 定时器已清,推进不 fit 不 resize
      expect(sock.resize).not.toHaveBeenCalled();
      expect(m.term.addon.fitCalls).toBe(1);
    } finally {
      vi.useRealTimers();
    }
  });
});

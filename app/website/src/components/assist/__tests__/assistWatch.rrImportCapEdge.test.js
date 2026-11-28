// 远程协助 P3 评审 Fix 1(3/3):帽顶缓冲 degrade 边缘。
// 恰在帽顶(2000)的缓冲再续入一帧 → trim 挤掉最老合成 Meta → 底座破 →
// 整段清空;清空后的续入路径无重试锚(pendingBootEvents 非空才点火),不空拉
// import,等新快照走强制重试收尾。
// 「每文件一条失败→恢复弧」的约束与共享夹具见 assistWatchHarness.js 头注。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

const mocks = vi.hoisted(() => ({
  termInstances: [],
  ro: { instances: [] },
  replayers: [],
  rrwebImportShouldFail: true, // 失败开局(弧约束见 harness 头注)
  rrwebImportCalls: 0,
}));

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

// 抛错的 mock factory 不进缓存,下一次 import 重新求值——先失败后成功即靠它。
vi.mock('rrweb', () => {
  mocks.rrwebImportCalls += 1;
  if (mocks.rrwebImportShouldFail) {
    throw new Error('chunk 404 (simulated redeploy)');
  }
  return {
    Replayer: class {
      constructor(initialEvents, config) {
        this.initialEvents = initialEvents;
        this.config = config;
        this.added = [];
        this.liveStarted = false;
        this.destroyed = false;
        this.iframe = {
          attrs: {},
          style: {},
          setAttribute(k, v) { this.attrs[k] = v; },
        };
        this.wrapper = { style: {} };
        mocks.replayers.push(this);
      }
      startLive() { this.liveStarted = true; }
      addEvent(ev) { this.added.push(ev); }
      destroy() { this.destroyed = true; }
    },
  };
});

const api = vi.hoisted(() => ({
  getAssistGates: vi.fn(),
  getAssistSession: vi.fn(),
  requestAssist: vi.fn(),
  cancelAssist: vi.fn(),
  endAssist: vi.fn(),
}));
vi.mock('../../../services/agentApi', () => api);

import { resetForTests } from '../../../composables/useAssist';
import {
  FakeWebSocket, currentPage, snap, mountActive, unmountAll, send,
  rrMeta, rrFullSnap, rrIncremental,
} from './assistWatchHarness';

beforeEach(() => {
  for (const fn of Object.values(api)) fn.mockReset();
  api.getAssistSession.mockResolvedValue(snap());
  resetForTests();
  FakeWebSocket.instances = [];
  globalThis.WebSocket = FakeWebSocket;
  mocks.termInstances.length = 0;
  mocks.ro.instances.length = 0;
  mocks.replayers.length = 0;
  currentPage.value = 'dashboard';
  global.ResizeObserver = class {
    constructor(cb) { this.cb = cb; mocks.ro.instances.push(this); }
    observe(el) { this.el = el; }
    disconnect() { this.disconnected = true; }
  };
});

afterEach(() => {
  vi.useRealTimers();
  unmountAll();
  resetForTests();
  document.body.innerHTML = '';
  delete global.ResizeObserver;
});

describe('AssistWatchPage rrweb import 失败·帽顶 degrade(清空后无重试锚)', () => {
  it('第 2001 帧挤掉底座 → 清空且续入路径不重拉,新快照强制重试收尾', async () => {
    vi.useFakeTimers();
    const { sock } = await mountActive();
    send(sock, { type: 'assist.rr.snapshot', session_id: 's1', data: JSON.stringify(rrFullSnap()) });
    // 精确 1998 条增量:缓冲 2000(帽顶),底座完好
    for (let i = 0; i < 1998; i += 1) {
      send(sock, { type: 'assist.rr.event', session_id: 's1', data: JSON.stringify(rrIncremental()) });
    }
    await vi.advanceTimersByTimeAsync(0);
    expect(mocks.rrwebImportCalls).toBe(1);

    // 过冷却后第 2001 帧续入 → trim 丢最老合成 Meta → 底座破 → 整段清空。
    // 清空后 appendPendingBoot 的重试锚不触发——degrade 到「等新快照」形态,
    // 不空拉 import。
    mocks.rrwebImportShouldFail = false;
    await vi.advanceTimersByTimeAsync(5000);
    send(sock, { type: 'assist.rr.event', session_id: 's1', data: JSON.stringify(rrIncremental()) });
    await vi.advanceTimersByTimeAsync(0);
    expect(mocks.rrwebImportCalls).toBe(1); // 清空路径无重试锚
    expect(mocks.replayers.length).toBe(0);

    // 新快照 → 新底座强制重试 → 恢复
    const meta = rrMeta();
    const full2 = rrFullSnap();
    send(sock, { type: 'assist.rr.snapshot', session_id: 's1', data: JSON.stringify([meta, full2]) });
    await vi.advanceTimersByTimeAsync(0);
    expect(mocks.rrwebImportCalls).toBe(2);
    expect(mocks.replayers.length).toBe(1);
    expect(mocks.replayers[0].added).toEqual([meta, full2]);
  });
});

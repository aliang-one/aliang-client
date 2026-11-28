// 远程协助 P3 评审 Fix 1(2/3):import 失败·缓冲保留路径(路径 a)。
// 底座仍在帽内 → 5s 冷却内续入不重拉(防 404 以帧率打网络面板)→ 过冷却
// 后下一次续入自动重拉,按序注入零丢帧。
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
  rrFullSnap, rrIncremental,
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

describe('AssistWatchPage rrweb import 失败·路径 a(缓冲保留+冷却后续入重拉)', () => {
  it('冷却内续入不重试;过 5s 冷却下一次续入重拉,失败窗口内的帧一条不丢', async () => {
    vi.useFakeTimers();
    const { sock } = await mountActive();
    send(sock, { type: 'assist.rr.snapshot', session_id: 's1', data: JSON.stringify(rrFullSnap()) });
    for (let i = 0; i < 100; i += 1) {
      send(sock, { type: 'assist.rr.event', session_id: 's1', data: JSON.stringify(rrIncremental()) });
    }
    await vi.advanceTimersByTimeAsync(0); // import #1 抛 404,缓冲(2+100)完好保留
    expect(mocks.rrwebImportCalls).toBe(1);
    expect(mocks.replayers.length).toBe(0);

    // 冷却内续入:不重拉
    send(sock, { type: 'assist.rr.event', session_id: 's1', data: JSON.stringify(rrIncremental()) });
    await vi.advanceTimersByTimeAsync(0);
    expect(mocks.rrwebImportCalls).toBe(1);

    // 过 5s 冷却:下一次续入重拉 import(本次成功)→ flush 按序注入全量
    mocks.rrwebImportShouldFail = false;
    await vi.advanceTimersByTimeAsync(5000);
    send(sock, { type: 'assist.rr.event', session_id: 's1', data: JSON.stringify(rrIncremental()) });
    await vi.advanceTimersByTimeAsync(0);
    expect(mocks.rrwebImportCalls).toBe(2);
    expect(mocks.replayers.length).toBe(1);
    const rp = mocks.replayers[0];
    // 合成 Meta + 快照 + 102 增量(100 失败窗口 + 冷却内 1 + 过冷却触发帧 1,
    // 全部按序保留零丢帧)
    expect(rp.added.length).toBe(104);
    expect(rp.added[0].type).toBe(4);
    expect(rp.added[1].type).toBe(2);
    expect(rp.added[103].type).toBe(3);
  });
});

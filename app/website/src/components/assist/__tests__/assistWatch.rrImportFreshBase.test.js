// 远程协助 P3 终审 Minor-2:失败残留缓冲在途时收到「Meta+FullSnapshot 成对」
// 的新快照 → 续入路径判定 fresh base,绕过 5s 冷却强制重试 import——与
// 「缓冲空开的新底座」(pageBoot 空开那一拍)同一意图:用户可见的新页面,
// 恢复优先。此前该拍走 appendPendingBoot 默认冷却路径,新快照要在残留缓冲
// 的尾上干等 5s。
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

describe('AssistWatchPage rrweb import·失败残留缓冲上的新底座(Minor-2)', () => {
  it('失败后缓冲保留(底座完好),冷却内收到 Meta+FullSnapshot 新快照 → 绕冷却立即重拉恢复', async () => {
    vi.useFakeTimers();
    const { sock } = await mountActive();
    // 首底座失败:缓冲 [合成Meta, FullSnapshot] 保留(远小于帽,底座完好)
    send(sock, { type: 'assist.rr.snapshot', session_id: 's1', data: JSON.stringify(rrFullSnap()) });
    send(sock, { type: 'assist.rr.event', session_id: 's1', data: JSON.stringify(rrIncremental()) });
    await vi.advanceTimersByTimeAsync(0);
    expect(mocks.rrwebImportCalls).toBe(1);
    expect(mocks.replayers.length).toBe(0);

    // 冷却内:纯增量续入不重拉(冷却语义照旧)
    send(sock, { type: 'assist.rr.event', session_id: 's1', data: JSON.stringify(rrIncremental()) });
    await vi.advanceTimersByTimeAsync(0);
    expect(mocks.rrwebImportCalls).toBe(1);

    // 冷却内收到「Meta+FullSnapshot 成对」新快照(数组形态走 pageBoot 续尾):
    // fresh base 判定 → 绕冷却立即重拉(不等 5s)
    mocks.rrwebImportShouldFail = false;
    const meta2 = rrMeta();
    const full2 = rrFullSnap();
    send(sock, { type: 'assist.rr.snapshot', session_id: 's1', data: JSON.stringify([meta2, full2]) });
    await vi.advanceTimersByTimeAsync(0);
    expect(mocks.rrwebImportCalls).toBe(2); // 冷却被 fresh base 绕过
    expect(mocks.replayers.length).toBe(1);
    const rp = mocks.replayers[0];
    // 注入 = 失败残留缓冲全量 + 新快照,按到达序(合成Meta+快照+2增量+新Meta+新快照)
    expect(rp.added.length).toBe(6);
    expect(rp.added[0].type).toBe(4);
    expect(rp.added[1].type).toBe(2);
    expect(rp.added[2].type).toBe(3);
    expect(rp.added[3].type).toBe(3);
    expect(rp.added[4]).toEqual(meta2);
    expect(rp.added[5]).toEqual(full2);
  });
});
